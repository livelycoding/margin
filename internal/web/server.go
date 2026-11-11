// Package web exposes Margin's existing browser API with local-origin controls.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"margin/internal/article"
	"margin/internal/domain"
	"margin/internal/library"
)

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; media-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

var episodeRoute = regexp.MustCompile(`^/api/episodes/([0-9a-f]{32})(?:/(audio\.wav|transcript\.srt|transcript\.txt|cancel|retry|recast))?$`)
var previewRoute = regexp.MustCompile(`^/api/voices/([a-z0-9_]+)/preview\.wav$`)
var singleRange = regexp.MustCompile(`^bytes=([0-9]*)-([0-9]*)$`)
var validHost = regexp.MustCompile(`^[a-z0-9.-]+$`)

type Options struct {
	StaticDir, BindHost string
	Port                int
	PublicOrigins       []string
}
type Handler struct {
	app            *library.App
	staticDir      string
	origins, hosts map[string]bool
	Import         func(context.Context, string) (domain.Article, error)
}

func ParsePublicOrigin(value string) (string, error) {
	bad := func() (string, error) {
		return "", errors.New("Use an HTTP(S) origin only, e.g. http://reader:8765, without a path or credentials.")
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r < 32 || r == 127 || r == '\\' }) >= 0 {
		return bad()
	}
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return bad()
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(u.Hostname()))
	if err != nil || !validHost.MatchString(host) {
		return bad()
	}
	suffix := ""
	if strings.HasSuffix(u.Host, ":") {
		return bad()
	}
	if port := u.Port(); port != "" {
		number, e := strconv.Atoi(port)
		if e != nil || number < 1 || number > 65535 {
			return bad()
		}
		if !((u.Scheme == "https" && number == 443) || (u.Scheme == "http" && number == 80)) {
			suffix = ":" + strconv.Itoa(number)
		}
	}
	return u.Scheme + "://" + host + suffix, nil
}

func ParseBindHost(value string) (string, error) {
	ip := net.ParseIP(value)
	if ip == nil || ip.To4() == nil || strings.Contains(value, ":") {
		return "", errors.New("Use an IPv4 bind address, e.g. 127.0.0.1 or 0.0.0.0.")
	}
	return ip.String(), nil
}
func ParseTailnetHost(value string) (string, error) {
	ip, err := ParseBindHost(value)
	_, block, _ := net.ParseCIDR("100.64.0.0/10")
	if err != nil || !block.Contains(net.ParseIP(ip)) {
		return "", errors.New("The extra listener must use this machine's Tailscale IPv4 address (100.64.0.0/10).")
	}
	return ip, nil
}

func New(app *library.App, options Options) (*Handler, error) {
	h := &Handler{app: app, staticDir: options.StaticDir, origins: map[string]bool{}, hosts: map[string]bool{}, Import: article.Import}
	origins := []string{fmt.Sprintf("http://127.0.0.1:%d", options.Port), fmt.Sprintf("http://localhost:%d", options.Port)}
	if ip := net.ParseIP(options.BindHost); ip != nil && ip.IsLoopback() {
		origins = append(origins, fmt.Sprintf("http://%s:%d", options.BindHost, options.Port))
	}
	origins = append(origins, options.PublicOrigins...)
	for _, raw := range origins {
		origin, err := ParsePublicOrigin(raw)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(origin)
		h.origins[origin] = true
		h.hosts[u.Host] = true
	}
	return h, nil
}
func headers(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", csp)
}
func respond(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = 500
		data = []byte(`{"error":"Could not encode response."}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write(data)
	}
}
func fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	respond(w, r, status, map[string]string{"error": message})
}
func readBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return nil, errors.New("Send application/json.")
	}
	if r.ContentLength == 0 || r.ContentLength > 1000000 {
		return nil, errors.New("Request is empty or too large.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1000000)
	data, err := io.ReadAll(r.Body)
	if err != nil || len(data) == 0 {
		return nil, errors.New("Request is empty or too large.")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("Send valid UTF-8 JSON.")
	}
	var body map[string]any
	if err = json.Unmarshal(data, &body); err != nil || body == nil {
		return nil, errors.New("Expected a JSON object.")
	}
	return body, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	headers(w)
	if !h.hosts[r.Host] {
		fail(w, r, 403, "Use one of Margin's configured local or tailnet addresses.")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !h.origins[origin] {
		fail(w, r, 403, "Requests must come from a configured Margin address.")
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, r, 403, "Cross-site requests are not allowed.")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(h.app.Token)) != 1 {
		fail(w, r, 403, "Reload the app before trying again.")
		return
	}
	err := h.route(w, r)
	if err == nil {
		return
	}
	if errors.Is(err, library.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		fail(w, r, 404, "Not found.")
	} else if library.IsValidation(err) {
		fail(w, r, 400, err.Error())
	} else {
		log.Printf("Request failed: %v", err)
		fail(w, r, 500, "Something went wrong. Check the local server log and try again.")
	}
}
func (h *Handler) route(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	method := r.Method
	if method == "HEAD" {
		method = "GET"
	}
	if method == "GET" && path == "/api/config" {
		respond(w, r, 200, h.app.Config())
		return nil
	}
	if m := previewRoute.FindStringSubmatch(path); m != nil && method == "GET" {
		p, e := h.app.VoicePreview(m[1])
		if e != nil {
			return e
		}
		return serveFile(w, r, p, false)
	}
	if path == "/api/episodes" {
		switch method {
		case "GET":
			items, e := h.app.List()
			if e != nil {
				return e
			}
			summaries := make([]map[string]any, 0, len(items))
			for _, item := range items {
				data, _ := json.Marshal(item)
				var m map[string]any
				_ = json.Unmarshal(data, &m)
				delete(m, "text")
				delete(m, "cues")
				summaries = append(summaries, m)
			}
			respond(w, r, 200, map[string]any{"episodes": summaries})
			return nil
		case "POST":
			body, e := readBody(w, r)
			if e != nil {
				fail(w, r, 400, e.Error())
				return nil
			}
			item, e := h.app.Create(body)
			if e != nil {
				return e
			}
			respond(w, r, 201, item)
			return nil
		}
	}
	if method == "POST" && path == "/api/import" {
		body, e := readBody(w, r)
		if e != nil {
			fail(w, r, 400, e.Error())
			return nil
		}
		address, ok := body["url"].(string)
		if !ok {
			fail(w, r, 400, "Enter a public article URL.")
			return nil
		}
		result, e := h.Import(r.Context(), address)
		if e != nil {
			fail(w, r, 400, e.Error())
			return nil
		}
		if utf8.RuneCountInString(result.Text) > domain.MaxText {
			fail(w, r, 400, "This article is too long. Paste it in smaller parts.")
			return nil
		}
		respond(w, r, 200, result)
		return nil
	}
	if match := episodeRoute.FindStringSubmatch(path); match != nil {
		id, action := match[1], match[2]
		item, e := h.app.Get(id)
		if e != nil {
			return e
		}
		if action == "" && method == "GET" {
			respond(w, r, 200, item)
			return nil
		}
		if action == "" && method == "PATCH" {
			body, e := readBody(w, r)
			if e != nil {
				fail(w, r, 400, e.Error())
				return nil
			}
			updated, e := h.app.Rename(id, body["title"])
			if e != nil {
				return e
			}
			respond(w, r, 200, map[string]any{"episode": updated})
			return nil
		}
		if action == "" && method == "DELETE" {
			result, e := h.app.Action(id, "delete")
			if e != nil {
				return e
			}
			respond(w, r, 200, result)
			return nil
		}
		if action == "recast" && method == "POST" {
			body, e := readBody(w, r)
			if e != nil {
				fail(w, r, 400, e.Error())
				return nil
			}
			result, e := h.app.Recast(id, body["voice"])
			if e != nil {
				return e
			}
			respond(w, r, 201, map[string]any{"episode": result})
			return nil
		}
		if (action == "cancel" || action == "retry") && method == "POST" {
			_, e := readBody(w, r)
			if e != nil {
				fail(w, r, 400, e.Error())
				return nil
			}
			result, e := h.app.Action(id, action)
			if e != nil {
				return e
			}
			respond(w, r, 200, map[string]any{"episode": result})
			return nil
		}
		if (action == "audio.wav" || action == "transcript.srt" || action == "transcript.txt") && method == "GET" {
			if item.Status != "complete" {
				fail(w, r, 400, "The episode is not ready yet.")
				return nil
			}
			folder, e := h.app.Path(id)
			if e != nil {
				return e
			}
			return serveFile(w, r, filepath.Join(folder, action), action != "audio.wav")
		}
	}
	if method == "GET" {
		path = strings.TrimPrefix(path, "/static")
		switch path {
		case "/", "/index.html":
			return serveFile(w, r, filepath.Join(h.staticDir, "index.html"), false)
		case "/app.js", "/styles.css", "/favicon.svg":
			return serveFile(w, r, filepath.Join(h.staticDir, path[1:]), false)
		case "/style.css":
			return serveFile(w, r, filepath.Join(h.staticDir, "styles.css"), false)
		}
	}
	return library.ErrNotFound
}
func serveFile(w http.ResponseWriter, r *http.Request, path string, download bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return os.ErrNotExist
	}
	if requested := r.Header.Get("Range"); requested != "" {
		m := singleRange.FindStringSubmatch(requested)
		if m == nil || (m[1] == "" && strings.TrimLeft(m[2], "0") == "") {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size()))
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(416)
			return nil
		}
	}
	switch filepath.Ext(path) {
	case ".wav":
		w.Header().Set("Content-Type", "audio/wav")
	case ".srt":
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	case ".txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	if download {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	}
	http.ServeContent(idleWriter{ResponseWriter: w, controller: http.NewResponseController(w)}, r, filepath.Base(path), info.ModTime(), f)
	return nil
}

// Refresh the deadline on each chunk instead of limiting an entire large WAV
// download to the server's initial response deadline.
type idleWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
}

func (w idleWriter) Write(data []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.ResponseWriter.Write(data)
}
