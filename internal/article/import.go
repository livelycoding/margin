package article

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	trafilatura "github.com/markusmobius/go-trafilatura/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/idna"
	"margin/internal/domain"
)

const (
	maxArticleBytes = 5 * 1024 * 1024
	fetchTimeout    = 20 * time.Second
	maxRedirects    = 5
	maxHTMLNodes    = 25000
)

var errPublicAddress = errors.New("article URLs must resolve only to public internet addresses; local and private addresses are blocked")
var errArticleLimit = errors.New("this page exceeds the 5 MB article import limit")
var errTooLittleText = errors.New("too little readable text was available; this may be a paywall, login page, or preview; paste the full text you can access")

// Import extracts a public article without cookies, proxy credentials, remote
// subresources, or access to local/private networks. Its deadline includes DNS,
// every redirect, TLS handshakes, headers, and the complete response body.
func Import(ctx context.Context, rawURL string) (domain.Article, error) {
	f := newFetcher()
	body, finalURL, contentType, err := f.fetch(ctx, strings.TrimSpace(rawURL))
	if err != nil {
		return domain.Article{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Article{}, err
	}
	return extract(body, finalURL, contentType)
}

type fetcher struct {
	lookup  func(context.Context, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
	timeout time.Duration
	// Production leaves this nil: system roots and normal hostname verification.
	// Tests supply a private CA to verify SNI without exposing any network port.
	tlsConfig *tls.Config
}

func newFetcher() fetcher {
	resolver := &net.Resolver{PreferGo: true}
	dialer := &net.Dialer{Timeout: fetchTimeout, KeepAlive: -1}
	return fetcher{
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return resolver.LookupNetIP(ctx, "ip", host)
		},
		dial:    dialer.DialContext,
		timeout: fetchTimeout,
	}
}

func validatedURL(raw string) (*url.URL, error) {
	invalid := errors.New("enter a valid public HTTP or HTTPS article URL")
	if raw == "" || len(raw) > 8192 || !utf8.ValidString(raw) {
		return nil, invalid
	}
	for _, c := range raw {
		if unicode.IsSpace(c) || unicode.IsControl(c) || c == '\\' {
			return nil, errors.New("article URLs cannot contain whitespace, control characters, or backslashes")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return nil, invalid
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, invalid
	}
	if u.User != nil {
		return nil, errors.New("URLs with usernames or passwords are not supported")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") {
		return nil, invalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || len(host) > 253 || strings.Contains(host, ":") {
			return nil, invalid
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, invalid
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return nil, invalid
				}
			}
		}
	}
	port := u.Port()
	want := "80"
	if u.Scheme == "https" {
		want = "443"
	}
	if port != "" && port != want {
		return nil, errors.New("only the standard HTTP (80) and HTTPS (443) ports are supported")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, invalid
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if u.Path == "" {
		u.Path = "/"
	}
	// Go escapes Unicode in paths; preserve query separators and escapes while
	// percent-encoding non-ASCII bytes, matching the original importer.
	var query strings.Builder
	for i := 0; i < len(u.RawQuery); i++ {
		c := u.RawQuery[i]
		if c >= 0x80 {
			fmt.Fprintf(&query, "%%%02X", c)
		} else {
			query.WriteByte(c)
		}
	}
	u.RawQuery = query.String()
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

var blockedNetworks = func() []netip.Prefix {
	var networks []netip.Prefix
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
	} {
		networks = append(networks, netip.MustParsePrefix(cidr))
	}
	return networks
}()

func isPublicIP(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	// Only the allocated global IPv6 unicast range is accepted, excluding
	// translation/tunneling ranges that can conceal private IPv4 destinations.
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, blocked := range blockedNetworks {
		if blocked.Contains(address) {
			return false
		}
	}
	return true
}

func (f fetcher) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	var addresses []netip.Addr
	if address, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{address}
	} else {
		var err error
		addresses, err = f.lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("could not resolve the article hostname: %w", err)
		}
	}
	if len(addresses) == 0 {
		return nil, errPublicAddress
	}
	for _, address := range addresses {
		if !isPublicIP(address) {
			return nil, errPublicAddress
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return addresses, nil
}

func (f fetcher) fetch(ctx context.Context, rawURL string) ([]byte, *url.URL, string, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	for redirect := 0; redirect <= maxRedirects; redirect++ {
		u, err := validatedURL(rawURL)
		if err != nil {
			return nil, nil, "", err
		}
		addresses, err := f.resolve(ctx, u.Hostname())
		if err != nil {
			return nil, nil, "", err
		}
		response, closeTransport, err := f.request(ctx, u, addresses)
		if err != nil {
			closeTransport()
			return nil, nil, "", fmt.Errorf("could not connect securely to the article server; try pasting the text: %w", err)
		}
		closeResponse := func() { response.Body.Close(); closeTransport() }
		if response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308 {
			location := response.Header.Get("Location")
			closeResponse()
			if location == "" || redirect == maxRedirects {
				return nil, nil, "", errors.New("the article URL redirected too many times or omitted its destination")
			}
			// Validate raw Location before parsing, so whitespace/control syntax is
			// not normalized away when resolving relative redirects.
			for _, c := range location {
				if unicode.IsSpace(c) || unicode.IsControl(c) || c == '\\' {
					return nil, nil, "", errors.New("the article server returned an invalid redirect")
				}
			}
			relative, err := url.Parse(location)
			if err != nil {
				return nil, nil, "", errors.New("the article server returned an invalid redirect")
			}
			rawURL = u.ResolveReference(relative).String()
			continue
		}
		body, contentType, err := readResponse(response)
		closeResponse()
		if err != nil {
			return nil, nil, "", err
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, "", err
		}
		return body, u, contentType, nil
	}
	return nil, nil, "", errors.New("too many article redirects")
}

func (f fetcher) request(ctx context.Context, u *url.URL, addresses []netip.Addr) (*http.Response, func(), error) {
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if f.tlsConfig != nil {
		tlsConfig = f.tlsConfig.Clone()
	}
	tlsConfig.ServerName = u.Hostname()
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, DisableKeepAlives: true,
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: f.timeout,
		ResponseHeaderTimeout: f.timeout, MaxResponseHeaderBytes: 64 * 1024,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			// Ignore the hostname supplied by Transport: only connect to the
			// already-validated address set, never performing another lookup.
			var lastErr error
			for _, ip := range addresses {
				conn, err := f.dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
				if ctx.Err() != nil {
					break
				}
			}
			return nil, lastErr
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, func() {}, err
	}
	request.Header.Set("User-Agent", "Margin/1.0 (public article reader)")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9")
	request.Header.Set("Accept-Encoding", "identity")
	request.Close = true
	response, err := transport.RoundTrip(request)
	return response, transport.CloseIdleConnections, err
}

func readResponse(response *http.Response) ([]byte, string, error) {
	if response.StatusCode == 401 || response.StatusCode == 402 || response.StatusCode == 403 {
		return nil, "", errors.New("this page requires access or blocks automated readers; paste text you can access; only public articles can be imported")
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the article server returned HTTP %d; try pasting the text", response.StatusCode)
	}
	contentType := response.Header.Get("Content-Type")
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if mediaType != "" && mediaType != "text/html" && mediaType != "application/xhtml+xml" && mediaType != "text/plain" {
		return nil, "", errors.New("this URL does not point to an HTML article or plain text")
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" {
		return nil, "", errors.New("the article server returned unsupported compressed content; paste the article text instead")
	}
	if response.ContentLength > maxArticleBytes {
		return nil, "", errArticleLimit
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxArticleBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("the article download was interrupted or timed out; try again or paste the text: %w", err)
	}
	if len(body) > maxArticleBytes {
		return nil, "", errArticleLimit
	}
	if response.ContentLength >= 0 && int64(len(body)) != response.ContentLength {
		return nil, "", errors.New("the article download ended before the full page arrived; try again or paste the text")
	}
	return body, contentType, nil
}

func extract(body []byte, finalURL *url.URL, contentType string) (domain.Article, error) {
	title, text := finalURL.Hostname(), ""
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "text/plain" {
		text = strings.ToValidUTF8(string(body), "�")
	} else {
		// Parse supplied HTML ourselves: the extractor's reader API accepts
		// gzip magic bytes and could otherwise inflate a disguised zip bomb.
		if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
			return domain.Article{}, errors.New("the article server returned compressed content without a supported encoding")
		}
		reader, err := charset.NewReader(bytes.NewReader(body), contentType)
		if err != nil {
			return domain.Article{}, errors.New("could not decode the article text")
		}
		decoded, err := io.ReadAll(io.LimitReader(reader, maxArticleBytes+1))
		if err != nil {
			return domain.Article{}, errors.New("could not decode the article text")
		}
		if len(decoded) > maxArticleBytes {
			return domain.Article{}, errArticleLimit
		}
		if err := checkHTMLComplexity(decoded); err != nil {
			return domain.Article{}, err
		}
		doc, err := html.Parse(bytes.NewReader(decoded))
		if err != nil {
			return domain.Article{}, errors.New("could not parse the article HTML")
		}
		result, err := trafilatura.ExtractDocument(doc, trafilatura.Options{
			OriginalURL: finalURL, ExcludeComments: true, ExcludeTables: false,
			IncludeLinks: false, Focus: trafilatura.FavorPrecision, EnableFallback: true,
			MaxTreeSize: maxHTMLNodes, HtmlDateMode: trafilatura.Disabled,
		})
		if err != nil || result == nil {
			return domain.Article{}, errors.New("no readable public article was found; the post may require a subscription; paste text you can access")
		}
		if result.Metadata.Title != "" {
			title = result.Metadata.Title
		}
		// The extractor renders structural blocks on separate lines. Preserve
		// their boundaries as paragraphs and therefore longer narration pauses.
		var paragraphs []string
		for _, line := range strings.Split(result.ContentText, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				paragraphs = append(paragraphs, line)
			}
		}
		text = strings.Join(paragraphs, "\n\n")
	}
	text = PrepareText(text)
	if utf8.RuneCountInString(text) < 200 || len(strings.Fields(text)) < 35 {
		return domain.Article{}, errTooLittleText
	}
	title = strings.Join(strings.Fields(title), " ")
	titleRunes := []rune(title)
	if len(titleRunes) > 240 {
		title = string(titleRunes[:240])
	}
	return domain.Article{Title: title, Text: text, SourceURL: finalURL.String()}, nil
}

func checkHTMLComplexity(body []byte) error {
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	nodes, depth := 0, 0
	for {
		t := tokenizer.Next()
		if t == html.ErrorToken {
			if err := tokenizer.Err(); err != io.EOF {
				return errors.New("could not parse the article HTML")
			}
			return nil
		}
		nodes++
		if nodes > maxHTMLNodes {
			return errors.New("the article HTML is too complex; paste the article text instead")
		}
		if t == html.StartTagToken {
			name, _ := tokenizer.TagName()
			if !strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+string(name)+" ") {
				depth++
			}
		} else if t == html.EndTagToken && depth > 0 {
			depth--
		}
		if depth > 256 {
			return errors.New("the article HTML is too deeply nested; paste the article text instead")
		}
	}
}
