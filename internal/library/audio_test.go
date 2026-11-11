package library

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPCMClippingAndExactHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.wav")
	w, err := newWave(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.appendSamples([]float32{-2, -1, -.5, 0, .5, 1, 2}, 24000); err != nil {
		t.Fatal(err)
	}
	if err = w.silence(3); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var samples []int16
	for i := 44; i < len(raw); i += 2 {
		samples = append(samples, int16(binary.LittleEndian.Uint16(raw[i:])))
	}
	want := []int16{-32767, -32767, -16383, 0, 16383, 32767, 32767, 0, 0, 0}
	if !reflect.DeepEqual(samples, want) {
		t.Fatalf("PCM = %v", samples)
	}
	if binary.LittleEndian.Uint32(raw[4:]) != uint32(len(raw)-8) || binary.LittleEndian.Uint32(raw[40:]) != 20 {
		t.Fatalf("incorrect WAV lengths")
	}
}
func TestInvalidAudioRejected(t *testing.T) {
	for _, samples := range [][]float32{nil, {float32(math.NaN())}, {float32(math.Inf(1))}} {
		w, err := newWave(filepath.Join(t.TempDir(), "bad.wav"))
		if err != nil {
			t.Fatal(err)
		}
		if w.appendSamples(samples, 24000) == nil {
			t.Fatal("invalid audio accepted")
		}
		w.Close()
	}
	w, err := newWave(filepath.Join(t.TempDir(), "rate.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.appendSamples([]float32{0}, 0) == nil {
		t.Fatal("zero sample rate accepted")
	}
	if err = w.appendSamples([]float32{0}, 24000); err != nil {
		t.Fatal(err)
	}
	if w.appendSamples([]float32{0}, 22050) == nil {
		t.Fatal("sample rate change accepted")
	}
}
