package speech

import (
	"strings"
	"testing"
)

func TestNormalizationScientificUnits(t *testing.T) {
	for _, unit := range []struct {
		symbol, singular, plural string
	}{
		{"nm", "nanometer", "nanometers"},
		{"ns", "nanosecond", "nanoseconds"},
		{"ms", "millisecond", "milliseconds"},
		{"µs", "microsecond", "microseconds"},
		{"μs", "microsecond", "microseconds"},
		{"µm", "micrometer", "micrometers"},
		{"μm", "micrometer", "micrometers"},
		{"µg", "microgram", "micrograms"},
		{"μg", "microgram", "micrograms"},
		{"µl", "microliter", "microliters"},
		{"μl", "microliter", "microliters"},
	} {
		t.Run(unit.symbol, func(t *testing.T) {
			for _, tc := range []struct{ text, want string }{
				{"1 " + unit.symbol, "one " + unit.singular},
				{"1.00" + unit.symbol, "one point zero zero " + unit.singular},
				{"2 " + unit.symbol, "two " + unit.plural},
				{"160" + unit.symbol, "one hundred and sixty " + unit.plural},
			} {
				got, err := normalizeText(tc.text)
				if err != nil {
					t.Fatal(err)
				}
				got = strings.Join(strings.Fields(got), " ")
				if got != tc.want {
					t.Errorf("%q normalized to %q; want %q", tc.text, got, tc.want)
				}
			}
		})
	}
}

func TestNormalizationOilPaintingMeasurements(t *testing.T) {
	text := "213 nm (15 ns), 266 nm (15 ns), and 2940 nm (160 µs)"
	want := "two hundred and thirteen nanometers ( fifteen nanoseconds), two hundred and sixty six nanometers ( fifteen nanoseconds), and two thousand nine hundred and forty nanometers ( one hundred and sixty microseconds)"
	got, err := normalizeText(text)
	if err != nil {
		t.Fatal(err)
	}
	if got = strings.Join(strings.Fields(got), " "); got != want {
		t.Errorf("scientific measurements normalized to %q; want %q", got, want)
	}
}

func TestNormalizationScientificUnitBoundaries(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"1 µsomething", "one μsomething"},
		{"1 nsec", "one nsec"},
		{"1 nmeter", "one nmeter"},
		{"1 msec", "one msec"},
		{"1 μgα", "one μgα"},
		{"1 µs2", "one μs two"},
		{"1 µs_suffix", "one μs suffix"},
		{"1 µS", "one μS"},
		{"1 μM", "one μM"},
		{"1 µG", "one μG"},
		{"1 μL", "one μL"},
		{"1 NS", "one NS"},
		{"1 NM", "one NM"},
		{"1 MS", "one MS"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := normalizeText(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if got = strings.Join(strings.Fields(got), " "); got != tc.want {
				t.Errorf("normalized to %q; want %q", got, tc.want)
			}
		})
	}
}
