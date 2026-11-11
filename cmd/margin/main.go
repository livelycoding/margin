// Margin is a local article narration server. Its runtime does not require Python.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"margin/internal/bootstrap"
	"margin/internal/library"
	"margin/internal/speech"
	"margin/internal/web"
)

var version = "2.0.0-go"

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(s string) error { *r = append(*r, s); return nil }
func rootDir() string {
	if env := os.Getenv("MARGIN_APP_DIR"); env != "" {
		return env
	}
	exe, e := os.Executable()
	if e == nil {
		dir := filepath.Dir(exe)
		if filepath.Base(dir) == "bin" {
			dir = filepath.Dir(dir)
		}
		if _, e = os.Stat(filepath.Join(dir, "static", "index.html")); e == nil {
			return dir
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}
func main() {
	syscall.Umask(0077)
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "Margin:", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(args)
	case "setup":
		return setup(args)
	case "previews":
		return previews(args)
	case "healthcheck":
		return healthCommand(args)
	case "launch":
		return launch(args)
	case "version":
		fmt.Println("Margin", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q; use serve, setup, previews, healthcheck, launch, or version", command)
	}
}
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func setup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	root := flags.String("app-dir", rootDir(), "Folder containing static/ and assets/")
	models := flags.String("model-dir", "", "Model cache directory")
	runtimeOnly := flags.Bool("runtime-only", false, "Install native runtime without downloading speech weights")
	modelsOnly := flags.Bool("models-only", false, "Only verify/download model weights")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if *models == "" {
		*models = filepath.Join(*root, "models")
	}
	ctx, cancel := signalContext()
	defer cancel()
	if !*modelsOnly {
		if e := bootstrap.VerifyPronunciation(filepath.Join(*root, "assets", "pronunciation")); e != nil {
			return e
		}
		if e := bootstrap.EnsureRuntime(ctx, filepath.Join(*root, "runtime", "onnxruntime")); e != nil {
			return e
		}
	}
	if !*runtimeOnly {
		if e := bootstrap.EnsureModels(ctx, *models); e != nil {
			return e
		}
	}
	fmt.Println("Margin assets are ready.")
	return nil
}
func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	root := flags.String("app-dir", rootDir(), "Folder containing static/ and assets/")
	host := flags.String("host", "127.0.0.1", "IPv4 bind address; use 0.0.0.0 in a container")
	port := flags.Int("port", 8765, "HTTP port")
	tailnet := flags.String("tailnet-host", "", "Optional second listener on a Tailscale IPv4 address")
	dataDir := flags.String("data-dir", "", "Library folder (default app-dir/data)")
	modelDir := flags.String("model-dir", "", "Model folder (default app-dir/models)")
	ortLibrary := flags.String("ort-library", os.Getenv("MARGIN_ORT_LIBRARY"), "Path to native ONNX Runtime library")
	noDesktop := flags.Bool("no-desktop-notifications", false, "Disable host desktop notifications")
	download := flags.Bool("download-models", false, "Verify/download model cache before starting")
	var origins repeated
	for _, raw := range strings.FieldsFunc(os.Getenv("MARGIN_PUBLIC_ORIGINS"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		origins = append(origins, raw)
	}
	flags.Var(&origins, "public-origin", "Allowed browser origin (repeat for aliases)")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var e error
	*host, e = web.ParseBindHost(*host)
	if e != nil {
		return e
	}
	if *port < 0 || *port > 65535 {
		return errors.New("port must be between 0 and 65535")
	}
	if *tailnet != "" {
		*tailnet, e = web.ParseTailnetHost(*tailnet)
		if e != nil {
			return e
		}
		if !net.ParseIP(*host).IsLoopback() {
			return errors.New("--tailnet-host requires a loopback --host")
		}
	}
	for i, raw := range origins {
		origins[i], e = web.ParsePublicOrigin(raw)
		if e != nil {
			return e
		}
	}
	if *dataDir == "" {
		*dataDir = filepath.Join(*root, "data")
	}
	if *modelDir == "" {
		*modelDir = filepath.Join(*root, "models")
	}
	if *ortLibrary == "" {
		*ortLibrary = bootstrap.RuntimeLibrary(filepath.Join(*root, "runtime", "onnxruntime"))
	}
	// Lock before opening SQLite, so two processes cannot generate into one library.
	if e = os.MkdirAll(*dataDir, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(*dataDir, "server.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("Margin is already running with this library. Stop that process first.")
	}
	ctx, cancel := signalContext()
	defer cancel()
	if *download {
		if e = bootstrap.EnsureModels(ctx, *modelDir); e != nil {
			return e
		}
	}
	if e = bootstrap.VerifyPronunciation(filepath.Join(*root, "assets", "pronunciation")); e != nil {
		return e
	}
	engine, e := speech.New(*modelDir, filepath.Join(*root, "assets", "pronunciation"), *ortLibrary)
	if e != nil {
		return e
	}
	app, e := library.New(library.Options{DataDir: *dataDir, PreviewDir: filepath.Join(*root, "static", "voice-previews"), DesktopNotifications: !*noDesktop, Engine: engine})
	if e != nil {
		_ = engine.Close()
		return e
	}
	defer app.Close()
	listener, e := net.Listen("tcp4", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if e != nil {
		return e
	}
	defer listener.Close()
	actualPort := listener.Addr().(*net.TCPAddr).Port
	listeners := []net.Listener{listener}
	if *tailnet != "" {
		extra, err := net.Listen("tcp4", net.JoinHostPort(*tailnet, strconv.Itoa(actualPort)))
		if err != nil {
			return err
		}
		defer extra.Close()
		listeners = append(listeners, extra)
		origins = append(origins, fmt.Sprintf("http://%s:%d", *tailnet, actualPort))
	}
	handler, e := web.New(app, web.Options{StaticDir: filepath.Join(*root, "static"), BindHost: *host, Port: actualPort, PublicOrigins: origins})
	if e != nil {
		return e
	}
	localHost := *host
	if localHost == "0.0.0.0" {
		localHost = "127.0.0.1"
	}
	address := fmt.Sprintf("http://%s:%d", localHost, actualPort)
	if e = lock.Truncate(0); e != nil {
		return e
	}
	if _, e = lock.WriteAt([]byte(address), 0); e != nil {
		return e
	}
	app.Start(ctx)
	servers := make([]*http.Server, 0, len(listeners))
	failure := make(chan error, len(listeners))
	for _, l := range listeners {
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 * 1024}
		servers = append(servers, server)
		go func(s *http.Server, ln net.Listener) { failure <- s.Serve(ln) }(server, l)
	}
	fmt.Printf("Margin is ready at %s\n", address)
	for _, origin := range origins {
		fmt.Println("Margin also accepts", origin)
	}
	select {
	case <-ctx.Done():
	case e = <-failure:
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		cancel()
	}
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	for _, server := range servers {
		_ = server.Shutdown(shutdown)
	}
	return e
}
func health(ctx context.Context, address string) error {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	request, e := http.NewRequestWithContext(ctx, "GET", address+"/api/config", nil)
	if e != nil {
		return e
	}
	response, e := client.Do(request)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var status struct {
		Engine struct {
			Ready bool `json:"ready"`
		} `json:"engine"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&status); e != nil {
		return e
	}
	if !status.Engine.Ready {
		return errors.New("the local speech engine is not ready")
	}
	return nil
}
func healthCommand(args []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	address := flags.String("url", "http://127.0.0.1:8765", "Margin base URL")
	if e := flags.Parse(args); e != nil {
		return e
	}
	return health(context.Background(), strings.TrimRight(*address, "/"))
}
func launch(args []string) error {
	flags := flag.NewFlagSet("launch", flag.ContinueOnError)
	address := flags.String("url", "http://127.0.0.1:8765", "Browser URL")
	service := flags.String("service", "margin.service", "User service to start")
	checkOnly := flags.Bool("check", false, "Start and verify without opening a browser")
	if e := flags.Parse(args); e != nil {
		return e
	}
	normalized, e := web.ParsePublicOrigin(*address)
	if e != nil {
		return e
	}
	*address = normalized
	if e = exec.Command("systemctl", "--user", "start", *service).Run(); e != nil {
		return fmt.Errorf("start user service: %w", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if e = health(ctx, *address); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Margin did not become ready: %w", e)
		case <-ticker.C:
		}
	}
	if *checkOnly {
		fmt.Println("Margin is running and ready at", *address)
		return nil
	}
	cmd := exec.Command("xdg-open", *address)
	if e = cmd.Start(); e != nil {
		return e
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
