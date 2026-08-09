package analyze

import (
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

func completed(name, conclusion string, start time.Time) gh.WorkflowRun {
	return gh.WorkflowRun{
		Name:         name,
		Status:       "completed",
		Conclusion:   conclusion,
		RunStartedAt: start,
		UpdatedAt:    start.Add(5 * time.Minute),
	}
}

var day = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

// Two runs that differ is one coin flip, not a flaky suite. Scoring that at 1.0
// -- the maximum -- was a real bug, found by running the tool against
// grafana/k6 where most workflows had two runs in the window.
func TestTwoRunsDoNotProduceAConfidentScore(t *testing.T) {
	runs := []gh.WorkflowRun{
		completed("lint", "success", day),
		completed("lint", "failure", day.Add(time.Hour)),
	}

	result := Analyze(runs, Options{ZombieHours: 6, Now: day.Add(24 * time.Hour)})

	if len(result.Workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(result.Workflows))
	}
	w := result.Workflows[0]
	if w.ScoreConfident {
		t.Errorf("a %d-run sample must not be reported as confident", w.Runs)
	}
	// The raw score is still computed and still maximal -- the guard is about
	// whether we are willing to show it, not about changing the maths.
	if w.FlakinessScore != 1.0 {
		t.Errorf("raw score = %v, want 1.0 (the guard should not alter the maths)", w.FlakinessScore)
	}
}

func TestEnoughRunsProducesAConfidentScore(t *testing.T) {
	var runs []gh.WorkflowRun
	for i := 0; i < MinRunsForScore; i++ {
		runs = append(runs, completed("ci", "success", day.Add(time.Duration(i)*time.Hour)))
	}

	result := Analyze(runs, Options{ZombieHours: 6, Now: day.Add(24 * time.Hour)})

	if !result.Workflows[0].ScoreConfident {
		t.Errorf("%d runs should clear the confidence threshold", MinRunsForScore)
	}
}

// A thin sample with an extreme score must not outrank a well-sampled one, or
// the noisiest row heads the table.
func TestConfidentScoresSortAboveThinOnes(t *testing.T) {
	runs := []gh.WorkflowRun{
		// Alternates over two runs: raw score 1.0, but not confident.
		completed("thin", "success", day),
		completed("thin", "failure", day.Add(time.Hour)),
	}
	// Enough runs to be confident, with a lower raw score.
	for i := 0; i < MinRunsForScore; i++ {
		conclusion := "success"
		if i == 2 {
			conclusion = "failure"
		}
		runs = append(runs, completed("solid", conclusion, day.Add(time.Duration(i+2)*time.Hour)))
	}

	result := Analyze(runs, Options{ZombieHours: 6, Now: day.Add(48 * time.Hour)})

	if result.Workflows[0].Name != "solid" {
		t.Errorf("expected the confidently-scored workflow first, got %q", result.Workflows[0].Name)
	}
}

