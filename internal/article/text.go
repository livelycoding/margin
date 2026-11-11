// Package article prepares spoken text, measured subtitles, and public articles.
package article

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"margin/internal/domain"
)

var (
	referenceLine    = regexp.MustCompile(`^ {0,3}\[[^]\n]+\]:\s*\S+.*$`)
	fenceLine        = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,}).*$")
	headingLine      = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.+?)(?:\s+#+)?$`)
	quoteLine        = regexp.MustCompile(`^[ \t]*> ?`)
	ruleLine         = regexp.MustCompile(`^[ \t]*(?:[-*_][ \t]*){3,}$|^[ \t]*=+[ \t]*$`)
	listLine         = regexp.MustCompile(`^[ \t]*(?:[-+*]|\d+[.)])[ \t]+(?:\[[ xX]\][ \t]+)?`)
	inlineCode       = regexp.MustCompile("`+([^`\\n]+)`+")
	escapedMarkup    = regexp.MustCompile("\\\\([`*_{}\\[\\]()#+.!>~-])")
	horizontalSpace  = regexp.MustCompile(`[ \t]+`)
	paragraphBreak   = regexp.MustCompile(`\n\s*\n`)
	initialism       = regexp.MustCompile(`^(?:[A-Za-z]\.)+[A-Za-z]$`)
	emphasisPatterns = makeEmphasisPatterns()
)

func makeEmphasisPatterns() []*regexp.Regexp {
	var patterns []*regexp.Regexp
	for _, marker := range []string{"**", "__", "*", "_", "~~"} {
		quoted := regexp.QuoteMeta(marker)
		patterns = append(patterns, regexp.MustCompile(`(^|[^\pL\pN_])`+quoted+`(\S(?:[^\n]*?\S)?)`+quoted+`($|[^\pL\pN_])`))
	}
	return patterns
}

// PrepareText removes Markdown presentation while retaining spoken content.
// Go's RE2 engine and a single-pass bracket matcher keep malformed Markdown
// bounded; imported text is never interpreted as HTML in the application UI.
func PrepareText(text string) string {
	text = html.UnescapeString(text)
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u00a0", " ", "\u200b", "").Replace(text)
	text = stripLinks(text)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case referenceLine.MatchString(line), fenceLine.MatchString(line), ruleLine.MatchString(line):
			line = ""
		default:
			line = headingLine.ReplaceAllString(line, "$1")
			line = quoteLine.ReplaceAllString(line, "")
			line = listLine.ReplaceAllString(line, "")
		}
		line = inlineCode.ReplaceAllString(line, "$1")
		for _, pattern := range emphasisPatterns {
			// Two passes also handle adjacent marked phrases sharing a separator.
			line = pattern.ReplaceAllString(line, "$1$2$3")
			line = pattern.ReplaceAllString(line, "$1$2$3")
		}
		line = escapedMarkup.ReplaceAllString(line, "$1")
		lines[i] = strings.TrimSpace(horizontalSpace.ReplaceAllString(line, " "))
	}
	var out []string
	for _, line := range lines {
		if line != "" || len(out) == 0 || out[len(out)-1] != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripLinks(text string) string {
	// Match delimiters once rather than repeatedly scanning malformed links.
	pairs := make(map[int]int)
	var stack []int
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			continue
		}
		switch text[i] {
		case '\n':
			stack = stack[:0]
		case '[', '(':
			stack = append(stack, i)
		case ']', ')':
			if len(stack) > 0 {
				open := stack[len(stack)-1]
				if text[open] == '[' && text[i] == ']' || text[open] == '(' && text[i] == ')' {
					pairs[open] = i
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		open := i
		if text[i] == '!' && i+1 < len(text) && text[i+1] == '[' {
			open++
		}
		if text[open] == '[' {
			if close, ok := pairs[open]; ok && close+1 < len(text) && (text[close+1] == '(' || text[close+1] == '[') {
				if destinationEnd, ok := pairs[close+1]; ok {
					out.WriteString(text[open+1 : close])
					i = destinationEnd + 1
					continue
				}
			}
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

var abbreviations = func() map[string]bool {
	m := make(map[string]bool)
	for _, s := range strings.Fields("mr mrs ms dr prof sr jr st mt vs etc e.g i.e a.m p.m approx dept fig no vol pp inc ltd co corp jan feb mar apr jun jul aug sep sept oct nov dec rev hon gen sen rep gov capt lt col sgt ave blvd rd") {
		m[s] = true
	}
	return m
}()

func sentences(paragraph string) []string {
	r := []rune(paragraph)
	var parts []string
	start := 0
	for i := 0; i < len(r); i++ {
		if !strings.ContainsRune(".!?", r[i]) {
			continue
		}
		punctuationStart := i
		for i+1 < len(r) && strings.ContainsRune(".!?", r[i+1]) {
			i++
		}
		punctuationEnd := i + 1
		for i+1 < len(r) && strings.ContainsRune("\"'”’)]", r[i+1]) {
			i++
		}
		end := i + 1
		if end < len(r) && !unicode.IsSpace(r[end]) {
			continue
		}
		if r[punctuationStart] == '.' && punctuationEnd == punctuationStart+1 && end < len(r) {
			tokenStart := punctuationStart
			for tokenStart > 0 && (unicode.IsLetter(r[tokenStart-1]) || unicode.IsDigit(r[tokenStart-1]) || r[tokenStart-1] == '_' || r[tokenStart-1] == '.') {
				tokenStart--
			}
			token := string(r[tokenStart:punctuationStart])
			if abbreviations[strings.ToLower(token)] || initialism.MatchString(token) || len(token) == 1 && token[0] >= 'A' && token[0] <= 'Z' {
				continue
			}
		}
		if part := strings.TrimSpace(string(r[start:end])); part != "" {
			parts = append(parts, part)
		}
		start = end
	}
	if tail := strings.TrimSpace(string(r[start:])); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

func shortChunks(sentence string, limit int) []string {
	r := []rune(sentence)
	var chunks []string
	for len(r) > limit {
		clause, space := 0, 0
		for i := 0; i <= limit && i < len(r); i++ {
			if !unicode.IsSpace(r[i]) {
				continue
			}
			end := i + 1
			for end < len(r) && unicode.IsSpace(r[end]) {
				end++
			}
			if i > 0 && strings.ContainsRune(",;:—–", r[i-1]) {
				clause = end
			}
			space = end
			i = end - 1
		}
		cut := space
		if clause >= limit/2 {
			cut = clause
		}
		if cut == 0 {
			cut = len(r)
			for i, c := range r {
				if unicode.IsSpace(c) {
					cut = i + 1
					break
				}
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(r[:cut])))
		r = r[cut:]
		for len(r) > 0 && unicode.IsSpace(r[0]) {
			r = r[1:]
		}
	}
	if s := strings.TrimSpace(string(r)); s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}

// SplitSegments returns utterances and pause seconds. Captions use measured
// audio durations for these segments, not estimated word-level timestamps.
func SplitSegments(text string) []domain.Segment {
	segments := make([]domain.Segment, 0)
	for _, paragraph := range paragraphBreak.Split(strings.TrimSpace(text), -1) {
		paragraph = strings.Join(strings.Fields(paragraph), " ")
		if paragraph == "" {
			continue
		}
		for _, sentence := range sentences(paragraph) {
			for _, chunk := range shortChunks(sentence, 350) {
				segments = append(segments, domain.Segment{Text: chunk, Pause: .12})
			}
		}
		if len(segments) > 0 {
			segments[len(segments)-1].Pause = .45
		}
	}
	return segments
}

func timestamp(milliseconds int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", milliseconds/3600000, milliseconds/60000%60, milliseconds/1000%60, milliseconds%1000)
}

func wrap(text string, width int) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// SRT serializes measured, finite segment boundaries as standard SRT subtitles.
func SRT(cues []domain.Cue) (string, error) {
	var blocks []string
	for _, cue := range cues {
		if math.IsNaN(cue.Start) || math.IsNaN(cue.End) || math.IsInf(cue.Start, 0) || math.IsInf(cue.End, 0) || cue.Start < 0 || cue.End < cue.Start || cue.End >= float64(math.MaxInt64)/1000 {
			return "", fmt.Errorf("caption timestamps must be finite, nonnegative, and ordered")
		}
		text := strings.Join(strings.Fields(cue.Text), " ")
		if text == "" {
			continue
		}
		start, end := int64(math.RoundToEven(cue.Start*1000)), int64(math.RoundToEven(cue.End*1000))
		if end <= start {
			end = start + 1
		}
		blocks = append(blocks, fmt.Sprintf("%d\n%s --> %s\n%s", len(blocks)+1, timestamp(start), timestamp(end), wrap(text, 78)))
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return strings.Join(blocks, "\n\n") + "\n", nil
}
