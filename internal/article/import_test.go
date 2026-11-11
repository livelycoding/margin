package article

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUnsafeURLSyntax(t *testing.T) {
	for _, raw := range []string{
		"", "file:///etc/passwd", "ftp://example.com/a", "http://user:pass@example.com/", "http://example.com:8080/",
		"https://example.com:80/", "https://example.com:0/", "http://example.com:/", "https://example.com/\r\nHost:localhost",
		"https://example.com\\@localhost/", "http://[fe80::1%25eth0]/", "not a URL", "//example.com/a", "http://example..com", "http://-example.com", "https://example.com/\u2003x",
	} {
		if _, err := validatedURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestURLCanonicalization(t *testing.T) {
	u, err := validatedURL("https://EXAMPLE.com:443/café?a=1&b=2#section")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "https://example.com/caf%C3%A9?a=1&b=2" {
		t.Fatalf("canonical URL: %q", u)
	}
	u, err = validatedURL("https://bücher.example./?q=café")
	if err != nil || u.String() != "https://xn--bcher-kva.example/?q=caf%C3%A9" {
		t.Fatalf("international URL: %v, %v", u, err)
	}
}

func TestPrivateAndTunneledIPsBlocked(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "240.0.0.1", "192.0.2.10", "198.18.0.1", "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "64:ff9b:1::a00:1", "2002:7f00:1::", "2001::1234", "2001:db8::1", "3fff::1"} {
		if isPublicIP(netip.MustParseAddr(value)) {
			t.Errorf("accepted %s", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888", "::ffff:1.1.1.1"} {
		if !isPublicIP(netip.MustParseAddr(value)) {
			t.Errorf("blocked public %s", value)
		}
	}
}

func publicLookup(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
}

func TestMixedDNSAnswersRejectedBeforeDial(t *testing.T) {
	f := newFetcher()
	f.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	f.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dialed mixed DNS answer")
		return nil, nil
	}
	if _, _, _, err := f.fetch(context.Background(), "https://example.test/article"); !errors.Is(err, errPublicAddress) {
		t.Fatalf("got %v", err)
	}
}

func TestDNSDeadline(t *testing.T) {
	f := newFetcher()
	f.timeout = 20 * time.Millisecond
	f.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }
	start := time.Now()
	if _, _, _, err := f.fetch(context.Background(), "https://example.test/article"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("DNS was not bounded")
	}
}

func pipeFetcher(t *testing.T, responses ...string) (fetcher, *[]string) {
	t.Helper()
	f := newFetcher()
	f.timeout = time.Second
	f.lookup = publicLookup
	addresses := new([]string)
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		index := len(*addresses)
		*addresses = append(*addresses, address)
		if index >= len(responses) {
			return nil, fmt.Errorf("unexpected connection %d", index)
		}
		client, server := net.Pipe()
		go func(response string) {
			defer server.Close()
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			request.Body.Close()
			io.WriteString(server, response)
		}(responses[index])
		return client, nil
	}
	return f, addresses
}

func TestPinnedDialIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:12345")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:12345")
	f, addresses := pipeFetcher(t, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nHello")
	lookups := 0
	f.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		lookups++
		return publicLookup(ctx, host)
	}
	body, finalURL, contentType, err := f.fetch(context.Background(), "http://example.test/article")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "Hello" || contentType != "text/plain" || finalURL.Hostname() != "example.test" {
		t.Fatalf("unexpected result: %q, %v, %q", body, finalURL, contentType)
	}
	if lookups != 1 || len(*addresses) != 1 || (*addresses)[0] != "1.1.1.1:80" {
		t.Fatalf("unpinned connection: %d lookups, %#v", lookups, addresses)
	}
}

func TestRedirectRevalidatesDestination(t *testing.T) {
	for _, destination := range []string{"http://127.0.0.1/secrets", "http://100.64.0.1/article", "http://[64:ff9b::7f00:1]/"} {
		f, addresses := pipeFetcher(t, "HTTP/1.1 302 Found\r\nLocation: "+destination+"\r\nContent-Length: 0\r\n\r\n")
		if _, _, _, err := f.fetch(context.Background(), "http://example.test/article"); !errors.Is(err, errPublicAddress) {
			t.Errorf("%s: got %v", destination, err)
		}
		if len(*addresses) != 1 {
			t.Fatal("connected to blocked redirect")
		}
	}
	f, addresses := pipeFetcher(t, "HTTP/1.1 302 Found\r\nLocation: /second\r\nContent-Length: 0\r\n\r\n", "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\nOK")
	lookups := 0
	f.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		lookups++
		return publicLookup(ctx, host)
	}
	_, finalURL, _, err := f.fetch(context.Background(), "http://example.test/first")
	if err != nil || finalURL.Path != "/second" || lookups != 2 || len(*addresses) != 2 {
		t.Fatalf("relative redirect: %v, %v, %d lookups", finalURL, err, lookups)
	}
}

func TestRedirectLimit(t *testing.T) {
	responses := make([]string, maxRedirects+1)
	for i := range responses {
		responses[i] = "HTTP/1.1 302 Found\r\nLocation: /again\r\nContent-Length: 0\r\n\r\n"
	}
	f, addresses := pipeFetcher(t, responses...)
	if _, _, _, err := f.fetch(context.Background(), "http://example.test/again"); err == nil || !strings.Contains(err.Error(), "redirected too many") {
		t.Fatalf("got %v", err)
	}
	if len(*addresses) != maxRedirects+1 {
		t.Fatalf("unexpected requests: %d", len(*addresses))
	}
}

func TestTLSUsesOriginalSNIAndHostnameVerification(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.test"}, DNSNames: []string{"example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	root := x509.NewCertPool()
	root.AddCert(certificate)
	f := newFetcher()
	f.timeout = time.Second
	f.lookup = publicLookup
	f.tlsConfig = &tls.Config{RootCAs: root, MinVersion: tls.VersionTLS12}
	sni := make(chan string, 1)
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "1.1.1.1:443" {
			t.Errorf("dialed %q", address)
		}
		client, rawServer := net.Pipe()
		go func() {
			defer rawServer.Close()
			server := tls.Server(rawServer, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) { sni <- hello.ServerName; return nil, nil }})
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			request.Body.Close()
			io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\nOK")
		}()
		return client, nil
	}
	body, _, _, err := f.fetch(context.Background(), "https://example.test/article")
	if err != nil || string(body) != "OK" {
		t.Fatalf("TLS request: %q, %v", body, err)
	}
	if got := <-sni; got != "example.test" {
		t.Fatalf("SNI: %q", got)
	}
	if _, _, _, err := f.fetch(context.Background(), "https://wrong.test/article"); err == nil {
		t.Fatal("accepted wrong certificate hostname")
	}
}

func TestTricklingResponseDeadline(t *testing.T) {
	f := newFetcher()
	f.timeout = 25 * time.Millisecond
	f.lookup = publicLookup
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_, _ = http.ReadRequest(bufio.NewReader(server))
			io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\na")
			<-ctx.Done()
		}()
		return client, nil
	}
	start := time.Now()
	if _, _, _, err := f.fetch(context.Background(), "http://example.test/article"); err == nil {
		t.Fatal("accepted incomplete response")
	}
	if time.Since(start) > time.Second {
		t.Fatal("body deadline was not enforced")
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("body should not be read") }
func (panicReader) Close() error             { return nil }

func TestResponseLimitsAndCompression(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: maxArticleBytes + 1, Body: panicReader{}}
	response.Header.Set("Content-Type", "text/html")
	if _, _, err := readResponse(response); !errors.Is(err, errArticleLimit) {
		t.Fatalf("length header: %v", err)
	}
	response.ContentLength = -1
	response.Body = io.NopCloser(bytes.NewReader(make([]byte, maxArticleBytes+1)))
	if _, _, err := readResponse(response); !errors.Is(err, errArticleLimit) {
		t.Fatalf("stream length: %v", err)
	}
	response.Body = panicReader{}
	response.Header.Set("Content-Encoding", "gzip")
	if _, _, err := readResponse(response); err == nil {
		t.Fatal("accepted compressed response")
	}
	u, _ := url.Parse("https://example.com/")
	if _, err := extract([]byte{0x1f, 0x8b, 0, 0}, u, "text/html"); err == nil {
		t.Fatal("accepted disguised gzip")
	}
}

func TestArticleExtractionRetainsParagraphs(t *testing.T) {
	first := "A complete paragraph with enough readable words to be imported and spoken by the local narration tool, retaining sentence boundaries and the pacing expected by a listener. The first paragraph introduces the topic with useful context."
	second := "The second paragraph continues our story and explains how a public article can become a local audio recording. It should remain separate from the opening paragraph so that listeners hear a natural pause between the two sections."
	body := "<!doctype html><html><head><title>Public post</title></head><body><nav>Home Subscribe Sign in</nav><article><h1>Public post</h1><p>" + first + "</p><p>" + second + "</p></article><footer>Copyright</footer></body></html>"
	u, _ := url.Parse("https://example.com/post")
	result, err := extract([]byte(body), u, "text/html; charset=utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Public post" || result.SourceURL != u.String() {
		t.Fatalf("metadata: %#v", result)
	}
	if !strings.Contains(result.Text, first+"\n\n"+second) {
		t.Fatalf("paragraphs were not retained: %q", result.Text)
	}
	if strings.Contains(result.Text, "Subscribe") || strings.Contains(result.Text, "Copyright") {
		t.Fatalf("navigation leaked: %q", result.Text)
	}
	pauses := 0
	for _, segment := range SplitSegments(result.Text) {
		if segment.Pause == .45 {
			pauses++
		}
	}
	if pauses < 2 {
		t.Fatalf("paragraph pause count: %d", pauses)
	}
}

func TestPlainTextAndPaywall(t *testing.T) {
	u, _ := url.Parse("https://example.com/post")
	paragraph := "A complete paragraph with enough readable words to be imported and spoken by the local narration tool, retaining sentence boundaries and the pacing expected by a listener. "
	result, err := extract([]byte(paragraph+"\n\n"+paragraph), u, "text/plain; charset=utf-8")
	if err != nil || !strings.Contains(result.Text, "\n\n") {
		t.Fatalf("plain text: %#v, %v", result, err)
	}
	if _, err := extract([]byte("Subscribe to keep reading."), u, "text/plain"); !errors.Is(err, errTooLittleText) {
		t.Fatalf("paywall: %v", err)
	}
}

func TestHTMLComplexityBounds(t *testing.T) {
	if err := checkHTMLComplexity([]byte(strings.Repeat("<div>", 257))); err == nil {
		t.Fatal("accepted deeply nested HTML")
	}
	if err := checkHTMLComplexity([]byte(strings.Repeat("<br>", maxHTMLNodes+1))); err == nil {
		t.Fatal("accepted too many nodes")
	}
}
