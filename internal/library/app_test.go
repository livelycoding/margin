package library

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"margin/internal/domain"
)

type fakeEngine struct {
	mu     sync.Mutex
	fn     func(context.Context, string, string, float64) ([]float32, int, error)
	closed atomic.Int32
}

func (f *fakeEngine) Status() domain.EngineStatus {
	return domain.EngineStatus{Ready: true, Name: "Test", Message: "Ready"}
}
func (f *fakeEngine) Voices() []domain.Voice {
	return []domain.Voice{{ID: "af_heart", Name: "Heart"}, {ID: "am_adam", Name: "Adam"}}
}
func (f *fakeEngine) Synthesize(ctx context.Context, text, voice string, speed float64) ([]float32, int, error) {
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, text, voice, speed)
	}
	size := 2400
	if voice == "am_adam" {
		size = 4800
	}
	samples := make([]float32, size)
	for i := range samples {
		samples[i] = .05
	}
	return samples, 24000, nil
}
func (f *fakeEngine) Close() error { f.closed.Add(1); return nil }
func (f *fakeEngine) set(fn func(context.Context, string, string, float64) ([]float32, int, error)) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}
func newTestApp(t *testing.T) (*App, *fakeEngine) {
	t.Helper()
	engine := &fakeEngine{}
	app, err := New(Options{DataDir: t.TempDir(), Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	return app, engine
}
func createTest(t *testing.T, app *App, text string) domain.Episode {
	t.Helper()
	e, err := app.Create(map[string]any{"title": "Test reading", "text": text})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func awaitStatus(t *testing.T, app *App, id, status string) domain.Episode {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e, err := app.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Status == status {
			return e
		}
		if e.Status == "failed" && status != "failed" {
			t.Fatalf("failed: %v", e.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	e, _ := app.Get(id)
	t.Fatalf("wanted %s, got %+v", status, e)
	return domain.Episode{}
}
func TestGenerationMeasuredAudioAndTranscript(t *testing.T) {
	app, _ := newTestApp(t)
	item := createTest(t, app, "First sentence.\n\nSecond paragraph.")
	app.Start(context.Background())
	done := awaitStatus(t, app, item.ID, "complete")
	if len(done.Cues) != 2 || done.Cues[0].End != .1 || done.Cues[1].Start <= done.Cues[0].End {
		t.Fatalf("cues: %+v", done.Cues)
	}
	folder, _ := app.Path(item.ID)
	wav, err := os.ReadFile(filepath.Join(folder, "audio.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || binary.LittleEndian.Uint16(wav[22:24]) != 1 || binary.LittleEndian.Uint16(wav[34:36]) != 16 {
		t.Fatalf("bad WAV header")
	}
	frames := binary.LittleEndian.Uint32(wav[40:44]) / 2
	rate := binary.LittleEndian.Uint32(wav[24:28])
	if float64(frames)/float64(rate) != done.Duration {
		t.Fatalf("duration differs from file")
	}
	subtitles, err := os.ReadFile(filepath.Join(folder, "transcript.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(subtitles), "00:00:00,000 --> 00:00:00,100") || !strings.Contains(string(subtitles), "Second paragraph.") {
		t.Fatalf("subtitles: %s", subtitles)
	}
	if done.Progress != 100 || done.ETASeconds != nil || done.GenerationPhase != nil {
		t.Fatalf("terminal metadata not cleared: %+v", done)
	}
}
func TestCancelRetryDeleteAndQueuedFIFO(t *testing.T) {
	app, engine := newTestApp(t)
	first := createTest(t, app, "First.")
	second := createTest(t, app, "Second.")
	third := createTest(t, app, "Third.")
	if _, err := app.Action(first.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Action(first.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	var callsMu sync.Mutex
	var calls []string
	engine.set(func(ctx context.Context, text, voice string, speed float64) ([]float32, int, error) {
		callsMu.Lock()
		calls = append(calls, text)
		callsMu.Unlock()
		return []float32{.1, .2}, 24000, nil
	})
	app.Start(context.Background())
	awaitStatus(t, app, first.ID, "complete")
	awaitStatus(t, app, second.ID, "complete")
	awaitStatus(t, app, third.ID, "complete")
	callsMu.Lock()
	got := append([]string{}, calls...)
	callsMu.Unlock()
	if !reflect.DeepEqual(got, []string{"Second.", "Third.", "First."}) {
		t.Fatalf("FIFO changed: %v", got)
	}
	if _, err := app.Action(first.ID, "delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Get(first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted record returned %v", err)
	}
}
func TestActiveCancellationPreventsRetryDeleteUntilWorkerStops(t *testing.T) {
	app, engine := newTestApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	engine.set(func(ctx context.Context, text, voice string, speed float64) ([]float32, int, error) {
		close(entered)
		<-release
		return nil, 0, ctx.Err()
	})
	item := createTest(t, app, "Wait for cancellation.")
	app.Start(context.Background())
	<-entered
	result, err := app.Action(item.ID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if e := result.(domain.Episode); e.Status != "generating" || !e.CancelRequested {
		t.Fatalf("wrong cancellation state: %+v", e)
	}
	for _, action := range []string{"retry", "delete"} {
		if _, err := app.Action(item.ID, action); !IsValidation(err) {
			t.Fatalf("%s allowed while generating", action)
		}
	}
	close(release)
	done := awaitStatus(t, app, item.ID, "cancelled")
	if done.ETASeconds != nil || done.GenerationPhase != nil || done.CancelRequested {
		t.Fatalf("cancellation not finalized: %+v", done)
	}
	folder, _ := app.Path(item.ID)
	if _, err := os.Stat(filepath.Join(folder, "audio.partial.wav")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial remains: %v", err)
	}
	engine.set(nil)
	if _, err := app.Action(item.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, app, item.ID, "complete")
}
func TestShutdownRequeuesAndClosesEngineOnce(t *testing.T) {
	app, engine := newTestApp(t)
	entered := make(chan struct{})
	engine.set(func(ctx context.Context, text, voice string, speed float64) ([]float32, int, error) {
		close(entered)
		<-ctx.Done()
		return nil, 0, ctx.Err()
	})
	item := createTest(t, app, "Interrupt this reading.")
	app.Start(context.Background())
	<-entered
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if engine.closed.Load() != 1 {
		t.Fatalf("engine closed %d times", engine.closed.Load())
	}
	reopened, err := NewStore(app.store.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e, err := reopened.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "queued" || e.Progress != 0 || e.Error != nil || e.ETASeconds != nil {
		t.Fatalf("shutdown lost queued reading: %+v", e)
	}
}
func TestRenameDuringGenerationAndRecastPreserveSource(t *testing.T) {
	app, engine := newTestApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	engine.set(func(ctx context.Context, text, voice string, speed float64) ([]float32, int, error) {
		once.Do(func() { close(entered); <-release })
		return make([]float32, 2400), 24000, nil
	})
	item, err := app.Create(map[string]any{"title": "Original", "text": "A nested entity: &amp;copy;.\n\nA literal \\*word\\* stays literal.", "speed": 1.15, "source_url": "https://example.com/article"})
	if err != nil {
		t.Fatal(err)
	}
	app.Start(context.Background())
	<-entered
	if _, err := app.Rename(item.ID, " A new\n title for café "); err != nil {
		t.Fatal(err)
	}
	close(release)
	original := awaitStatus(t, app, item.ID, "complete")
	if original.Title != "A new title for café" {
		t.Fatal(original.Title)
	}
	folder, _ := app.Path(item.ID)
	audio, err := os.ReadFile(filepath.Join(folder, "audio.wav"))
	if err != nil {
		t.Fatal(err)
	}
	recast, err := app.Recast(item.ID, "am_adam")
	if err != nil {
		t.Fatal(err)
	}
	for _, same := range []bool{recast.Text == original.Text, recast.Title == original.Title, recast.Speed == original.Speed, recast.SourceURL == original.SourceURL, recast.RecastOf == original.ID, recast.ID != original.ID} {
		if !same {
			t.Fatalf("recast changed saved settings: %+v", recast)
		}
	}
	awaitStatus(t, app, recast.ID, "complete")
	saved, _ := app.Get(item.ID)
	if !reflect.DeepEqual(saved, original) {
		t.Fatalf("source record changed")
	}
	after, _ := os.ReadFile(filepath.Join(folder, "audio.wav"))
	if !reflect.DeepEqual(after, audio) {
		t.Fatalf("source audio changed")
	}
	if _, err := app.Recast(item.ID, "af_heart"); !IsValidation(err) {
		t.Fatal("same voice accepted")
	}
}
func TestQueueBoundIncludesRetryAndConcurrentCreates(t *testing.T) {
	app, _ := newTestApp(t)
	failed := createTest(t, app, "Cancelled.")
	if _, err := app.Action(failed.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := app.Create(map[string]any{"text": "Queued reading."})
			if err == nil {
				success.Add(1)
			} else if !IsValidation(err) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 20 {
		t.Fatalf("accepted %d jobs", success.Load())
	}
	if _, err := app.Action(failed.ID, "retry"); !IsValidation(err) {
		t.Fatalf("retry bypassed queue cap: %v", err)
	}
}
func TestInputValidationAndUnicodeBoundaries(t *testing.T) {
	app, _ := newTestApp(t)
	for _, body := range []map[string]any{{"text": ""}, {"text": "a", "speed": math.NaN()}, {"text": "a", "speed": math.Inf(1)}, {"text": "a", "speed": true}, {"text": "a", "speed": 0}, {"text": "a", "voice": []string{"af_heart"}}, {"text": "a", "title": []string{"invalid"}}, {"text": "a", "source_url": "javascript:alert(1)"}, {"text": strings.Repeat("é", domain.MaxText+1)}} {
		if _, err := app.Create(body); !IsValidation(err) {
			t.Fatalf("invalid body accepted: %v", err)
		}
	}
	item := createTest(t, app, "A reading.")
	for _, title := range []any{nil, 12, []string{"title"}, "\n\t ", strings.Repeat("é", 201)} {
		if _, err := app.Rename(item.ID, title); !IsValidation(err) {
			t.Fatalf("invalid title accepted: %v", title)
		}
	}
	if _, err := app.Rename(item.ID, strings.Repeat("é", 200)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "", strings.Repeat("a", 31), strings.Repeat("g", 32)} {
		if _, err := app.Path(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid path accepted: %s", id)
		}
	}
}
func TestWorkerFailureCleansMetadataAndFiles(t *testing.T) {
	app, engine := newTestApp(t)
	engine.set(func(context.Context, string, string, float64) ([]float32, int, error) {
		return []float32{float32(math.NaN())}, 24000, nil
	})
	item := createTest(t, app, "Invalid samples.")
	app.Start(context.Background())
	done := awaitStatus(t, app, item.ID, "failed")
	if done.Error == nil || done.ETASeconds != nil || done.GenerationPhase != nil {
		t.Fatalf("bad failure state: %+v", done)
	}
	folder, _ := app.Path(item.ID)
	for _, name := range []string{"audio.wav", "audio.partial.wav", "transcript.partial.srt", "transcript.partial.txt"} {
		if _, err := os.Stat(filepath.Join(folder, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected file %s", name)
		}
	}
}
func TestExistingPythonLibraryAndUnknownFieldsSurvive(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(root, "library.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE episodes (id TEXT PRIMARY KEY,body TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("a", 32)
	second := strings.Repeat("b", 32)
	for _, id := range []string{first, second} {
		body := map[string]any{"id": id, "title": "Legacy café", "text": "Saved text.", "voice": "af_heart", "speed": .95, "source_url": "", "created_at": "2026-11-09T12:00:00.000000+00:00", "status": "complete", "progress": 100, "duration": 12.5, "word_count": 2, "cues": []domain.Cue{{Start: 0, End: 1, Text: "Saved text."}}, "error": nil, "custom_metadata": map[string]any{"preserve": true}}
		if id == first {
			body["status"] = "generating"
			body["eta_seconds"] = 50
			body["generation_phase"] = "reading"
		}
		data, _ := json.Marshal(body)
		if _, err = db.Exec(`INSERT INTO episodes VALUES (?,?)`, id, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second || list[1].Status != "queued" || list[1].ETASeconds != nil || list[0].Duration != 12.5 {
		t.Fatalf("bad legacy recovery: %+v", list)
	}
	if _, err = store.update(second, false, func(e *domain.Episode) { e.Title = "Renamed" }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second} {
		var raw string
		if err = store.db.QueryRow(`SELECT body FROM episodes WHERE id=?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, `"custom_metadata":{"preserve":true}`) {
			t.Fatalf("lost unknown field: %s", raw)
		}
	}
}
func TestPendingUserCancellationSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 32)
	if err = store.Put(domain.Episode{ID: id, Status: "generating", CancelRequested: true}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, _ := store.Get(id)
	if e.Status != "cancelled" || e.CancelRequested {
		t.Fatalf("lost user cancellation: %+v", e)
	}
}
