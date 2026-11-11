package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"margin/internal/library"
	"margin/internal/speech"
)

func TestVoicePreviewWAVValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.wav")
	if err := library.WriteWAV(path, []float32{-.5, 0, .5, .25}, speech.SampleRate); err != nil {
		t.Fatal(err)
	}
	if duration, valid := validPreviewDuration(path); !valid || duration != 4.0/speech.SampleRate {
		t.Fatalf("valid file rejected: %f, %t", duration, valid)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":       func(b []byte) []byte { return b[:len(b)-1] },
		"silent":          func(b []byte) []byte { clear(b[44:]); return b },
		"wrong rate":      func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:28], 22050); return b },
		"stereo":          func(b []byte) []byte { binary.LittleEndian.PutUint16(b[22:24], 2); return b },
		"float":           func(b []byte) []byte { binary.LittleEndian.PutUint16(b[20:22], 3); return b },
		"wrong data size": func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:44], 2); return b },
		"wrong RIFF size": func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 100); return b },
	} {
		t.Run(name, func(t *testing.T) {
			changed := mutate(append([]byte(nil), original...))
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, valid := validPreviewDuration(path); valid {
				t.Fatal("accepted corrupt preview")
			}
		})
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.wav")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, valid := validPreviewDuration(link); valid {
		t.Fatal("followed symlink in cache")
	}
}

func TestVoicePreviewAtomicPublishAndInvalidAudio(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "voice.wav")
	if _, err := publishPreview(dir, destination, []float32{.25, -.25}, speech.SampleRate); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, samples := range [][]float32{nil, {0, 0}, {float32(math.NaN())}, {float32(math.Inf(1))}} {
		if _, err := publishPreview(dir, destination, samples, speech.SampleRate); err == nil {
			t.Fatal("published invalid audio")
		}
		current, err := os.ReadFile(destination)
		if err != nil || string(current) != string(original) {
			t.Fatal("invalid generation replaced good cache")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v, %v", entries, err)
	}
	if err := library.WriteWAV(destination, []float32{.5}, speech.SampleRate); err == nil {
		t.Fatal("WriteWAV overwrote existing path")
	}
}

func TestVoicePreviewSignatureTracksEngineAssetsAndModels(t *testing.T) {
	root := t.TempDir()
	paths := []string{"internal/speech/engine.go", "internal/speech/pronunciation.go", "assets/pronunciation/tokens.txt", "models/kokoro-v1.0.onnx", "models/voices-v1.0.bin", "runtime/library.so", "go.mod"}
	for _, relative := range paths {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(relative), 0600); err != nil {
			t.Fatal(err)
		}
	}
	models, runtime := filepath.Join(root, "models"), filepath.Join(root, "runtime/library.so")
	metadata, original, err := previewSignature(root, models, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.FormatVersion != 3 || metadata.Text != previewText || metadata.Speed != .95 || len(metadata.Models) != 2 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if _, again, err := previewSignature(root, models, runtime); err != nil || again != original {
		t.Fatal("signature is not deterministic", err)
	}
	for _, relative := range paths {
		t.Run(relative, func(t *testing.T) {
			path := filepath.Join(root, relative)
			if err := os.WriteFile(path, []byte(relative+" changed"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, changed, err := previewSignature(root, models, runtime); err != nil || changed == original {
				t.Fatal("change did not invalidate preview cache", err)
			}
			if err := os.WriteFile(path, []byte(relative), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := os.RemoveAll(filepath.Join(root, "internal")); err != nil {
		t.Fatal(err)
	}
	metadata, _, err = previewSignature(root, models, runtime)
	if err != nil || metadata.EngineSources["compiled-application"] == "" {
		t.Fatal("signature requires source files in a runtime image", err)
	}
}
