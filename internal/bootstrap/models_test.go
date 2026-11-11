package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheRequiresExactDigest(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "model")
	body := []byte("verified model bytes")
	sum := sha256.Sum256(body)
	expected := hex.EncodeToString(sum[:])
	if e := os.WriteFile(file, body, 0600); e != nil {
		t.Fatal(e)
	}
	if e := DownloadVerified(context.Background(), "not a URL", file, expected, 100); e != nil {
		t.Fatal("valid cache should need no network", e)
	}
	_ = os.WriteFile(file, []byte("changed"), 0600)
	if e := DownloadVerified(context.Background(), "not a URL", file, expected, 100); e == nil {
		t.Fatal("bad cache was trusted")
	}
}
func TestReleaseURLRestrictions(t *testing.T) {
	for _, address := range []string{"http://github.com/model", "https://evil.example/model", "https://user:pass@github.com/model", "https://github.com:8080/model"} {
		r, e := http.NewRequest("GET", address, nil)
		if e == nil && validAssetURL(r) {
			t.Fatal("accepted", address)
		}
	}
	for _, host := range []string{"github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"} {
		r, _ := http.NewRequest("GET", "https://"+host+"/model", nil)
		if !validAssetURL(r) {
			t.Fatal(host)
		}
	}
}
func TestPronunciationVerificationChecksEveryAsset(t *testing.T) {
	dir := t.TempDir()
	names := []string{"g2p-us.onnx", "g2p-us.json", "g2p-gb.onnx", "g2p-gb.json", "lexicon-us-en.txt.gz", "lexicon-gb-en.txt.gz", "tokens.txt"}
	assets := map[string]any{}
	for _, name := range names {
		body := []byte(name)
		sum := sha256.Sum256(body)
		assets[name] = map[string]any{"bytes": len(body), "sha256": hex.EncodeToString(sum[:])}
		_ = os.WriteFile(filepath.Join(dir, name), body, 0600)
	}
	manifest, _ := json.Marshal(map[string]any{"artifacts": assets})
	_ = os.WriteFile(filepath.Join(dir, "PROVENANCE.json"), manifest, 0600)
	if e := VerifyPronunciation(dir); e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte("changed"), 0600)
	if e := VerifyPronunciation(dir); e == nil {
		t.Fatal("corrupt vocabulary accepted")
	}
}
