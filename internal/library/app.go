package library

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"margin/internal/article"
	"margin/internal/domain"
)

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }
func IsValidation(err error) bool {
	var validation *ValidationError
	return errors.As(err, &validation)
}
func invalid(message string) error { return &ValidationError{message} }

type Options struct {
	DataDir              string
	PreviewDir           string
	DesktopNotifications bool
	Engine               domain.Engine
}

type App struct {
	Token                string
	store                *Store
	engine               domain.Engine
	previewDir           string
	desktopNotifications bool
	mu                   sync.Mutex
	wake                 chan struct{}
	started              bool
	closing              bool
	cancel               context.CancelFunc
	currentCancel        context.CancelFunc
	currentID            string
	wg                   sync.WaitGroup
	closeOnce            sync.Once
	closeErr             error
}

func New(options Options) (*App, error) {
	if options.Engine == nil {
		return nil, errors.New("a speech engine is required")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	store, err := NewStore(options.DataDir)
	if err != nil {
		return nil, err
	}
	return &App{Token: base64.RawURLEncoding.EncodeToString(token), store: store, engine: options.Engine, previewDir: options.PreviewDir, desktopNotifications: options.DesktopNotifications, wake: make(chan struct{}, 1)}, nil
}
func (a *App) Config() map[string]any {
	voices := append([]domain.Voice{}, a.engine.Voices()...)
	for i := range voices {
		if _, err := a.VoicePreview(voices[i].ID); err == nil {
			voices[i].PreviewURL = "/api/voices/" + voices[i].ID + "/preview.wav"
		}
	}
	_, notifyErr := exec.LookPath("notify-send")
	return map[string]any{"engine": a.engine.Status(), "voices": voices, "defaults": map[string]any{"voice": "af_heart", "speed": .95}, "csrf_token": a.Token, "features": map[string]bool{"rename": true, "recast": true}, "desktop_notifications": a.desktopNotifications && notifyErr == nil}
}
func (a *App) List() ([]domain.Episode, error)       { return a.store.List() }
func (a *App) Get(id string) (domain.Episode, error) { return a.store.Get(id) }
func (a *App) Path(id string) (string, error)        { return a.store.Path(id) }
func (a *App) VoicePreview(voice string) (string, error) {
	if a.validateVoice(voice) != nil {
		return "", ErrNotFound
	}
	if filepath.Base(voice) != voice || strings.ContainsAny(voice, `/\`) {
		return "", ErrNotFound
	}
	path := filepath.Join(a.previewDir, voice+".wav")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrNotFound
	}
	return path, nil
}
func (a *App) validateVoice(value any) error {
	voice, ok := value.(string)
	if ok {
		for _, entry := range a.engine.Voices() {
			if entry.ID == voice {
				return nil
			}
		}
	}
	return invalid("Choose one of the available English voices.")
}
func truncate(value string, limit int) string {
	r := []rune(value)
	if len(r) > limit {
		return string(r[:limit])
	}
	return value
}
func numeric(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, errors.New("not a number")
	}
}
func (a *App) Create(body map[string]any) (domain.Episode, error) {
	raw, ok := body["text"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return domain.Episode{}, invalid("Paste some text to narrate first.")
	}
	if utf8.RuneCountInString(raw) > domain.MaxText {
		return domain.Episode{}, invalid("Please split articles longer than 150,000 characters into parts.")
	}
	text := article.PrepareText(raw)
	if strings.TrimSpace(text) == "" {
		return domain.Episode{}, invalid("No readable text was found.")
	}
	title := truncate(strings.SplitN(text, "\n", 2)[0], 100)
	if v, exists := body["title"]; exists && v != nil && v != "" {
		var valid bool
		title, valid = v.(string)
		if !valid {
			return domain.Episode{}, invalid("Title must be text.")
		}
	}
	title = truncate(strings.TrimSpace(title), 200)
	if title == "" {
		title = "Untitled reading"
	}
	voice := any("af_heart")
	if v, exists := body["voice"]; exists {
		voice = v
	}
	if err := a.validateVoice(voice); err != nil {
		return domain.Episode{}, err
	}
	speed := .95
	if v, exists := body["speed"]; exists {
		var err error
		speed, err = numeric(v)
		if err != nil {
			return domain.Episode{}, invalid("Pace must be between 0.7 and 1.3.")
		}
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < .7 || speed > 1.3 {
		return domain.Episode{}, invalid("Pace must be between 0.7 and 1.3.")
	}
	source := ""
	if v, exists := body["source_url"]; exists && v != nil {
		var valid bool
		source, valid = v.(string)
		if !valid {
			return domain.Episode{}, invalid("Invalid source URL.")
		}
	}
	if utf8.RuneCountInString(source) > 4096 {
		return domain.Episode{}, invalid("Invalid source URL.")
	}
	if source != "" {
		parsed, err := url.Parse(source)
		if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return domain.Episode{}, invalid("Source links must use HTTP or HTTPS.")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.queueLocked(title, text, voice.(string), speed, source, "")
}
func (a *App) capacityLocked() error {
	if a.closing {
		return invalid("The server is shutting down. Please try again after it restarts.")
	}
	all, err := a.store.List()
	if err != nil {
		return err
	}
	count := 0
	for _, e := range all {
		if e.Status == "queued" || e.Status == "generating" {
			count++
		}
	}
	if count >= 20 {
		return invalid("The queue is full. Wait for an episode to finish.")
	}
	return nil
}
func (a *App) queueLocked(title, text, voice string, speed float64, source, recast string) (domain.Episode, error) {
	status := a.engine.Status()
	if !status.Ready {
		return domain.Episode{}, invalid(status.Message)
	}
	if err := a.capacityLocked(); err != nil {
		return domain.Episode{}, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return domain.Episode{}, err
	}
	item := domain.Episode{ID: hex.EncodeToString(id), Title: title, Text: text, Voice: voice, Speed: speed, SourceURL: source, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: "queued", WordCount: len(strings.Fields(text)), Cues: []domain.Cue{}, RecastOf: recast}
	if err := a.store.insertQueued(item); err != nil {
		return domain.Episode{}, err
	}
	a.wakeWorker()
	return item, nil
}
func (a *App) Rename(id string, title any) (domain.Episode, error) {
	value, ok := title.(string)
	if !ok {
		return domain.Episode{}, invalid("Enter a title for this reading.")
	}
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return domain.Episode{}, invalid("Enter a title for this reading.")
	}
	if utf8.RuneCountInString(value) > 200 {
		return domain.Episode{}, invalid("Keep the title to 200 characters or fewer.")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store.update(id, false, func(e *domain.Episode) { e.Title = value })
}
func (a *App) Recast(id string, voice any) (domain.Episode, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	source, err := a.store.Get(id)
	if err != nil {
		return domain.Episode{}, err
	}
	if source.Status != "complete" {
		return domain.Episode{}, invalid("Only finished readings can be recast.")
	}
	if err = a.validateVoice(voice); err != nil {
		return domain.Episode{}, err
	}
	if voice.(string) == source.Voice {
		return domain.Episode{}, invalid("Choose a different voice for this reading.")
	}
	return a.queueLocked(source.Title, source.Text, voice.(string), source.Speed, source.SourceURL, id)
}
func (a *App) Action(id, action string) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, err := a.store.Get(id)
	if err != nil {
		return nil, err
	}
	switch action {
	case "cancel":
		if item.Status == "queued" {
			return a.store.update(id, false, func(e *domain.Episode) {
				e.Status = "cancelled"
				e.Error = nil
				e.ETASeconds = nil
				e.GenerationPhase = nil
				e.CancelRequested = false
			})
		}
		if item.Status == "generating" {
			result, err := a.store.update(id, false, func(e *domain.Episode) { e.CancelRequested = true; e.ETASeconds = nil; e.GenerationPhase = nil })
			if err == nil && a.currentID == id && a.currentCancel != nil {
				a.currentCancel()
			}
			return result, err
		}
		return item, nil
	case "retry":
		if item.Status != "failed" && item.Status != "cancelled" {
			return nil, invalid("Only failed or cancelled episodes can be retried.")
		}
		if err = a.capacityLocked(); err != nil {
			return nil, err
		}
		status := a.engine.Status()
		if !status.Ready {
			return nil, invalid(status.Message)
		}
		result, err := a.store.update(id, true, func(e *domain.Episode) {
			e.Status = "queued"
			e.Progress = 0
			e.Error = nil
			e.CancelRequested = false
			e.Cues = []domain.Cue{}
			e.Duration = 0
			e.ETASeconds = nil
			e.GenerationPhase = nil
		})
		if err == nil {
			a.wakeWorker()
		}
		return result, err
	case "delete":
		if item.Status != "complete" && item.Status != "failed" && item.Status != "cancelled" {
			return nil, invalid("Cancel the episode and wait before deleting it.")
		}
		path, err := a.store.Path(id)
		if err != nil {
			return nil, err
		}
		if err = os.RemoveAll(path); err != nil {
			return nil, err
		}
		if err = a.store.Delete(id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "deleted": true}, nil
	default:
		return nil, ErrNotFound
	}
}
func (a *App) wakeWorker() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func (a *App) Start(parent context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started || a.closing {
		return
	}
	a.started = true
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.worker(ctx) }()
}
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closing = true
		if a.cancel != nil {
			a.cancel()
		}
		if a.currentCancel != nil {
			a.currentCancel()
		}
		a.mu.Unlock()
		a.wakeWorker()
		a.wg.Wait()
		a.closeErr = errors.Join(a.store.Close(), a.engine.Close())
	})
	return a.closeErr
}
func (a *App) worker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		item, exists, err := a.store.nextQueued()
		if err == nil && exists && ctx.Err() == nil {
			item, err = a.store.update(item.ID, false, func(e *domain.Episode) {
				e.Status = "generating"
				e.Progress = 0
				e.Error = nil
				e.CancelRequested = false
				e.ETASeconds = nil
				e.GenerationPhase = stringPointer("starting")
			})
		} else {
			exists = false
		}
		var jobCtx context.Context
		var cancel context.CancelFunc
		if err == nil && exists {
			jobCtx, cancel = context.WithCancel(ctx)
			a.currentID = item.ID
			a.currentCancel = cancel
		}
		a.mu.Unlock()
		if err != nil {
			log.Printf("reading queue failed: %v", err)
		}
		if !exists || err != nil {
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
			case <-time.After(time.Second):
			}
			continue
		}
		err = a.generate(jobCtx, item)
		cancel()
		a.mu.Lock()
		a.currentID = ""
		a.currentCancel = nil
		final, lookupErr := a.store.Get(item.ID)
		if lookupErr == nil && final.Status == "generating" {
			final, lookupErr = a.store.update(item.ID, false, func(e *domain.Episode) {
				e.ETASeconds = nil
				e.GenerationPhase = nil
				e.Progress = 0
				if e.CancelRequested {
					e.Status = "cancelled"
					e.Error = nil
				} else if ctx.Err() != nil {
					e.Status = "queued"
					e.Error = nil
				} else {
					e.Status = "failed"
					message := "Narration failed."
					if err != nil {
						message = fmt.Sprintf("Narration failed: %v", err)
					}
					e.Error = &message
				}
				e.CancelRequested = false
			})
		}
		a.mu.Unlock()
		if lookupErr != nil {
			log.Printf("saving narration result failed: %v", lookupErr)
		} else if final.Status == "complete" {
			a.notify("Your reading is ready", final.Title)
		} else if final.Status == "failed" {
			log.Printf("narration %s failed: %v", item.ID, err)
			a.notify("Narration needs attention", final.Title)
		}
	}
}
func stringPointer(s string) *string { return &s }
func (a *App) notify(title, message string) {
	if !a.desktopNotifications {
		return
	}
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, path, "--app-name=Margin", "--", title, message).Run()
}
