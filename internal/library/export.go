package library

import (
	"errors"
	"os"
)

// WriteWAV creates a new PCM16 mono WAV file using the same validated sample
// conversion as generated readings. It never overwrites an existing path.
// Callers that publish a replacement should write a temporary path then rename.
func WriteWAV(path string, samples []float32, rate int) error {
	wave, err := newWave(path)
	if err != nil {
		return err
	}
	err = wave.appendSamples(samples, rate)
	err = errors.Join(err, wave.Close())
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
