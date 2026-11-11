package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"margin/internal/domain"
)

var ErrNotFound = errors.New("Episode not found.")
var episodeID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Store struct {
	root string
	db   *sql.DB
	mu   sync.Mutex
}

type storedEpisode struct {
	episode  domain.Episode
	raw      map[string]json.RawMessage
	queued   string
	position int
}

func NewStore(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	// SQLite accepts URI query options only when the path is escaped as a URL.
	db, err := sql.Open("sqlite3", sqliteDSN(filepath.Join(abs, "library.sqlite3")))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{root: abs, db: db}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS episodes (id TEXT PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	rows, err := s.records()
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, r := range rows {
		if r.episode.Status == "generating" {
			_, err = s.update(r.episode.ID, false, func(e *domain.Episode) {
				if e.CancelRequested {
					e.Status = "cancelled"
				} else {
					e.Status = "queued"
				}
				e.Progress = 0
				e.Error = nil
				e.ETASeconds = nil
				e.GenerationPhase = nil
				e.CancelRequested = false
			})
			if err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path(id string) (string, error) {
	if !episodeID.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.root, id), nil
}
func decodeRecord(body []byte) (storedEpisode, error) {
	var r storedEpisode
	if err := json.Unmarshal(body, &r.episode); err != nil {
		return r, fmt.Errorf("decode reading: %w", err)
	}
	if err := json.Unmarshal(body, &r.raw); err != nil {
		return r, err
	}
	if !episodeID.MatchString(r.episode.ID) {
		return r, fmt.Errorf("invalid saved reading ID")
	}
	r.queued = r.episode.CreatedAt
	if value, ok := r.raw["queued_at"]; ok {
		var stamp string
		if json.Unmarshal(value, &stamp) == nil && stamp != "" {
			r.queued = stamp
		}
	}
	if r.episode.Cues == nil {
		r.episode.Cues = []domain.Cue{}
	}
	return r, nil
}
func (s *Store) getLocked(id string) (storedEpisode, error) {
	if !episodeID.MatchString(id) {
		return storedEpisode{}, ErrNotFound
	}
	var data []byte
	err := s.db.QueryRow(`SELECT body FROM episodes WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return storedEpisode{}, ErrNotFound
	}
	if err != nil {
		return storedEpisode{}, err
	}
	record, err := decodeRecord(data)
	if err == nil && record.episode.ID != id {
		return storedEpisode{}, fmt.Errorf("saved reading ID does not match its database key")
	}
	return record, err
}
func (s *Store) Get(id string) (domain.Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.getLocked(id)
	return r.episode, err
}
func (s *Store) records() ([]storedEpisode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,body FROM episodes ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []storedEpisode{}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		r, err := decodeRecord(data)
		if err != nil {
			return nil, err
		}
		if r.episode.ID != id {
			return nil, fmt.Errorf("saved reading ID does not match its database key")
		}
		r.position = len(records)
		records = append(records, r)
	}
	return records, rows.Err()
}
func (s *Store) List() ([]domain.Episode, error) {
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	result := make([]domain.Episode, 0, len(records))
	for _, r := range records {
		result = append(result, r.episode)
	}
	return result, nil
}
func (s *Store) nextQueued() (domain.Episode, bool, error) {
	records, err := s.records()
	if err != nil {
		return domain.Episode{}, false, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, ae := time.Parse(time.RFC3339Nano, records[i].queued)
		b, be := time.Parse(time.RFC3339Nano, records[j].queued)
		// Old or hand-edited records without timestamps precede dated work,
		// preserving their durable insertion order and a transitive ordering.
		if ae != nil || be != nil {
			if (ae != nil) != (be != nil) {
				return ae != nil
			}
			return records[i].position > records[j].position
		}
		if !a.Equal(b) {
			return a.Before(b)
		}
		return records[i].position > records[j].position
	})
	for _, r := range records {
		if r.episode.Status == "queued" {
			return r.episode, true, nil
		}
	}
	return domain.Episode{}, false, nil
}
func (s *Store) putLocked(e domain.Episode, raw map[string]json.RawMessage, queued bool) error {
	if !episodeID.MatchString(e.ID) {
		return ErrNotFound
	}
	if e.Cues == nil {
		e.Cues = []domain.Cue{}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	delete(raw, "cancel_requested")
	delete(raw, "recast_of")
	for k, v := range fields {
		raw[k] = v
	}
	if queued {
		stamp, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
		raw["queued_at"] = stamp
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO episodes (id,body) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body`, e.ID, string(data))
	return err
}
func (s *Store) Put(e domain.Episode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw map[string]json.RawMessage
	if previous, err := s.getLocked(e.ID); err == nil {
		raw = previous.raw
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.putLocked(e, raw, false)
}
func (s *Store) insertQueued(e domain.Episode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putLocked(e, nil, true)
}
func (s *Store) update(id string, queued bool, change func(*domain.Episode)) (domain.Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.getLocked(id)
	if err != nil {
		return domain.Episode{}, err
	}
	change(&r.episode)
	if err = s.putLocked(r.episode, r.raw, queued); err != nil {
		return domain.Episode{}, err
	}
	return r.episode, nil
}
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM episodes WHERE id=?`, id)
	return err
}
