package library

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type validatingEngine struct {
	fakeEngine
	validate func(context.Context, string, string) error
}

func (e *validatingEngine) ValidateText(ctx context.Context, text, voice string) error {
	return e.validate(ctx, text, voice)
}

func TestPreflightRejectsLateProblemBeforeAudio(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported text", true: "cancelled"}[cancelled], func(t *testing.T) {
			engine := &validatingEngine{}
			app, err := New(Options{DataDir: t.TempDir(), Engine: engine})
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("unsupported word")
			if cancelled {
				failure = context.Canceled
			}
			var checked []string
			engine.validate = func(ctx context.Context, text, voice string) error {
				checked = append(checked, text)
				if voice != "af_heart" {
					t.Errorf("unexpected voice: %q", voice)
				}
				if len(checked) == 2 {
					if cancelled {
						cancel()
						return ctx.Err()
					}
					return failure
				}
				return nil
			}
			engine.set(func(context.Context, string, string, float64) ([]float32, int, error) {
				t.Error("audio synthesis ran before the entire text passed validation")
				return nil, 0, failure
			})
			item := createTest(t, app, "First sentence.\n\nProblem at the end.")
			err = app.generate(ctx, item)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), "passage 2 of 2") {
				t.Fatalf("missing original error or location: %v", err)
			}
			if !reflect.DeepEqual(checked, []string{"First sentence.", "Problem at the end."}) {
				t.Fatalf("wrong utterances checked: %q", checked)
			}
			folder, _ := app.Path(item.ID)
			files, err := os.ReadDir(folder)
			if err != nil || len(files) != 0 {
				t.Fatalf("preflight left audio artifacts: %v, %v", files, err)
			}
		})
	}
}

func TestPreflightCompletesBeforeSynthesis(t *testing.T) {
	engine := &validatingEngine{}
	app, err := New(Options{DataDir: t.TempDir(), Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var steps []string
	engine.validate = func(_ context.Context, text, _ string) error { steps = append(steps, "check: "+text); return nil }
	engine.set(func(_ context.Context, text, _ string, _ float64) ([]float32, int, error) {
		steps = append(steps, "speak: "+text)
		return []float32{.1, .2}, 24000, nil
	})
	item := createTest(t, app, "First sentence.\n\nSecond sentence.")
	if err := app.generate(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	want := []string{"check: First sentence.", "check: Second sentence.", "speak: First sentence.", "speak: Second sentence."}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("generation order: %q", steps)
	}
}
