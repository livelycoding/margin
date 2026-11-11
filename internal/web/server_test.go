package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"margin/internal/domain"
	"margin/internal/library"
)

type fakeEngine struct{}

func (fakeEngine) Status() domain.EngineStatus { return domain.EngineStatus{Ready: true, Name: "test"} }
func (fakeEngine) Voices() []domain.Voice {
	return []domain.Voice{{ID: "af_heart", Name: "Heart"}, {ID: "bm_george", Name: "George"}}
}
func (fakeEngine) Synthesize(context.Context, string, string, float64) ([]float32, int, error) {
	return make([]float32, 2400), 24000, nil
}
func (fakeEngine) Close() error { return nil }
func fixture(t *testing.T) (*library.App, *Handler) {
	t.Helper()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "static"), 0700)
	_ = os.WriteFile(filepath.Join(root, "static", "index.html"), []byte("Margin page"), 0600)
	app, e := library.New(library.Options{DataDir: filepath.Join(root, "data"), PreviewDir: filepath.Join(root, "static"), Engine: fakeEngine{}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = app.Close() })
	handler, e := New(app, Options{StaticDir: filepath.Join(root, "static"), BindHost: "127.0.0.1", Port: 8765, PublicOrigins: []string{"https://reader.example:8766"}})
	if e != nil {
		t.Fatal(e)
	}
	return app, handler
}
func request(h *Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", h.app.Token)
	for k, v := range headers {
		if k == "Host" {
			r.Host = v
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestOriginAndBindValidation(t *testing.T) {
	for raw, want := range map[string]string{"https://READER.example:443": "https://reader.example", "http://reader:80": "http://reader", "https://reader.example:8766": "https://reader.example:8766"} {
		got, e := ParsePublicOrigin(raw)
		if e != nil || got != want {
			t.Errorf("%s = %s, %v", raw, got, e)
		}
	}
	for _, raw := range []string{"http://user:pass@reader", "http://reader/", "http://reader?", "http://reader#", "http://reader:0", "http://reader:65536", "http://reader:", "https://reader\\@evil", "http://re ader", "http://reader\r\nX:1", "ftp://reader", "http://[::1]"} {
		if _, e := ParsePublicOrigin(raw); e == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if got, e := ParseTailnetHost("100.64.1.2"); e != nil || got != "100.64.1.2" {
		t.Fatal(got, e)
	}
	for _, host := range []string{"0.0.0.0", "127.0.0.1", "100.128.0.1", "::1", "reader.invalid"} {
		if _, e := ParseTailnetHost(host); e == nil {
			t.Fatal("accepted", host)
		}
	}
}
func TestBrowserSecurityAndConfig(t *testing.T) {
	app, h := fixture(t)
	for _, headers := range []map[string]string{{"Host": "evil.example"}, {"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}, {"Host": "evil.example", "X-Forwarded-Host": "reader.example:8766"}} {
		if w := request(h, "GET", "/api/config", "", headers); w.Code != 403 {
			t.Fatalf("%v: %d", headers, w.Code)
		}
	}
	for _, token := range []string{"", "bad"} {
		if w := request(h, "POST", "/api/episodes", `{"text":"Hello."}`, map[string]string{"X-CSRF-Token": token}); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	w := request(h, "GET", "/api/config", "", map[string]string{"Host": "reader.example:8766", "Origin": "https://reader.example:8766"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), app.Token) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if w.Header().Get(name) == "" {
			t.Fatal("missing", name)
		}
	}
	if w := request(h, "HEAD", "/api/config", "", nil); w.Body.Len() != 0 || w.Header().Get("Content-Length") == "0" {
		t.Fatal("invalid HEAD")
	}
}
func TestBodiesAndCreateRenameRecast(t *testing.T) {
	app, h := fixture(t)
	for _, body := range []string{"", "null", "[]", "1", `{"text":"x"} {}`, string([]byte{0xff}), strings.Repeat("x", 1000001)} {
		w := request(h, "POST", "/api/episodes", body, nil)
		if w.Code != 400 {
			t.Errorf("bad body length %d got %d", len(body), w.Code)
		}
	}
	if w := request(h, "POST", "/api/episodes", `{"text":"Hello"}`, map[string]string{"Content-Type": "text/plain"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/episodes", `{"text":"First sentence. Second sentence.","title":"Test reading"}`, nil)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var episode domain.Episode
	if e := json.Unmarshal(w.Body.Bytes(), &episode); e != nil {
		t.Fatal(e)
	}
	app.Start(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		episode, _ = app.Get(episode.ID)
		if episode.Status == "complete" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if episode.Status != "complete" {
		t.Fatal(episode.Status, episode.Error)
	}
	path := "/api/episodes/" + episode.ID
	w = request(h, "PATCH", path, `{"title":"Renamed"}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Renamed") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "POST", path+"/recast", `{"voice":"bm_george"}`, nil)
	if w.Code != 201 || !strings.Contains(w.Body.String(), episode.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/episodes", "", nil)
	var listed struct {
		Episodes []map[string]any `json:"episodes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Episodes) != 2 {
		t.Fatal(w.Body.String())
	}
	for _, e := range listed.Episodes {
		if _, ok := e["text"]; ok {
			t.Fatal("summary leaked full text")
		}
		if _, ok := e["cues"]; ok {
			t.Fatal("summary includes cues")
		}
	}
	full := request(h, "GET", path+"/audio.wav", "", nil)
	if full.Code != 200 || !bytes.HasPrefix(full.Body.Bytes(), []byte("RIFF")) {
		t.Fatal(full.Code)
	}
	partial := request(h, "GET", path+"/audio.wav", "", map[string]string{"Range": "bytes=44-143"})
	if partial.Code != 206 || !bytes.Equal(partial.Body.Bytes(), full.Body.Bytes()[44:144]) {
		t.Fatal("range mismatch", partial.Code)
	}
	for _, r := range []string{"bytes=999999999-", "bytes=1-0", "bytes=-0", "bytes=0-1,4-6", "bytes=-"} {
		w := request(h, "GET", path+"/audio.wav", "", map[string]string{"Range": r})
		if w.Code != 416 {
			t.Errorf("range %s: %d", r, w.Code)
		}
	}
	head := request(h, "HEAD", path+"/audio.wav", "", nil)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatal("audio HEAD")
	}
	srt := request(h, "GET", path+"/transcript.srt", "", nil)
	if srt.Code != 200 || !strings.Contains(srt.Body.String(), " --> ") || !strings.Contains(srt.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(srt.Code, srt.Body.String())
	}
}
func TestArticleBoundaryAndStaticAllowlist(t *testing.T) {
	_, h := fixture(t)
	h.Import = func(ctx context.Context, address string) (domain.Article, error) {
		return domain.Article{Title: "Article", Text: "Imported words.", SourceURL: address}, nil
	}
	w := request(h, "POST", "/api/import", `{"url":"https://example.com/article"}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Imported words.") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/../../LICENSE", "/static/../LICENSE", "/api/episodes/../audio.wav", "/.env", "/LICENSE"} {
		if w = request(h, "GET", path, "", nil); w.Code != 404 {
			t.Fatalf("%s %d", path, w.Code)
		}
	}
	w = request(h, "GET", "/", "", nil)
	b, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 || string(b) != "Margin page" {
		t.Fatal(w.Code, string(b))
	}
}
