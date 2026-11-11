// Package speech implements local Kokoro inference and English pronunciation.
// It does not invoke external speech programs or make network requests.
package speech

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	ort "github.com/yalue/onnxruntime_go"
	"margin/internal/domain"
)

const SampleRate = 24000
const maxPhonemes = 510
const engineName = "Kokoro · local CPU"

var requiredAssets = []string{"tokens.txt", "lexicon-us-en.txt.gz", "lexicon-gb-en.txt.gz", "g2p-us.onnx", "g2p-us.json", "g2p-gb.onnx", "g2p-gb.json"}
var voiceNames = [][2]string{
	{"af_heart", "Heart"}, {"af_bella", "Bella"}, {"af_nicole", "Nicole"}, {"af_sarah", "Sarah"}, {"af_alloy", "Alloy"}, {"af_aoede", "Aoede"}, {"af_jessica", "Jessica"}, {"af_kore", "Kore"}, {"af_nova", "Nova"}, {"af_river", "River"}, {"af_sky", "Sky"},
	{"am_michael", "Michael"}, {"am_adam", "Adam"}, {"am_echo", "Echo"}, {"am_eric", "Eric"}, {"am_fenrir", "Fenrir"}, {"am_liam", "Liam"}, {"am_onyx", "Onyx"}, {"am_puck", "Puck"}, {"am_santa", "Santa"},
	{"bf_emma", "Emma"}, {"bf_alice", "Alice"}, {"bf_isabella", "Isabella"}, {"bf_lily", "Lily"}, {"bm_george", "George"}, {"bm_daniel", "Daniel"}, {"bm_fable", "Fable"}, {"bm_lewis", "Lewis"},
}

type Engine struct {
	modelDir, assetDir, libraryPath string
	mu                              sync.Mutex
	closed                          atomic.Bool
	runtimeHeld                     bool
	model                           *ort.DynamicAdvancedSession
	styles                          map[string][]float32
	vocabulary                      map[rune]int64
	pronouncers                     map[bool]*pronouncer
}

// New constructs an engine without eagerly loading its models. Status reports
// missing assets, so the web interface can still explain setup errors.
func New(modelDir, pronunciationDir, ortLibraryPath string) (*Engine, error) {
	if pronunciationDir == "" {
		pronunciationDir = filepath.Join("assets", "pronunciation")
	}
	if ortLibraryPath == "" {
		ortLibraryPath = os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH")
	}
	if ortLibraryPath == "" {
		ortLibraryPath = "libonnxruntime.so"
	}
	return &Engine{modelDir: modelDir, assetDir: pronunciationDir, libraryPath: ortLibraryPath, pronouncers: make(map[bool]*pronouncer)}, nil
}

func (e *Engine) Status() domain.EngineStatus {
	return e.status()
}
func (e *Engine) status() domain.EngineStatus {
	status := domain.EngineStatus{Name: engineName, Message: "Ready. Speech is generated on this computer.", Ready: true}
	if e.closed.Load() {
		status.Ready = false
		status.Message = "The speech engine has been closed."
		return status
	}
	paths := []string{filepath.Join(e.modelDir, "kokoro-v1.0.onnx"), filepath.Join(e.modelDir, "voices-v1.0.bin")}
	for _, name := range requiredAssets {
		paths = append(paths, filepath.Join(e.assetDir, name))
	}
	if strings.ContainsRune(e.libraryPath, os.PathSeparator) {
		paths = append(paths, e.libraryPath)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			status.Ready = false
			status.Message = "Speech assets are missing. Run ./setup.sh to install the local speech engine."
			return status
		}
	}
	return status
}
func (e *Engine) Voices() []domain.Voice {
	result := make([]domain.Voice, 0, len(voiceNames))
	for _, voice := range voiceNames {
		accent, gender := "American", "male"
		if voice[0][0] == 'b' {
			accent = "British"
		}
		if voice[0][1] == 'f' {
			gender = "female"
		}
		result = append(result, domain.Voice{ID: voice[0], ShortName: voice[1], Name: voice[1] + " · " + accent + " " + gender, Accent: accent, Gender: gender})
	}
	return result
}
func (e *Engine) load() error {
	if e.model != nil {
		return nil
	}
	if status := e.status(); !status.Ready {
		return errors.New(status.Message)
	}
	if !e.runtimeHeld {
		if err := acquireRuntime(e.libraryPath); err != nil {
			return err
		}
		e.runtimeHeld = true
	}
	styles, err := loadStyles(filepath.Join(e.modelDir, "voices-v1.0.bin"))
	if err != nil {
		return err
	}
	vocab, err := loadVocabulary(filepath.Join(e.assetDir, "tokens.txt"))
	if err != nil {
		return err
	}
	model, err := newSession(filepath.Join(e.modelDir, "kokoro-v1.0.onnx"), []string{"input_ids", "style", "speed"}, []string{"waveform"}, 4)
	if err != nil {
		return fmt.Errorf("load Kokoro: %w", err)
	}
	e.styles, e.vocabulary, e.model = styles, vocab, model
	return nil
}
func validVoice(voice string) bool {
	for _, v := range voiceNames {
		if v[0] == voice {
			return true
		}
	}
	return false
}

// preparePronouncer is shared by validation and synthesis; the caller holds e.mu.
func (e *Engine) preparePronouncer(voice string) (*pronouncer, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	british := voice[0] == 'b'
	p := e.pronouncers[british]
	if p == nil {
		var err error
		p, err = newPronouncer(e.assetDir, british)
		if err != nil {
			return nil, err
		}
		e.pronouncers[british] = p
	}
	return p, nil
}

// ValidateText checks one utterance without generating audio. Successful word
// pronunciations use the same bounded caches as subsequent synthesis.
func (e *Engine) ValidateText(ctx context.Context, text, voice string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("Enter some text to narrate.")
	}
	if !validVoice(voice) {
		return errors.New("Choose an available voice.")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := e.preparePronouncer(voice)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = p.phonemize(ctx, strings.TrimSpace(text))
	return err
}

func (e *Engine) Synthesize(ctx context.Context, text, voice string, speed float64) ([]float32, int, error) {
	if strings.TrimSpace(text) == "" {
		return nil, 0, errors.New("Enter some text to narrate.")
	}
	if !validVoice(voice) {
		return nil, 0, errors.New("Choose an available voice.")
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < .5 || speed > 2 {
		return nil, 0, errors.New("Narration speed must be between 0.5 and 2.0.")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	p, err := e.preparePronouncer(voice)
	if err != nil {
		return nil, 0, err
	}
	phones, err := p.phonemize(ctx, strings.TrimSpace(text))
	if err != nil {
		return nil, 0, err
	}
	batches := phonemeBatches(phones)
	var result []float32
	for i, batch := range batches {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		runes := []rune(batch)
		tokens := make([]int64, len(runes)+2)
		for j, phone := range runes {
			id, ok := e.vocabulary[phone]
			if !ok {
				return nil, 0, errors.New("The pronunciation engine returned unsupported speech symbols.")
			}
			tokens[j+1] = id
		}
		style := e.styles[voice][(len(runes)-1)*256 : len(runes)*256]
		samples, err := e.infer(ctx, tokens, style, float32(speed))
		if err != nil {
			return nil, 0, err
		}
		trimmed, err := trimSilence(samples)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, trimmed...)
		if i+1 < len(batches) {
			pause := .1
			if strings.ContainsRune(".!?", runes[len(runes)-1]) {
				pause = .25
			}
			result = append(result, make([]float32, int(math.RoundToEven(pause*SampleRate/speed)))...)
		}
	}
	if len(result) == 0 {
		return nil, 0, errors.New("No speakable text was found.")
	}
	return result, SampleRate, nil
}
func (e *Engine) infer(ctx context.Context, ids []int64, style []float32, speed float32) ([]float32, error) {
	t, err := ort.NewTensor(ort.NewShape(1, int64(len(ids))), ids)
	if err != nil {
		return nil, err
	}
	defer t.Destroy()
	s, err := ort.NewTensor(ort.NewShape(1, 256), style)
	if err != nil {
		return nil, err
	}
	defer s.Destroy()
	v, err := ort.NewTensor(ort.NewShape(1), []float32{speed})
	if err != nil {
		return nil, err
	}
	defer v.Destroy()
	return runFloat(ctx, e.model, []ort.Value{t, s, v})
}
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return nil
	}
	e.closed.Store(true)
	var errs []error
	if e.model != nil {
		errs = append(errs, e.model.Destroy())
		e.model = nil
	}
	for _, p := range e.pronouncers {
		errs = append(errs, p.close())
	}
	if e.runtimeHeld {
		errs = append(errs, releaseRuntime())
		e.runtimeHeld = false
	}
	e.styles = nil
	e.pronouncers = nil
	return errors.Join(errs...)
}
func phonemeBatches(phones string) []string {
	remaining := []rune(strings.TrimSpace(phones))
	var batches []string
	for len(remaining) > 0 {
		cut := len(remaining)
		if cut > maxPhonemes {
			cut = maxPhonemes
			punct, space := 0, 0
			for i, r := range remaining[:maxPhonemes] {
				if strings.ContainsRune(".!?;:,—", r) {
					punct = i + 1
				}
				if r == ' ' {
					space = i
				}
			}
			if punct >= maxPhonemes/2 {
				cut = punct
			} else if space > 0 {
				cut = space
			}
		}
		batch := strings.TrimSpace(string(remaining[:cut]))
		if batch != "" {
			batches = append(batches, batch)
		}
		remaining = []rune(strings.TrimSpace(string(remaining[cut:])))
	}
	return batches
}
func trimSilence(samples []float32) ([]float32, error) {
	if len(samples) == 0 {
		return nil, errors.New("The speech engine did not produce valid audio.")
	}
	const window = SampleRate / 50
	energies := make([]float64, (len(samples)+window-1)/window)
	peak := 0.0
	for i, v := range samples {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("The speech engine did not produce valid audio.")
		}
		energies[i/window] += f * f / window
	}
	for _, energy := range energies {
		if energy > peak {
			peak = energy
		}
	}
	if peak < 1e-14 {
		return nil, errors.New("The speech engine returned silent audio.")
	}
	first, last := -1, 0
	for i, energy := range energies {
		if energy > peak*1e-6 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	start := max(0, first*window-2*window)
	end := min(len(samples), (last+1)*window+2*window)
	return samples[start:end], nil
}
