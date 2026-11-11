package library

import (
	"margin/internal/domain"
	"math"
	"strings"
	"testing"
)

func estimateSegments(lengths ...int) []domain.Segment {
	var result []domain.Segment
	for _, n := range lengths {
		result = append(result, domain.Segment{Text: strings.Repeat("x", n)})
	}
	return result
}
func TestEstimateWarmupAndWeight(t *testing.T) {
	e := newEstimate(estimateSegments(10, 20, 30, 60))
	if e.completed(0, 120) != nil {
		t.Fatal("warmup included")
	}
	for i, v := range []struct{ elapsed, remaining float64 }{{2, 9}, {3, 6}, {6, 0}} {
		got := e.completed(i+1, v.elapsed)
		if got == nil || *got != v.remaining {
			t.Fatalf("estimate %v, wanted %v", got, v.remaining)
		}
	}
}
func TestEstimateRecentSlowdown(t *testing.T) {
	lengths := make([]int, 19)
	for i := range lengths {
		lengths[i] = 10
	}
	e := newEstimate(estimateSegments(lengths...))
	e.completed(0, 100)
	for i := 1; i <= 8; i++ {
		e.completed(i, 1)
	}
	var v *float64
	for i := 9; i <= 16; i++ {
		v = e.completed(i, 3)
	}
	if v == nil || *v <= 5 || *v >= 6 {
		t.Fatalf("slowdown estimate: %v", v)
	}
}
func TestEstimateInvalidMeasurements(t *testing.T) {
	e := newEstimate(estimateSegments(10, 10, 10, 10, 10, 10, 10))
	e.completed(0, 1)
	for i, v := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if e.completed(i+1, v) != nil {
			t.Fatal("invalid timing counted")
		}
	}
	if got := e.completed(5, 2); got == nil || *got != 2 {
		t.Fatalf("estimate: %v", got)
	}
	if got := e.completed(6, 2); got == nil || *got != 0 {
		t.Fatalf("done: %v", got)
	}
}
func TestEstimateShortReading(t *testing.T) {
	e := newEstimate(estimateSegments(12))
	if got := e.completed(0, 100); got == nil || *got != 0 {
		t.Fatalf("short reading: %v", got)
	}
}
