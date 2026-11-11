package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assetsDir() string { return filepath.Join("..", "..", "assets", "pronunciation") }

type pronunciationGolden struct {
	Text       string `json:"text"`
	British    bool   `json:"british"`
	Normalized string `json:"normalized"`
	Phonemes   string `json:"phonemes"`
}

func pronunciationGoldens(t *testing.T) []pronunciationGolden {
	t.Helper()
	data, err := os.ReadFile("testdata/python_pronunciation.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []pronunciationGolden
	if err = json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}
func TestNormalizationPythonParity(t *testing.T) {
	for _, row := range pronunciationGoldens(t) {
		if row.British {
			continue
		}
		t.Run(row.Text, func(t *testing.T) {
			actual, err := normalizeText(row.Text)
			if err != nil {
				t.Fatal(err)
			}
			actual = strings.Join(strings.Fields(actual), " ")
			if actual != row.Normalized {
				t.Errorf("normalization mismatch\n got: %s\nwant: %s", actual, row.Normalized)
			}
		})
	}
}
func TestNormalizationRejectsEmpty(t *testing.T) {
	for _, text := range []string{"", " \n\t "} {
		if _, err := normalizeText(text); err == nil {
			t.Fatal("accepted empty text")
		}
	}
}
func TestPronunciationDictionaryAndFallback(t *testing.T) {
	p, err := newPronouncer(assetsDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.fallbackFunc = func(context.Context, string) (string, error) { calls++; return "zˈɪp", nil }
	for _, row := range []struct{ text, want string }{{"Don't worry, you're home!", p.lexicon["don't"]}, {"The apple.", "ði "}, {"The book.", "ðə "}, {"US", "jˈu ˈɛs"}, {"API", "ˈA pˈi ˈI"}, {"Please record this.", "ɹəkˈɔɹd"}, {"A record spins.", "ɹˈɛkəɹd"}, {"I have read it.", "ɹˈɛd"}, {"A live show.", "lˈIv"}} {
		out, err := p.phonemize(context.Background(), row.text)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, row.want) {
			t.Errorf("%s: missing %q in %q", row.text, row.want, out)
		}
	}
	if calls != 0 {
		t.Errorf("dictionary unexpectedly invoked fallback %d times", calls)
	}
	out, err := p.phonemize(context.Background(), "Hello quizzacious, goodbye.")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out, "zˈɪp,") {
		t.Fatalf("unknown word disappeared: %q, %d calls", out, calls)
	}
	clear(p.cache)
	p.fallbackFunc = func(context.Context, string) (string, error) { return "", errUnpronounceable }
	out, err = p.phonemize(context.Background(), "quizzacious")
	if err != nil {
		t.Fatal(err)
	}
	if out != p.spell("quizzacious") {
		t.Fatalf("failed fallback did not spell word: %s", out)
	}
	clear(p.cache)
	p.fallbackFunc = func(context.Context, string) (string, error) { return "☃", nil }
	if _, err = p.phonemize(context.Background(), "quizzacious"); err == nil {
		t.Fatal("unsupported phones accepted")
	}
	if _, err = p.phonemize(context.Background(), "...?!"); err == nil {
		t.Fatal("punctuation-only input accepted")
	}
}
func TestUnsupportedWritingIsNotDropped(t *testing.T) {
	p, err := newPronouncer(assetsDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	p.fallbackFunc = func(context.Context, string) (string, error) { return "", errUnpronounceable }
	if _, err = p.phonemize(context.Background(), "hello 日本語"); err == nil {
		t.Fatal("non-English word silently omitted")
	}
}
func TestRuntimeErrorsNotHiddenBySpelling(t *testing.T) {
	p, err := newPronouncer(assetsDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("native session failed")
	p.fallbackFunc = func(context.Context, string) (string, error) { return "", failure }
	if _, err = p.phonemize(context.Background(), "quizzacious"); !errors.Is(err, failure) {
		t.Fatal("native error was swallowed")
	}
}
func TestVoiceCatalogAndMissingAssets(t *testing.T) {
	e, _ := New(t.TempDir(), t.TempDir(), "missing.so")
	defer e.Close()
	if e.Status().Ready {
		t.Fatal("missing assets reported ready")
	}
	voices := e.Voices()
	if len(voices) != 28 || voices[0].ID != "af_heart" || voices[27].ID != "bm_lewis" {
		t.Fatal("voice order changed")
	}
	for _, v := range voices {
		if v.ShortName == "" || v.Accent == "" || v.Gender == "" {
			t.Fatal("incomplete voice metadata")
		}
	}
}
func TestSynthesizeRejectsInvalidInputBeforeLoading(t *testing.T) {
	e, _ := New(t.TempDir(), t.TempDir(), "missing.so")
	defer e.Close()
	for _, tc := range []struct {
		text, voice string
		speed       float64
	}{{"", "af_heart", 1}, {"hello", "missing", 1}, {"hello", "af_heart", math.NaN()}, {"hello", "af_heart", math.Inf(1)}, {"hello", "af_heart", .4}, {"hello", "af_heart", 2.1}} {
		if _, _, err := e.Synthesize(context.Background(), tc.text, tc.voice, tc.speed); err == nil {
			t.Fatalf("invalid request accepted: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.Synthesize(ctx, "hello", "af_heart", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request not canceled: %v", err)
	}
}
func TestBatchesPreserveEveryPhone(t *testing.T) {
	for _, input := range []string{strings.Repeat("ɑ", 1600), strings.Repeat("həlˈO wˈɜɹld. ", 200)} {
		batches := phonemeBatches(input)
		if len(batches) < 2 {
			t.Fatal("long input was not split")
		}
		for _, batch := range batches {
			if len([]rune(batch)) > 510 {
				t.Fatal("oversized batch")
			}
		}
		joined := strings.ReplaceAll(strings.Join(batches, ""), " ", "")
		if joined != strings.ReplaceAll(strings.TrimSpace(input), " ", "") {
			t.Fatal("phones lost during chunking")
		}
	}
}
func TestTrimKeepsInternalPauseAndMargins(t *testing.T) {
	samples := make([]float32, 480*12)
	for i := 480 * 4; i < 480*5; i++ {
		samples[i] = .1
	}
	for i := 480 * 8; i < 480*9; i++ {
		samples[i] = .2
	}
	got, err := trimSilence(samples)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 480*9 {
		t.Fatalf("trim length %d", len(got))
	}
	for _, bad := range [][]float32{nil, make([]float32, 100), {float32(math.NaN())}, {float32(math.Inf(1))}} {
		if _, err := trimSilence(bad); err == nil {
			t.Fatal("invalid/silent output accepted")
		}
	}
}
func testNPYHeader(header string) []byte {
	var out bytes.Buffer
	out.WriteString("\x93NUMPY\x01\x00")
	binary.Write(&out, binary.LittleEndian, uint16(len(header)))
	out.WriteString(header)
	out.Write(make([]byte, 510*256*4))
	return out.Bytes()
}
func TestVoiceNPYValidation(t *testing.T) {
	valid := testNPYHeader("{'descr': '<f4', 'fortran_order': False, 'shape': (510, 1, 256), }\n")
	v, err := parseVoiceNPY(valid)
	if err != nil || len(v) != 510*256 {
		t.Fatalf("valid array rejected: %v", err)
	}
	for _, header := range []string{"{'descr':'|O','shape': (510, 1, 256),'fortran_order': False}", "{'descr':'<f4','shape': (511, 1, 256),'fortran_order': False}", "{'descr':'<f4','shape': (510, 1, 256),'fortran_order': True}"} {
		if _, err := parseVoiceNPY(testNPYHeader(header)); err == nil {
			t.Fatal("unsafe or wrong-shaped NPY accepted")
		}
	}
	binary.LittleEndian.PutUint32(valid[len(valid)-4:], math.Float32bits(float32(math.NaN())))
	if _, err := parseVoiceNPY(valid); err == nil {
		t.Fatal("nonfinite voice accepted")
	}
}
func TestG2PAssetTamperingRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "g2p-us.onnx"), []byte("damaged"), 0600)
	if _, err := newG2P(dir, false); err == nil {
		t.Fatal("tampered native model accepted")
	}
}

func TestPythonPronunciationParityWithNativeG2P(t *testing.T) {
	library := os.Getenv("MARGIN_ORT_LIBRARY")
	if library == "" {
		t.Skip("set MARGIN_ORT_LIBRARY to run native G2P parity")
	}
	if err := acquireRuntime(library); err != nil {
		t.Fatal(err)
	}
	defer releaseRuntime()
	us, err := newPronouncer(assetsDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer us.close()
	gb, err := newPronouncer(assetsDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer gb.close()
	for _, row := range pronunciationGoldens(t) {
		p := us
		if row.British {
			p = gb
		}
		t.Run(row.Text+map[bool]string{false: " US", true: " GB"}[row.British], func(t *testing.T) {
			phones, err := p.phonemize(context.Background(), row.Text)
			if err != nil {
				t.Fatal(err)
			}
			if phones != row.Phonemes {
				t.Errorf("phoneme mismatch\n got: %s\nwant: %s", phones, row.Phonemes)
			}
		})
	}
}
func TestNativeAudioMatchesPython(t *testing.T) {
	library, models := os.Getenv("MARGIN_ORT_LIBRARY"), os.Getenv("MARGIN_TEST_MODEL_DIR")
	if library == "" || models == "" {
		t.Skip("set MARGIN_ORT_LIBRARY and MARGIN_TEST_MODEL_DIR for Kokoro audio parity")
	}
	e, err := New(models, assetsDir(), library)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	data, err := os.ReadFile("testdata/python_audio.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Voice, Text, File string
		Speed             float64
		SampleRate        int `json:"sample_rate"`
		Samples           int
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Voice, func(t *testing.T) {
			got, sr, err := e.Synthesize(context.Background(), tc.Text, tc.Voice, tc.Speed)
			if err != nil {
				t.Fatal(err)
			}
			if sr != tc.SampleRate || len(got) != tc.Samples {
				t.Fatalf("audio format changed: %dHz/%d samples, wanted %d/%d", sr, len(got), tc.SampleRate, tc.Samples)
			}
			ref, err := os.ReadFile(filepath.Join("testdata", tc.File))
			if err != nil {
				t.Fatal(err)
			}
			// Kokoro contains random noise operators, so even repeated Python
			// runs differ sample-by-sample. Duration and signal correlation
			// are stable; require a much tighter match than perceptual parity.
			var dot, gotEnergy, refEnergy, difference float64
			for i, value := range got {
				expected := math.Float32frombits(binary.LittleEndian.Uint32(ref[i*4:]))
				g, r := float64(value), float64(expected)
				dot += g * r
				gotEnergy += g * g
				refEnergy += r * r
				difference += (g - r) * (g - r)
			}
			correlation := dot / math.Sqrt(gotEnergy*refEnergy)
			relativeRMSE := math.Sqrt(difference / refEnergy)
			ratio := math.Sqrt(gotEnergy / refEnergy)
			if correlation < .98 || relativeRMSE > .2 || ratio < .9 || ratio > 1.1 {
				t.Errorf("native output drifted from Python: correlation %.6f, relative RMSE %.6f, RMS ratio %.6f", correlation, relativeRMSE, ratio)
			}
			t.Logf("%d samples at %d Hz; correlation %.6f; relative RMSE %.6f", len(got), sr, correlation, relativeRMSE)
		})
	}
}

func TestNativeCancellationDoesNotPoisonNextGeneration(t *testing.T) {
	library, models := os.Getenv("MARGIN_ORT_LIBRARY"), os.Getenv("MARGIN_TEST_MODEL_DIR")
	if library == "" || models == "" {
		t.Skip("native speech integration assets not configured")
	}
	e, err := New(models, assetsDir(), library)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	// Warm the model before imposing a short deadline, to exercise native Run
	// cancellation rather than canceling during initial model loading.
	if _, _, err = e.Synthesize(context.Background(), "Hello.", "af_heart", .95); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, _, err = e.Synthesize(ctx, strings.Repeat("The quick brown fox jumps over the lazy dog. ", 8), "af_heart", .95)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline cancellation, got %v", err)
	}
	audio, sr, err := e.Synthesize(context.Background(), "Hello again.", "af_heart", .95)
	if err != nil || sr != SampleRate || len(audio) == 0 {
		t.Fatalf("generation after cancel failed: %v", err)
	}
}

func TestStatusDoesNotWaitForInference(t *testing.T) {
	e, _ := New(t.TempDir(), t.TempDir(), "missing.so")
	e.mu.Lock() // Models load and inference run under this mutex.
	done := make(chan struct{})
	go func() { _ = e.Status(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		e.mu.Unlock()
		t.Fatal("Status blocked behind speech inference")
	}
	e.mu.Unlock()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if status := e.Status(); status.Ready || !strings.Contains(status.Message, "closed") {
		t.Fatalf("closed status: %+v", status)
	}
}
