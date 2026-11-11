package library

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"margin/internal/article"
	"margin/internal/domain"
)

type waveWriter struct {
	file   *os.File
	rate   int
	frames uint64
	closed bool
}

func newWave(path string) (*waveWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(make([]byte, 44)); err != nil {
		file.Close()
		return nil, err
	}
	return &waveWriter{file: file}, nil
}
func (w *waveWriter) appendSamples(samples []float32, rate int) error {
	if len(samples) == 0 {
		return errors.New("The voice engine returned empty or invalid audio.")
	}
	if rate < 1000 || rate > 384000 {
		return errors.New("The voice engine returned an invalid sample rate.")
	}
	if w.rate == 0 {
		w.rate = rate
	} else if w.rate != rate {
		return errors.New("The voice engine changed its sample rate.")
	}
	if (w.frames+uint64(len(samples)))*2 > math.MaxUint32-36 {
		return errors.New("This reading exceeds the WAV size limit. Please split it into parts.")
	}
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
			return errors.New("The voice engine returned empty or invalid audio.")
		}
		if sample > 1 {
			sample = 1
		} else if sample < -1 {
			sample = -1
		}
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(sample*32767)))
	}
	if _, err := w.file.Write(pcm); err != nil {
		return err
	}
	w.frames += uint64(len(samples))
	return nil
}
func (w *waveWriter) silence(frames int64) error {
	if frames < 0 || uint64(frames) > math.MaxUint32/2 || w.frames+uint64(frames) > (math.MaxUint32-36)/2 {
		return errors.New("This reading exceeds the WAV size limit. Please split it into parts.")
	}
	buffer := make([]byte, 8192)
	remaining := frames * 2
	for remaining > 0 {
		count := int64(len(buffer))
		if count > remaining {
			count = remaining
		}
		if _, err := w.file.Write(buffer[:count]); err != nil {
			return err
		}
		remaining -= count
	}
	w.frames += uint64(frames)
	return nil
}
func (w *waveWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.rate == 0 {
		w.rate = 24000
	}
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+uint32(w.frames*2))
	copy(header[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], uint32(w.rate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(w.rate*2))
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(w.frames*2))
	_, seekErr := w.file.Seek(0, io.SeekStart)
	var writeErr, syncErr error
	if seekErr == nil {
		_, writeErr = w.file.Write(header)
		if writeErr == nil {
			syncErr = w.file.Sync()
		}
	}
	return errors.Join(seekErr, writeErr, syncErr, w.file.Close())
}
func roundedTime(frames uint64, rate int) float64 {
	return math.RoundToEven(float64(frames)/float64(rate)*1e6) / 1e6
}
func ensureEpisodeFolder(path string) error {
	err := os.Mkdir(path, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("The reading folder is not a regular directory.")
	}
	return nil
}
func writeTemporary(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	return errors.Join(writeErr, syncErr, file.Close())
}
func (a *App) generate(ctx context.Context, item domain.Episode) (resultErr error) {
	defer func() {
		if problem := recover(); problem != nil {
			resultErr = fmt.Errorf("speech engine panic: %v", problem)
		}
	}()
	folder, err := a.store.Path(item.ID)
	if err != nil {
		return err
	}
	if err = ensureEpisodeFolder(folder); err != nil {
		return err
	}
	temporary := filepath.Join(folder, "audio.partial.wav")
	srtTemporary := filepath.Join(folder, "transcript.partial.srt")
	textTemporary := filepath.Join(folder, "transcript.partial.txt")
	// Interrupted work may leave scratch files. Never follow existing symlinks.
	for _, path := range []string{temporary, srtTemporary, textTemporary} {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		defer os.Remove(path)
	}
	segments := article.SplitSegments(item.Text)
	if len(segments) == 0 {
		return errors.New("No speakable text was found.")
	}
	// Check the exact utterances before spending time generating any audio.
	// Engines without a separate text pass retain the synthesis-only contract.
	if validator, ok := a.engine.(interface {
		ValidateText(context.Context, string, string) error
	}); ok {
		for index, segment := range segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := validator.ValidateText(ctx, segment.Text, item.Voice); err != nil {
				return fmt.Errorf("check passage %d of %d: %w", index+1, len(segments), err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	estimate := newEstimate(segments)
	output, err := newWave(temporary)
	if err != nil {
		return err
	}
	defer output.Close()
	cues := make([]domain.Cue, 0, len(segments))
	for index, segment := range segments {
		if err = ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		samples, rate, err := a.engine.Synthesize(ctx, segment.Text, item.Voice, item.Speed)
		if err != nil {
			return fmt.Errorf("passage %d of %d: %w", index+1, len(segments), err)
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		previous := output.frames
		if err = output.appendSamples(samples, rate); err != nil {
			return err
		}
		cues = append(cues, domain.Cue{Start: roundedTime(previous, rate), End: roundedTime(output.frames, rate), Text: segment.Text})
		pause := math.RoundToEven(segment.Pause * float64(rate) / item.Speed)
		if math.IsNaN(pause) || math.IsInf(pause, 0) || pause < 0 || pause > float64(math.MaxUint32/2) {
			return errors.New("Invalid narration pause.")
		}
		if err = output.silence(int64(pause)); err != nil {
			return err
		}
		remaining := estimate.completed(index, time.Since(started).Seconds())
		a.mu.Lock()
		_, err = a.store.update(item.ID, false, func(e *domain.Episode) {
			e.Progress = int(math.RoundToEven(99 * float64(index+1) / float64(len(segments))))
			if e.CancelRequested || ctx.Err() != nil {
				e.ETASeconds = nil
				e.GenerationPhase = nil
			} else {
				e.ETASeconds = remaining
				if index+1 == len(segments) {
					e.GenerationPhase = stringPointer("finalizing")
				} else {
					e.GenerationPhase = stringPointer("reading")
				}
			}
		})
		a.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err = output.Close(); err != nil {
		return err
	}
	subtitles, err := article.SRT(cues)
	if err != nil {
		return err
	}
	if err = writeTemporary(srtTemporary, []byte(subtitles)); err != nil {
		return err
	}
	if err = writeTemporary(textTemporary, []byte(item.Text)); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := a.store.Get(item.ID)
	if err != nil {
		return err
	}
	if current.CancelRequested || ctx.Err() != nil {
		return context.Canceled
	}
	for _, file := range []struct{ temporary, final string }{{srtTemporary, "transcript.srt"}, {textTemporary, "transcript.txt"}, {temporary, "audio.wav"}} {
		if err = os.Rename(file.temporary, filepath.Join(folder, file.final)); err != nil {
			return err
		}
	}
	// Flush directory entries before committing the completed record.
	directory, err := os.Open(folder)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err = errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	_, err = a.store.update(item.ID, false, func(e *domain.Episode) {
		e.Status = "complete"
		e.Progress = 100
		e.Duration = float64(output.frames) / float64(output.rate)
		e.Cues = cues
		e.Error = nil
		e.CancelRequested = false
		e.ETASeconds = nil
		e.GenerationPhase = nil
	})
	return err
}
