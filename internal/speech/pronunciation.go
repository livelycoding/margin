package speech

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/runenames"
)

var letterNames = map[rune]string{'a': "ˈA", 'b': "bˈi", 'c': "sˈi", 'd': "dˈi", 'e': "ˈi", 'f': "ˈɛf", 'g': "ʤˈi", 'h': "ˈAʧ", 'i': "ˈI", 'j': "ʤˈA", 'k': "kˈA", 'l': "ˈɛl", 'm': "ˈɛm", 'n': "ˈɛn", 'o': "ˈO", 'p': "pˈi", 'q': "kjˈu", 'r': "ˈɑɹ", 's': "ˈɛs", 't': "tˈi", 'u': "jˈu", 'v': "vˈi", 'w': "dˈʌbᵊlju", 'x': "ˈɛks", 'y': "wˈI", 'z': "zˈi"}

func wordSet(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

var wordAcronyms = wordSet("NASA NATO UNESCO UNICEF OPEC COVID AIDS LASER RADAR SCUBA ASCII SQL ASAP RAM ROM GIF JPEG PIN SIM FAQ FOMO YOLO")
var initialisms = wordSet("AI API CPU GPU GPT HTML HTTP HTTPS URL USB UI UX TV UK USA US EU UN IT IP PDF DVD CD BBC CNN FBI CIA CEO CTO CFO ID DIY TTS STT LLM MIT GPL DNS SSH VPN PC AM PM")
var lowerInitialisms = wordSet("http https www html api cpu gpu gpt usb url pdf")
var verbBefore = wordSet("i you we they he she to will would can could should must may might shall please do does did don't doesn't didn't let's let often usually sometimes always never")

const punctuation = `;:,.!?—…"()“”`
const vowelPhones = "AIOQWYaeiouɑɐɒæɔəɚɛɜɪʊʌᵻ"

type pronouncer struct {
	british      bool
	lexicon      map[string]string
	vocabulary   map[rune]int64
	fallback     *neuralG2P
	fallbackFunc func(context.Context, string) (string, error)
	cache        map[string]string
}

type unsupportedWordError struct {
	word  string
	cause error
}

func (e *unsupportedWordError) Error() string {
	word := []rune(e.word)
	if len(word) > 64 {
		word = append(word[:64], '…')
	}
	return fmt.Sprintf("Margin couldn’t read %q aloud. It currently supports narration in English; other languages and some symbols or specialized notation aren’t supported yet.", string(word))
}
func (e *unsupportedWordError) Unwrap() error { return e.cause }

func newPronouncer(dir string, british bool) (*pronouncer, error) {
	accent := "us"
	if british {
		accent = "gb"
	}
	file, err := os.Open(filepath.Join(dir, "lexicon-"+accent+"-en.txt.gz"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	source, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	p := &pronouncer{british: british, lexicon: map[string]string{}, cache: map[string]string{}}
	scan := bufio.NewScanner(source)
	scan.Buffer(make([]byte, 4096), 1024*1024)
	for scan.Scan() {
		word, phones, ok := strings.Cut(scan.Text(), " ")
		if ok && strings.TrimSpace(phones) != "" {
			p.lexicon[word] = strings.Join(strings.Fields(phones), "")
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(p.lexicon) == 0 {
		return nil, errors.New("The English pronunciation dictionary is empty.")
	}
	p.vocabulary, err = loadVocabulary(filepath.Join(dir, "tokens.txt"))
	if err != nil {
		return nil, err
	}
	p.fallback, err = newG2P(dir, british)
	if err != nil {
		return nil, err
	}
	p.fallbackFunc = p.fallback.phonemize
	return p, nil
}
func (p *pronouncer) letter(letter rune) string {
	if p.british {
		if letter == 'z' {
			return "zˈɛd"
		}
		if value, ok := p.lexicon[string(letter)]; ok {
			return value
		}
	}
	return letterNames[letter]
}
func (p *pronouncer) spell(word string) string {
	var out []string
	for _, r := range word {
		if r < 'a' || r > 'z' {
			if r < 'A' || r > 'Z' {
				return ""
			}
		}
		out = append(out, p.letter(unicode.ToLower(r)))
	}
	return strings.Join(out, " ")
}
func isUpper(word string) bool {
	hasUpper := false
	for _, r := range word {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			hasUpper = true
		}
	}
	return hasUpper
}
func camelParts(word string) []string {
	runes := []rune(word)
	start := 0
	var out []string
	for i := 1; i < len(runes); i++ {
		if runes[i-1] >= 'a' && runes[i-1] <= 'z' && runes[i] >= 'A' && runes[i] <= 'Z' {
			out = append(out, string(runes[start:i]))
			start = i
		}
	}
	out = append(out, string(runes[start:]))
	return out
}
func (p *pronouncer) word(ctx context.Context, word string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if value, ok := p.cache[word]; ok {
		return value, nil
	}
	phones, err := p.wordUncached(ctx, word)
	if err == nil {
		if len(p.cache) >= 4096 {
			clear(p.cache)
		}
		p.cache[word] = phones
	}
	return phones, err
}
func (p *pronouncer) wordUncached(ctx context.Context, word string) (string, error) {
	lower := strings.ToLower(word)
	_, known := p.lexicon[lower]
	if initialisms[word] || lowerInitialisms[lower] || (isUpper(word) && utf8.RuneCountInString(word) > 1 && !wordAcronyms[word] && !known) {
		if spelled := p.spell(word); spelled != "" {
			return spelled, nil
		}
	}
	if len(word) == 1 && ((word[0] >= 'a' && word[0] <= 'z') || (word[0] >= 'A' && word[0] <= 'Z')) {
		return p.letter(rune(lower[0])), nil
	}
	if value, ok := p.lexicon[lower]; ok {
		return value, nil
	}
	ascii := asciiWord(lower)
	if value, ok := p.lexicon[ascii]; ok {
		return value, nil
	}
	if strings.HasSuffix(lower, "'s") && len(lower) > 2 {
		stem, err := p.word(ctx, word[:len(word)-2])
		if err != nil {
			return "", err
		}
		runes := []rune(stem)
		if len(runes) == 0 {
			return "", errors.New("Could not pronounce possessive stem")
		}
		last := runes[len(runes)-1]
		ending := "z"
		if strings.ContainsRune("ptkfθ", last) {
			ending = "s"
		} else if strings.ContainsRune("szʃʒʧʤ", last) {
			ending = "ᵻz"
			if p.british {
				ending = "ɪz"
			}
		}
		return stem + ending, nil
	}
	parts := []string{word}
	if strings.Contains(word, "-") {
		parts = strings.Split(word, "-")
	} else {
		parts = camelParts(word)
	}
	if len(parts) > 1 {
		var out []string
		for _, part := range parts {
			if part != "" {
				value, err := p.word(ctx, part)
				if err != nil {
					return "", err
				}
				out = append(out, value)
			}
		}
		return strings.Join(out, " "), nil
	}
	phones, err := p.fallbackFunc(ctx, ascii)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !errors.Is(err, errUnpronounceable) {
			return "", err
		}
		spelled := p.spell(strings.ReplaceAll(ascii, "'", ""))
		if spelled == "" {
			return "", err
		}
		log.Printf("Pronunciation fallback could not resolve a %d-letter word; spelling its letters.", utf8.RuneCountInString(ascii))
		return spelled, nil
	}
	if phones == "" {
		return "", errors.New("Could not pronounce a word in the reading.")
	}
	return phones, nil
}
func (p *pronouncer) context(word, before, after, phones, nextPhones string) string {
	lower, beforeLower, afterLower := strings.ToLower(word), strings.ToLower(before), strings.ToLower(after)
	verb := verbBefore[beforeLower]
	switch lower {
	case "a":
		if after != "" {
			return "ə"
		}
		return p.letter('a')
	case "an":
		return "ən"
	case "the":
		trimmed := strings.TrimLeft(nextPhones, "ˈˌ")
		if trimmed != "" {
			first, _ := utf8.DecodeRuneInString(trimmed)
			if strings.ContainsRune(vowelPhones, first) {
				return "ði"
			}
		}
		return "ðə"
	case "to":
		if after != "" {
			return "tə"
		}
	case "live":
		if verb || beforeLower == "who" || beforeLower == "where" {
			return "lˈɪv"
		}
	case "read":
		if wordSet("have has had already just")[beforeLower] || wordSet("yesterday earlier previously")[afterLower] {
			return "ɹˈɛd"
		}
	case "lead":
		if wordSet("pipe pipes paint poisoning metal pencil weight")[afterLower] {
			return "lˈɛd"
		}
	case "use":
		if wordSet("a the its their our your")[beforeLower] {
			if p.british {
				return "jˈuːs"
			}
			return "jˈus"
		}
	}
	imperative := before == "" && wordSet("it this that the a your")[afterLower]
	if verb || imperative {
		switch lower {
		case "record":
			if p.british {
				return "ɹɪkˈɔːd"
			}
			return "ɹəkˈɔɹd"
		case "close":
			if p.british {
				return "klˈQz"
			}
			return "klˈOz"
		case "wind":
			return "wˈInd"
		}
	}
	return phones
}
func tokenize(text string) []string {
	runes := []rune(text)
	var tokens []string
	for i := 0; i < len(runes); {
		start := i
		r := runes[i]
		i++
		if unicode.IsSpace(r) {
			for i < len(runes) && unicode.IsSpace(runes[i]) {
				i++
			}
		} else if unicode.IsLetter(r) {
			for i < len(runes) {
				if unicode.IsLetter(runes[i]) {
					i++
					continue
				}
				if (runes[i] == '\'' || runes[i] == '-') && i+1 < len(runes) && unicode.IsLetter(runes[i+1]) {
					i += 2
					continue
				}
				break
			}
		}
		tokens = append(tokens, string(runes[start:i]))
	}
	return tokens
}
func sentenceBoundary(tokens []string) bool {
	for _, t := range tokens {
		if t == "." || t == "!" || t == "?" || t == ";" {
			return true
		}
	}
	return false
}
func (p *pronouncer) phonemize(ctx context.Context, text string) (string, error) {
	normalized, err := normalizeText(text)
	if err != nil {
		return "", err
	}
	tokens := tokenize(normalized)
	var words []int
	pronounced := map[int]string{}
	for i, t := range tokens {
		first, _ := utf8.DecodeRuneInString(t)
		if unicode.IsLetter(first) {
			words = append(words, i)
			phones, err := p.word(ctx, t)
			if err != nil {
				if errors.Is(err, errUnpronounceable) {
					return "", &unsupportedWordError{word: t, cause: err}
				}
				return "", err
			}
			pronounced[i] = phones
		}
	}
	for position, index := range words {
		before, after, nextPhones := "", "", ""
		if position > 0 {
			previous := words[position-1]
			if !sentenceBoundary(tokens[previous+1 : index]) {
				before = tokens[previous]
			}
		}
		if position+1 < len(words) {
			following := words[position+1]
			if !sentenceBoundary(tokens[index+1 : following]) {
				after = tokens[following]
			}
			nextPhones = pronounced[following]
		}
		pronounced[index] = p.context(tokens[index], before, after, pronounced[index], nextPhones)
	}
	var output strings.Builder
	for i, token := range tokens {
		if phones, ok := pronounced[i]; ok {
			output.WriteString(phones)
			continue
		}
		first, _ := utf8.DecodeRuneInString(token)
		switch {
		case unicode.IsSpace(first):
			output.WriteByte(' ')
		case strings.ContainsRune(punctuation, first):
			output.WriteString(token)
		case token == "'":
			output.WriteByte('"')
		case token == "-":
			output.WriteRune('—')
		default:
			name := runenames.Name(first)
			if name == "" || strings.HasPrefix(name, "<") {
				return "", fmt.Errorf("Unsupported character in the reading: U+%04X", first)
			}
			output.WriteByte(' ')
			for j, w := range strings.Fields(name) {
				if j > 0 {
					output.WriteByte(' ')
				}
				phones, err := p.word(ctx, strings.ToLower(w))
				if err != nil {
					return "", err
				}
				output.WriteString(phones)
			}
			output.WriteByte(' ')
		}
	}
	phones := strings.NewReplacer("ɾ", "T", "ʔ", "t").Replace(strings.Join(strings.Fields(output.String()), " "))
	speakable := false
	for _, r := range phones {
		if _, ok := p.vocabulary[r]; !ok {
			return "", fmt.Errorf("The pronunciation engine returned unsupported phonemes: %c", r)
		}
		if unicode.IsLetter(r) {
			speakable = true
		}
	}
	if !speakable {
		return "", errors.New("No speakable text was found.")
	}
	return phones, nil
}
func (p *pronouncer) close() error {
	if p.fallback != nil {
		return p.fallback.close()
	}
	return nil
}
