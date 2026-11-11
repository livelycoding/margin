package speech

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUnsupportedWordErrorIdentifiesText(t *testing.T) {
	p, err := newPronouncer(assetsDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	p.fallbackFunc = func(context.Context, string) (string, error) { return "", errUnpronounceable }
	_, err = p.phonemize(context.Background(), "Hello 日本語")
	if !errors.Is(err, errUnpronounceable) || !strings.Contains(err.Error(), "日本語") || !strings.Contains(err.Error(), "narration in English") {
		t.Fatalf("missing unsupported-text explanation: %v", err)
	}
	_, err = p.phonemize(context.Background(), strings.Repeat("日", 100))
	if err == nil || strings.Contains(err.Error(), strings.Repeat("日", 65)) {
		t.Fatalf("unbounded word error: %v", err)
	}
}

func TestPreflightRejectsInvalidInputBeforeLoading(t *testing.T) {
	e, _ := New(t.TempDir(), t.TempDir(), "missing.so")
	defer e.Close()
	for _, tc := range []struct{ text, voice string }{{"", "af_heart"}, {"Hello", ""}, {"Hello", "unknown"}} {
		if err := e.ValidateText(context.Background(), tc.text, tc.voice); err == nil {
			t.Fatalf("accepted invalid input: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.ValidateText(ctx, "Hello", "af_heart"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
