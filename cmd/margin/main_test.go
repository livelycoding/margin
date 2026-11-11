package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestHealthRequiresReadyEngine(t *testing.T) {
	for _, body := range []string{`{"engine":{"ready":true}}`, `{"engine":{"ready":false}}`, `null`, `[]`, `{"engine":"wrong"}`, `bad JSON`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/config" {
					t.Error(r.URL.Path)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			e := health(context.Background(), server.URL)
			if (e == nil) != (body == `{"engine":{"ready":true}}`) {
				t.Fatal(e)
			}
		})
	}
}
func TestLibraryProcessLockBeforeDatabaseOpen(t *testing.T) {
	root := t.TempDir()
	lock, e := os.OpenFile(filepath.Join(root, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		t.Fatal(e)
	}
	e = serve([]string{"--data-dir", root, "--port", "0"})
	if e == nil || !strings.Contains(e.Error(), "already running") {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(root, "library.sqlite3")); !os.IsNotExist(e) {
		t.Fatal("second instance touched database", e)
	}
}
func TestInvalidListenerConfigurationFailsBeforeLibrary(t *testing.T) {
	for _, args := range [][]string{{"--host", "localhost"}, {"--port", "-1"}, {"--port", "65536"}, {"--host", "0.0.0.0", "--tailnet-host", "100.64.1.2"}, {"--public-origin", "https://reader/path"}} {
		if e := serve(args); e == nil {
			t.Fatal(args)
		}
	}
}
