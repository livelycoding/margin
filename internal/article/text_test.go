package article

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"margin/internal/domain"
)

func TestPrepareTextPreservesContent(t *testing.T) {
	source := "# A title\n\nA **bold** [linked phrase](https://example.com/a_(b)).\n\n- First item\n2. Second item\n\n---\n\n`code_name` &amp; _words_"
	want := "A title\n\nA bold linked phrase.\n\nFirst item\nSecond item\n\ncode_name & words"
	if got := PrepareText(source); got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
	cases := map[string]string{
		"**One** **two** and ~~three~~.": "One two and three.",
		"![Description](https://example.com/image.png) and [reference][id]\n\n[id]: https://example.com": "Description and reference",
		"```go\nvar x = 2\n```\n\n> Quoted words":                                                        "var x = 2\n\nQuoted words",
		"- [x] Done\n- [ ] Open\r\n\nfoo_bar and foo__bar":                                               "Done\nOpen\n\nfoo_bar and foo__bar",
		"[broken](unclosed and [another](open":                                                           "[broken](unclosed and [another](open",
	}
	for source, want := range cases {
		if got := PrepareText(source); got != want {
			t.Errorf("%q: got %q; want %q", source, got, want)
		}
	}
}

func TestMalformedMarkdownIsBounded(t *testing.T) {
	source := strings.Repeat("[broken](", 16000)
	if got := PrepareText(source); got != source {
		t.Fatal("malformed Markdown lost content")
	}
}

func TestSentenceBoundaries(t *testing.T) {
	source := "Dr. A. Smith paid $3.14 in the U.S. yesterday. \"Really?\" Yes!\n\nA new paragraph."
	want := []domain.Segment{
		{Text: "Dr. A. Smith paid $3.14 in the U.S. yesterday.", Pause: .12},
		{Text: "\"Really?\"", Pause: .12}, {Text: "Yes!", Pause: .45}, {Text: "A new paragraph.", Pause: .45},
	}
	if got := SplitSegments(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func TestLongSegmentsPreserveWordsAndUnicode(t *testing.T) {
	source := strings.Repeat("word ", 70) + "; a clause, " + strings.Repeat("café ", 150) + ".\n\nFinal words without a stop"
	segments := SplitSegments(source)
	var texts []string
	for _, segment := range segments {
		texts = append(texts, segment.Text)
		if utf8.RuneCountInString(segment.Text) > 350 {
			t.Errorf("chunk too long: %d", utf8.RuneCountInString(segment.Text))
		}
	}
	if got := strings.Join(texts, " "); got != strings.Join(strings.Fields(source), " ") {
		t.Fatalf("text changed: %q", got)
	}
	if segments[len(segments)-1].Pause != .45 {
		t.Fatal("missing paragraph pause")
	}
	source = strings.Repeat("x", 600) + " next."
	texts = nil
	for _, segment := range SplitSegments(source) {
		texts = append(texts, segment.Text)
	}
	if got := strings.Join(texts, " "); got != source {
		t.Fatal("long word was truncated")
	}
}

func TestEmptyText(t *testing.T) {
	if got := SplitSegments(" \n\n"); len(got) != 0 {
		t.Fatalf("unexpected segments: %#v", got)
	}
	if got, err := SRT(nil); got != "" || err != nil {
		t.Fatalf("unexpected SRT: %q, %v", got, err)
	}
}

func TestSRTMeasuredBoundaries(t *testing.T) {
	caption := strings.Repeat("Some caption words ", 12)
	got, err := SRT([]domain.Cue{{Start: 0, End: 1.234, Text: "First sentence."}, {Start: 3661.002, End: 3665.125, Text: caption}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "1\n00:00:00,000 --> 00:00:01,234\nFirst sentence.\n\n2\n") {
		t.Fatalf("unexpected SRT: %q", got)
	}
	parts := strings.Split(got, "01:01:01,002 --> 01:01:05,125\n")
	if len(parts) != 2 {
		t.Fatalf("missing measured timestamp: %q", got)
	}
	for _, line := range strings.Split(parts[1], "\n") {
		if utf8.RuneCountInString(line) > 78 {
			t.Errorf("line too wide: %q", line)
		}
	}
	if strings.Join(strings.Fields(parts[1]), " ") != strings.TrimSpace(caption) {
		t.Fatal("caption lost words")
	}
	got, err = SRT([]domain.Cue{{Start: 1, End: 1, Text: "Very short."}, {Start: 2, End: 3, Text: " "}})
	if err != nil || got != "1\n00:00:01,000 --> 00:00:01,001\nVery short.\n" {
		t.Fatalf("zero-length cue: %q, %v", got, err)
	}
}

func TestSRTRejectsInvalidTimestamps(t *testing.T) {
	for _, pair := range [][2]float64{{-1, 1}, {2, 1}, {math.NaN(), 2}, {0, math.Inf(1)}, {0, math.MaxFloat64}} {
		if _, err := SRT([]domain.Cue{{Start: pair[0], End: pair[1], Text: "No"}}); err == nil {
			t.Errorf("accepted invalid pair: %#v", pair)
		}
	}
}
