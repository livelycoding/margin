package speech

import (
	"errors"
	"golang.org/x/text/unicode/norm"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const numberPattern = `(?:\d{1,3}(?:,\d{3})+|\d+)(?:\.\d+)?`

var numberRE = regexp.MustCompile(numberPattern)
var unitsRE = regexp.MustCompile(`\b(` + numberPattern + `)\s*(km/h|km|cm|mm|nm|kg|mg|ml|ms|ns|[µμ][smgl]|m|g|l)([²³]?)`)
var degreeRE = regexp.MustCompile(`°\s*([CF])\b`)
var moneyRE = regexp.MustCompile(`([$£€¥])\s*([+-]?` + numberPattern + `)`)
var clockSuffixRE = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*([ap]\.?m\.?)`)
var clockPlainRE = regexp.MustCompile(`\b(\d{1,2}):(\d{2})\b`)
var ordinalRE = regexp.MustCompile(`(?i)\b(\d+)(st|nd|rd|th)\b`)
var alphaDashRE = regexp.MustCompile(`([A-Za-z])-(\d)`)
var signRE = regexp.MustCompile(`(^|[^\pL\pN_])([+-])(` + numberPattern + `)`)
var egRE = regexp.MustCompile(`(?i)\be\.g\.`)
var ieRE = regexp.MustCompile(`(?i)\bi\.e\.`)
var abbreviationRE = regexp.MustCompile(`(?i)\b(Dr|Mr|Mrs|Ms|Prof|Sr|Jr|vs|etc)\.`)
var dottedRE = regexp.MustCompile(`\b(?:[A-Za-z]\.){2,}`)
var tinyNumbers = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
var tensNumbers = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
var scales = []string{"", "thousand", "million", "billion", "trillion", "quadrillion", "quintillion", "sextillion", "septillion", "octillion", "nonillion", "decillion"}

// replaceMatches supplies original byte offsets so punctuation and Unicode
// boundaries can be checked without unsupported regular-expression lookahead.
func replaceMatches(text string, re *regexp.Regexp, fn func([]string, int, int) string) string {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	at := 0
	for _, match := range matches {
		out.WriteString(text[at:match[0]])
		parts := make([]string, len(match)/2)
		for i := range parts {
			a, b := match[i*2], match[i*2+1]
			if a >= 0 {
				parts[i] = text[a:b]
			}
		}
		out.WriteString(fn(parts, match[0], match[1]))
		at = match[1]
	}
	out.WriteString(text[at:])
	return out.String()
}
func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }
func atWordEnd(text string, end int) bool {
	if end >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return !isWord(r)
}
func underThousand(n int) string {
	if n < 20 {
		return tinyNumbers[n]
	}
	if n < 100 {
		out := tensNumbers[n/10]
		if n%10 != 0 {
			out += " " + tinyNumbers[n%10]
		}
		return out
	}
	out := tinyNumbers[n/100] + " hundred"
	if n%100 != 0 {
		out += " and " + underThousand(n%100)
	}
	return out
}
func integerWords(digits string) string {
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "zero"
	}
	groups := (len(digits) + 2) / 3
	var out []string
	for index := groups - 1; index >= 0; index-- {
		end := len(digits) - index*3
		start := max(0, end-3)
		n, _ := strconv.Atoi(digits[start:end])
		if n == 0 {
			continue
		}
		piece := underThousand(n)
		if index > 0 {
			piece += " " + scales[index]
		}
		if index == 0 && n < 100 && len(out) > 0 {
			piece = "and " + piece
		}
		out = append(out, piece)
	}
	return strings.Join(out, " ")
}
func numberWords(value string, ordinal bool) string {
	value = strings.ReplaceAll(value, ",", "")
	if ordinal {
		value = strings.TrimLeft(value, "0")
		if value == "" {
			value = "0"
		}
	}
	if len(value) > 36 || (strings.HasPrefix(value, "0") && len(value) > 1 && !strings.Contains(value, ".")) {
		var words []string
		for _, c := range value {
			if c >= '0' && c <= '9' {
				words = append(words, tinyNumbers[c-'0'])
			}
		}
		return strings.Join(words, " ")
	}
	whole, fraction, decimal := strings.Cut(value, ".")
	out := integerWords(whole)
	if decimal {
		out += " point"
		for _, c := range fraction {
			out += " " + tinyNumbers[c-'0']
		}
	}
	if ordinal {
		parts := strings.Split(out, " ")
		last := parts[len(parts)-1]
		irregular := map[string]string{"zero": "zeroth", "one": "first", "two": "second", "three": "third", "five": "fifth", "eight": "eighth", "nine": "ninth", "twelve": "twelfth"}
		if changed, ok := irregular[last]; ok {
			last = changed
		} else if strings.HasSuffix(last, "y") {
			last = strings.TrimSuffix(last, "y") + "ieth"
		} else {
			last += "th"
		}
		parts[len(parts)-1] = last
		out = strings.Join(parts, " ")
	}
	return out
}
func normalizeText(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("Enter some text to narrate.")
	}
	text = foldDecimalDigits(text)
	units := map[string][2]string{
		"km/h": {"kilometer per hour", "kilometers per hour"},
		"km":   {"kilometer", "kilometers"},
		"cm":   {"centimeter", "centimeters"},
		"mm":   {"millimeter", "millimeters"},
		"nm":   {"nanometer", "nanometers"},
		"µm":   {"micrometer", "micrometers"},
		"kg":   {"kilogram", "kilograms"},
		"mg":   {"milligram", "milligrams"},
		"µg":   {"microgram", "micrograms"},
		"ml":   {"milliliter", "milliliters"},
		"µl":   {"microliter", "microliters"},
		"ms":   {"millisecond", "milliseconds"},
		"ns":   {"nanosecond", "nanoseconds"},
		"µs":   {"microsecond", "microseconds"},
		"m":    {"meter", "meters"},
		"g":    {"gram", "grams"},
		"l":    {"liter", "liters"},
	}
	source := text
	text = replaceMatches(source, unitsRE, func(m []string, _, end int) string {
		if !atWordEnd(source, end) {
			return m[0]
		}
		amount := strings.ReplaceAll(m[1], ",", "")
		singular := amount == "1" || amount == "1.0" || amount == "1.00"
		prefix := ""
		if m[3] == "²" {
			prefix = "square "
		} else if m[3] == "³" {
			prefix = "cubic "
		}
		i := 1
		if singular {
			i = 0
		}
		// Both the micro sign and Greek mu occur in copied measurements.
		// Expand units before NFKC turns the micro sign into a Greek letter.
		unit := strings.ReplaceAll(m[2], "μ", "µ")
		return m[1] + " " + prefix + units[unit][i]
	})
	text = norm.NFKC.String(text)
	text = replaceMatches(text, degreeRE, func(m []string, _, _ int) string {
		unit := "Celsius"
		if m[1] == "F" {
			unit = "Fahrenheit"
		}
		return " degrees " + unit
	})
	text = strings.NewReplacer("’", "'", "‘", "'", "ʼ", "'", "‐", "-", "‑", "-", "–", "—", "−", "-", "\u00a0", " ", "\u200b", "", "\ufeff", "", "[", "(", "]", ")", "{", "(", "}", ")", "`", "\"").Replace(text)
	text = replaceMatches(text, moneyRE, func(m []string, _, _ int) string {
		value := strings.ReplaceAll(m[2], ",", "")
		prefix := ""
		if strings.HasPrefix(value, "-") {
			prefix = "minus "
		} else if strings.HasPrefix(value, "+") {
			prefix = "plus "
		}
		value = strings.TrimLeft(value, "+-")
		names := map[string][4]string{"$": {"dollar", "dollars", "cent", "cents"}, "£": {"pound", "pounds", "penny", "pence"}, "€": {"euro", "euros", "cent", "cents"}, "¥": {"yen", "yen", "sen", "sen"}}[m[1]]
		whole, fraction, decimal := strings.Cut(value, ".")
		if decimal && len(fraction) > 2 {
			return prefix + numberWords(value, false) + " " + names[1]
		}
		minor := 0
		if fraction != "" {
			minor, _ = strconv.Atoi((fraction + "00")[:2])
		}
		zero := strings.Trim(whole, "0") == ""
		singular := strings.TrimLeft(whole, "0") == "1"
		var parts []string
		if !zero || minor == 0 {
			unit := names[1]
			if singular {
				unit = names[0]
			}
			parts = append(parts, numberWords(whole, false)+" "+unit)
		}
		if minor > 0 {
			unit := names[3]
			if minor == 1 {
				unit = names[2]
			}
			parts = append(parts, numberWords(strconv.Itoa(minor), false)+" "+unit)
		}
		return prefix + strings.Join(parts, " and ")
	})
	clock := func(source string) func([]string, int, int) string {
		return func(m []string, _, end int) string {
			if !atWordEnd(source, end) {
				return m[0]
			}
			hour, _ := strconv.Atoi(m[1])
			minutes := m[2]
			if minutes == "" {
				minutes = "00"
			}
			minute, _ := strconv.Atoi(minutes)
			if hour > 23 || minute > 59 {
				return m[0]
			}
			out := numberWords(m[1], false)
			if minutes == "00" {
				out += " o'clock"
			} else if minutes[0] == '0' {
				out += " oh " + numberWords(strconv.Itoa(minute), false)
			} else {
				out += " " + numberWords(minutes, false)
			}
			if len(m) > 3 && m[3] != "" {
				out += " " + strings.ToUpper(strings.ReplaceAll(m[3], ".", ""))
				if strings.HasSuffix(m[3], ".") && strings.TrimSpace(source[end:]) == "" {
					out += "."
				}
			}
			return out
		}
	}
	text = replaceMatches(text, clockSuffixRE, clock(text))
	text = replaceMatches(text, clockPlainRE, clock(text))
	text = replaceMatches(text, ordinalRE, func(m []string, _, _ int) string { return numberWords(m[1], true) })
	text = alphaDashRE.ReplaceAllString(text, "${1} ${2}")
	text = replaceMatches(text, signRE, func(m []string, _, _ int) string {
		sign := "minus "
		if m[2] == "+" {
			sign = "plus "
		}
		return m[1] + sign + m[3]
	})
	text = numberRE.ReplaceAllStringFunc(text, func(s string) string { return " " + numberWords(s, false) + " " })
	text = egRE.ReplaceAllString(text, "for example")
	text = ieRE.ReplaceAllString(text, "that is")
	source = text
	text = replaceMatches(source, abbreviationRE, func(m []string, _, end int) string {
		if end < len(source) {
			r, _ := utf8.DecodeRuneInString(source[end:])
			if !unicode.IsSpace(r) {
				return m[0]
			}
		}
		word := map[string]string{"dr": "doctor", "mr": "mister", "mrs": "missus", "ms": "miz", "prof": "professor", "sr": "senior", "jr": "junior", "vs": "versus", "etc": "et cetera"}[strings.ToLower(m[1])]
		if strings.TrimSpace(source[end:]) == "" {
			word += "."
		}
		return word
	})
	source = text
	text = replaceMatches(source, dottedRE, func(m []string, _, end int) string {
		out := strings.ToUpper(strings.ReplaceAll(m[0], ".", ""))
		if strings.TrimSpace(source[end:]) == "" {
			out += "."
		}
		return out
	})
	text = strings.NewReplacer("%", " percent ", "&", " and ", "@", " at ", "+", " plus ", "=", " equals ", "#", " number ", "/", " slash ", "\\", " backslash ", "*", " asterisk ", "<", " less than ", ">", " greater than ", "^", " caret ", "|", " vertical bar ", "$", " dollars ", "£", " pounds ", "€", " euros ", "¥", " yen ", "_", "   ").Replace(text)
	return text, nil
}
func asciiWord(word string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFKD.String(word))
}

// Python's decimal-number matching accepts all Unicode Nd digits. Go's RE2
// \d only accepts ASCII; fold decimal digits before applying the same rules.
func foldDecimalDigits(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 || !unicode.Is(unicode.Nd, r) {
			return r
		}
		for _, block := range unicode.Nd.R16 {
			if uint32(r) >= uint32(block.Lo) && uint32(r) <= uint32(block.Hi) && (uint32(r)-uint32(block.Lo))%uint32(block.Stride) == 0 {
				return '0' + rune((uint32(r)-uint32(block.Lo))/uint32(block.Stride)%10)
			}
		}
		for _, block := range unicode.Nd.R32 {
			if uint32(r) >= block.Lo && uint32(r) <= block.Hi && (uint32(r)-block.Lo)%block.Stride == 0 {
				return '0' + rune((uint32(r)-block.Lo)/block.Stride%10)
			}
		}
		return r
	}, text)
}
