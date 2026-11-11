// Package bootstrap installs verified upstream assets without Python or pip.
package bootstrap

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const ORTVersion = "1.31.1"
const ModelBaseURL = "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.1/"

var Models = map[string]string{
	"kokoro-v1.0.onnx": "beb0d1848dee9a49da392cc3df26958d46cfa35d321edf434f52949153f0df3a",
	"voices-v1.0.bin":  "bca610b8308e8d99f32e6fe4197e7ec01679264efed0cac9140fe9c29f1fbf7d",
}
var allowedHosts = map[string]bool{"github.com": true, "release-assets.githubusercontent.com": true, "objects.githubusercontent.com": true, "github-releases.githubusercontent.com": true}

func Digest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	hash := sha256.New()
	if _, e = io.Copy(hash, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func validAssetURL(r *http.Request) bool {
	return r.URL.Scheme == "https" && allowedHosts[r.URL.Hostname()] && r.URL.User == nil && (r.URL.Port() == "" || r.URL.Port() == "443")
}
func assetClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !validAssetURL(req) {
			return errors.New("asset download redirected outside trusted HTTPS release hosts")
		}
		return nil
	}}
}
func DownloadVerified(ctx context.Context, address, target, expected string, maximum int64) error {
	if actual, e := Digest(target); e == nil && actual == expected {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return e
	}
	if !validAssetURL(req) {
		return errors.New("invalid asset URL")
	}
	req.Header.Set("User-Agent", "Margin/2.0")
	log.Printf("Downloading %s", filepath.Base(target))
	response, e := assetClient().Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("asset server returned HTTP %d", response.StatusCode)
	}
	f, e := os.CreateTemp(filepath.Dir(target), ".download-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(response.Body, maximum+1))
	if e != nil {
		return e
	}
	if n > maximum {
		return errors.New("asset download exceeded size limit")
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return fmt.Errorf("checksum mismatch for %s; download was not installed", filepath.Base(target))
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, target)
}
func EnsureModels(ctx context.Context, dir string) error {
	for _, name := range []string{"kokoro-v1.0.onnx", "voices-v1.0.bin"} {
		if e := DownloadVerified(ctx, ModelBaseURL+name, filepath.Join(dir, name), Models[name], 1024*1024*1024); e != nil {
			return e
		}
		log.Printf("Verified %s", name)
	}
	return nil
}

// VerifyPronunciation checks the seven bundled assets against pinned provenance.
func VerifyPronunciation(dir string) error {
	data, e := os.ReadFile(filepath.Join(dir, "PROVENANCE.json"))
	if e != nil {
		return e
	}
	var document map[string]json.RawMessage
	if e = json.Unmarshal(data, &document); e != nil {
		return e
	}
	var entries map[string]struct {
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
	}
	if e = json.Unmarshal(document["artifacts"], &entries); e != nil {
		return e
	}
	expected := []string{"g2p-us.onnx", "g2p-us.json", "g2p-gb.onnx", "g2p-gb.json", "lexicon-us-en.txt.gz", "lexicon-gb-en.txt.gz", "tokens.txt"}
	for _, name := range expected {
		entry, ok := entries[name]
		if !ok {
			return fmt.Errorf("missing asset provenance: %s", name)
		}
		file := filepath.Join(dir, name)
		sum, e := Digest(file)
		if e != nil || sum != entry.SHA256 {
			return fmt.Errorf("pronunciation asset missing or failed checksum: %s", name)
		}
		info, e := os.Stat(file)
		if e != nil || info.Size() != entry.Bytes {
			return fmt.Errorf("pronunciation asset size mismatch: %s", name)
		}
	}
	return nil
}

func RuntimeLibrary(dir string) string {
	return filepath.Join(dir, "lib", "libonnxruntime.so."+ORTVersion)
}
func EnsureRuntime(ctx context.Context, dir string) error {
	if runtime.GOOS != "linux" {
		return errors.New("automatic ONNX Runtime setup currently supports Linux only; supply --ort-library for another platform")
	}
	architecture := map[string]string{"amd64": "x64", "arm64": "aarch64"}[runtime.GOARCH]
	checksums := map[string]string{"x64": "c849e36a1ef3e22d4ff217b9d1ce4a3cf5b9467b975610913ae959ad5b16bdc9", "aarch64": "5317505e2da99be65f8e2775ba3c2afd6d2961fd78aba1bd8f56159f48820c78"}
	if architecture == "" {
		return errors.New("automatic ONNX Runtime setup supports Linux amd64 and arm64")
	}
	name := "onnxruntime-linux-" + architecture + "-" + ORTVersion
	archive := filepath.Join(dir, "upstream.tgz")
	if e := DownloadVerified(ctx, "https://github.com/microsoft/onnxruntime/releases/download/v"+ORTVersion+"/"+name+".tgz", archive, checksums[architecture], 64*1024*1024); e != nil {
		return e
	}
	file, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer file.Close()
	gz, e := gzip.NewReader(file)
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		entry, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if entry.Typeflag != tar.TypeReg {
			continue
		}
		rel := strings.TrimPrefix(entry.Name, name+"/")
		if rel == entry.Name || strings.HasPrefix(rel, "/") || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
			return errors.New("unexpected runtime archive path")
		}
		// Keep the native libraries and upstream licenses; headers are build-time only.
		if !strings.HasPrefix(rel, "lib/") && rel != "LICENSE" && rel != "ThirdPartyNotices.txt" && rel != "VERSION_NUMBER" && rel != "GIT_COMMIT_ID" {
			continue
		}
		if entry.Size < 0 || entry.Size > 128*1024*1024 {
			return errors.New("runtime archive member is too large")
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return e
		}
		f, e := os.CreateTemp(filepath.Dir(target), ".extract-*")
		if e != nil {
			return e
		}
		tmp := f.Name()
		_, copyErr := io.CopyN(f, tr, entry.Size)
		if copyErr == nil {
			copyErr = f.Chmod(0644)
		}
		if copyErr == nil {
			copyErr = f.Sync()
		}
		closeErr := f.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil {
			copyErr = os.Rename(tmp, target)
		}
		_ = os.Remove(tmp)
		if copyErr != nil {
			return copyErr
		}
		if target == RuntimeLibrary(dir) {
			found = true
		}
	}
	if !found {
		return errors.New("runtime archive did not contain the expected library")
	}
	return nil
}
