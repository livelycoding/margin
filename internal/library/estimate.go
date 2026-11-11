package library

import (
	"margin/internal/domain"
	"math"
	"strings"
	"unicode/utf8"
)

type measurement struct {
	weight  int
	seconds float64
}
type generationEstimate struct {
	weights                   []int
	remaining, measuredWeight int
	measuredSeconds           float64
	recent                    []measurement
}

func newEstimate(segments []domain.Segment) *generationEstimate {
	e := &generationEstimate{}
	for _, s := range segments {
		weight := utf8.RuneCountInString(strings.TrimSpace(s.Text))
		if weight < 1 {
			weight = 1
		}
		e.weights = append(e.weights, weight)
		e.remaining += weight
	}
	return e
}
func (e *generationEstimate) completed(index int, seconds float64) *float64 {
	weight := e.weights[index]
	e.remaining -= weight
	if index > 0 && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		e.recent = append(e.recent, measurement{weight, seconds})
		if len(e.recent) > 8 {
			e.recent = e.recent[1:]
		}
		e.measuredWeight += weight
		e.measuredSeconds += seconds
	}
	if e.remaining <= 0 {
		v := float64(0)
		return &v
	}
	if e.measuredWeight == 0 {
		return nil
	}
	var secondsRecent float64
	weightRecent := 0
	for _, m := range e.recent {
		secondsRecent += m.seconds
		weightRecent += m.weight
	}
	rate := .65*secondsRecent/float64(weightRecent) + .35*e.measuredSeconds/float64(e.measuredWeight)
	v := math.Max(1, math.RoundToEven(float64(e.remaining)*rate*10)/10)
	return &v
}
