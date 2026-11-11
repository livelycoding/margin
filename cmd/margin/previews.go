package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"margin/internal/bootstrap"
	"margin/internal/library"
	"margin/internal/speech"
)

const previewText = "Take a moment to pause. A good story sounds different when you hear it aloud."
const previewSpeed = .95
const previewFormatVersion = 3

type previewMetadata struct {
	Text                string            `json:"text"`
	Speed               float64           `json:"speed"`
	SampleRate          int               `json:"sample_rate"`
	FormatVersion       int               `json:"format_version"`
	Model               string            `json:"model"`
	VoiceModel          string            `json:"voice_model"`
	EngineVersion       string            `json:"engine_version"`
	ApplicationVersion  string            `json:"application_version"`
	GoVersion           string            `json:"go_version"`
	Architecture        string            `json:"architecture"`
	EngineSources       map[string]string `json:"engine_sources"`
	PronunciationAssets map[string]string `json:"pronunciation_assets"`
	Models              map[string]string `json:"models"`
	RuntimeSHA256       string            `json:"runtime_sha256"`
}

type previewVoice struct {
	Duration float64 `json:"duration"`
	Filename string  `json:"filename"`
}

type previewManifest struct {
	Text      string                  `json:"text"`
	Speed     float64                 `json:"speed"`
	Signature string                  `json:"signature"`
	Engine    previewMetadata         `json:"engine"`
	Voices    map[string]previewVoice `json:"voices"`
}

func previews(args []string) error {
	flags := flag.NewFlagSet("previews", flag.ContinueOnError)
	root := flags.String("app-dir", rootDir(), "Folder containing static/ and assets/")
	models := flags.String("model-dir", "", "Model folder (default app-dir/models)")
	ortLibrary := flags.String("ort-library", os.Getenv("MARGIN_ORT_LIBRARY"), "Path to native ONNX Runtime library")
	force := flags.Bool("force", false, "Regenerate all voice previews")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *models == "" {
		*models = filepath.Join(*root, "models")
	}
	if *ortLibrary == "" {
		*ortLibrary = bootstrap.RuntimeLibrary(filepath.Join(*root, "runtime", "onnxruntime"))
	}
	assets := filepath.Join(*root, "assets", "pronunciation")
	if err := bootstrap.VerifyPronunciation(assets); err != nil {
		return err
	}
	engine, err := speech.New(*models, assets, *ortLibrary)
	if err != nil {
		return err
	}
	defer engine.Close()
	if status := engine.Status(); !status.Ready {
		return errors.New(status.Message)
	}
	metadata, signature, err := previewSignature(*root, *models, *ortLibrary)
	if err != nil {
		return err
	}
	output := filepath.Join(*root, "static", "voice-previews")
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	// Lock the directory itself: parallel generators cannot publish a manifest
	// describing a mixture of different engine versions. No lock file is shipped.
	lock, err := os.Open(output)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("voice previews are already being generated")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	manifestPath := filepath.Join(output, "manifest.json")
	previous := previewManifest{}
	if encoded, err := os.ReadFile(manifestPath); err == nil {
		if json.Unmarshal(encoded, &previous) != nil {
			previous = previewManifest{}
		}
	}
	cacheMatches := previous.Signature == signature
	manifest := previewManifest{Text: previewText, Speed: previewSpeed, Signature: signature, Engine: metadata, Voices: make(map[string]previewVoice)}
	ctx, cancel := signalContext()
	defer cancel()
	for _, voice := range engine.Voices() {
		if err := ctx.Err(); err != nil {
			return err
		}
		filename := voice.ID + ".wav"
		destination := filepath.Join(output, filename)
		duration, cached := 0.0, false
		if cacheMatches && !*force {
			duration, cached = validPreviewDuration(destination)
		}
		if !cached {
			samples, rate, err := engine.Synthesize(ctx, previewText, voice.ID, previewSpeed)
			if err != nil {
				return fmt.Errorf("generate %s preview: %w", voice.ID, err)
			}
			if rate != speech.SampleRate {
				return fmt.Errorf("invalid audio sample rate for %s", voice.ID)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			duration, err = publishPreview(output, destination, samples, rate)
			if err != nil {
				return fmt.Errorf("save %s preview: %w", voice.ID, err)
			}
		}
		manifest.Voices[voice.ID] = previewVoice{Duration: duration, Filename: filename}
		status := "generated"
		if cached {
			status = "cached"
		}
		fmt.Printf("%s: %.2fs (%s)\n", voice.ID, duration, status)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := publishPreviewManifest(output, manifestPath, append(encoded, '\n')); err != nil {
		return err
	}
	return lock.Sync()
}

func previewSignature(root, modelDir, ortLibrary string) (previewMetadata, string, error) {
	metadata := previewMetadata{
		Text: previewText, Speed: previewSpeed, SampleRate: speech.SampleRate, FormatVersion: previewFormatVersion,
		Model: "kokoro-v1.0.onnx", VoiceModel: "voices-v1.0.bin", EngineVersion: bootstrap.ORTVersion,
		ApplicationVersion: version, GoVersion: runtime.Version(), Architecture: runtime.GOOS + "/" + runtime.GOARCH,
		EngineSources: make(map[string]string), PronunciationAssets: make(map[string]string), Models: make(map[string]string),
	}
	sources, err := filepath.Glob(filepath.Join(root, "internal", "speech", "*.go"))
	if err != nil {
		return metadata, "", err
	}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		digest, err := bootstrap.Digest(source)
		if err != nil {
			return metadata, "", err
		}
		relative, err := filepath.Rel(root, source)
		if err != nil {
			return metadata, "", err
		}
		metadata.EngineSources[filepath.ToSlash(relative)] = digest
	}
	// Runtime images contain the compiled application, not its Go sources.
	// Always hash the executable, including when optional sources are present,
	// so rebuilding with a changed engine or compiler invalidates its cache.
	executable, err := os.Executable()
	if err != nil {
		return metadata, "", err
	}
	digest, err := bootstrap.Digest(executable)
	if err != nil {
		return metadata, "", err
	}
	metadata.EngineSources["compiled-application"] = digest
	for _, relative := range []string{"go.mod", "go.sum", "internal/library/audio.go", "internal/library/export.go", "cmd/margin/previews.go"} {
		digest, err := bootstrap.Digest(filepath.Join(root, relative))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return metadata, "", err
		}
		metadata.EngineSources[relative] = digest
	}
	assets := filepath.Join(root, "assets", "pronunciation")
	entries, err := os.ReadDir(assets)
	if err != nil {
		return metadata, "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		digest, err := bootstrap.Digest(filepath.Join(assets, entry.Name()))
		if err != nil {
			return metadata, "", err
		}
		metadata.PronunciationAssets[entry.Name()] = digest
	}
	for _, name := range []string{metadata.Model, metadata.VoiceModel} {
		digest, err := bootstrap.Digest(filepath.Join(modelDir, name))
		if err != nil {
			return metadata, "", err
		}
		metadata.Models[name] = digest
	}
	metadata.RuntimeSHA256, err = bootstrap.Digest(ortLibrary)
	if err != nil {
		return metadata, "", fmt.Errorf("hash native runtime; provide --ort-library with a file path: %w", err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return metadata, "", err
	}
	signatureDigest := sha256.Sum256(encoded)
	return metadata, hex.EncodeToString(signatureDigest[:]), nil
}

func validPreviewDuration(path string) (float64, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 44 || info.Size() > 10*1024*1024 {
		return 0, false
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	header := make([]byte, 44)
	if _, err := io.ReadFull(file, header); err != nil {
		return 0, false
	}
	if string(header[0:4]) != "RIFF" || string(header[8:16]) != "WAVEfmt " || string(header[36:40]) != "data" {
		return 0, false
	}
	if binary.LittleEndian.Uint32(header[16:20]) != 16 || binary.LittleEndian.Uint16(header[20:22]) != 1 || binary.LittleEndian.Uint16(header[22:24]) != 1 || binary.LittleEndian.Uint32(header[24:28]) != speech.SampleRate || binary.LittleEndian.Uint32(header[28:32]) != speech.SampleRate*2 || binary.LittleEndian.Uint16(header[32:34]) != 2 || binary.LittleEndian.Uint16(header[34:36]) != 16 {
		return 0, false
	}
	dataLength := int64(binary.LittleEndian.Uint32(header[40:44]))
	if dataLength == 0 || dataLength%2 != 0 || dataLength+44 != info.Size() || int64(binary.LittleEndian.Uint32(header[4:8]))+8 != info.Size() {
		return 0, false
	}
	buffer := make([]byte, 32768)
	remaining, audible := dataLength, false
	for remaining > 0 {
		count := min(remaining, int64(len(buffer)))
		if _, err := io.ReadFull(file, buffer[:count]); err != nil {
			return 0, false
		}
		for _, b := range buffer[:count] {
			if b != 0 {
				audible = true
				break
			}
		}
		remaining -= count
	}
	return float64(dataLength/2) / speech.SampleRate, audible
}

func publishPreview(output, destination string, samples []float32, rate int) (float64, error) {
	// A private staging directory gives WriteWAV an unused O_EXCL path without
	// a create/delete race. Rename publishes the complete verified WAV atomically.
	directory, err := os.MkdirTemp(output, ".preview-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(directory)
	temporary := filepath.Join(directory, "audio.wav")
	if err := library.WriteWAV(temporary, samples, rate); err != nil {
		return 0, err
	}
	duration, valid := validPreviewDuration(temporary)
	if !valid {
		return 0, errors.New("generated preview was empty, silent, or invalid")
	}
	if err := os.Chmod(temporary, 0644); err != nil {
		return 0, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return 0, err
	}
	return duration, nil
}

func publishPreviewManifest(output, destination string, encoded []byte) error {
	file, err := os.CreateTemp(output, ".preview-manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}
