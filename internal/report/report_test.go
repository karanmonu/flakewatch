package report

import (
	"strings"
	"testing"

	"github.com/karanmonu/flakewatch/internal/analyze"
)

// A workflow that fails every time scores zero for flakiness, because
// consistently broken is not flaky -- that is what 4p(1-p) is for. But zero
// fell through to "stable", so a workflow failing 100% of the time was
// reported with a green dot and the word stable. Found by running against
// gohugoio/hugo, where "Close stale and lock closed issues" had failed all 13
// times in the window and the report called it healthy.
func TestAlwaysFailingIsNotReportedAsStable(t *testing.T) {
	broken := analyze.WorkflowStats{
		Name:           "Close stale",
		Runs:           13,
		Scored:         13,
		FailureRate:    1.0,
		FlakinessScore: 0,
		ScoreConfident: true,
	}
	if got := badge(broken); !strings.Contains(got, "always failing") {
		t.Fatalf("a workflow failing every run must not read as healthy, got %q", got)
	}

	healthy := analyze.WorkflowStats{
		Name:           "CI",
		Runs:           13,
		Scored:         13,
		FailureRate:    0,
		FlakinessScore: 0,
		ScoreConfident: true,
	}
	if got := badge(healthy); !strings.Contains(got, "stable") {
		t.Fatalf("a workflow that never fails is stable, got %q", got)
	}
}

func TestTerminalWarnsOnDurationRegression(t *testing.T) {
	r := analyze.Result{Workflows: []analyze.WorkflowStats{{
		Name:               "CI",
		Runs:               10,
		Scored:             10,
		ScoreConfident:     true,
		PreviousMedianSec:  100,
		RecentMedianSec:    130,
		DurationRatio:      1.3,
		DurationRegression: true,
	}}}
	var b strings.Builder
	WriteTerminal(&b, "owner/repo", r, false)
	out := b.String()
	for _, want := range []string{"WARN", "CI", "130", "100", "+30%", "most recent 5 scored"} {
		if !strings.Contains(out, want) {
			t.Errorf("duration regression output should contain %q; got:\n%s", want, out)
		}
	}
}

func TestJSONIncludesDurationRegressionMeasurements(t *testing.T) {
	r := analyze.Result{Workflows: []analyze.WorkflowStats{{
		Name:               "CI",
		PreviousMedianSec:  100,
		RecentMedianSec:    130,
		DurationRatio:      1.3,
		DurationRegression: true,
	}}}
	var b strings.Builder
	if err := WriteJSON(&b, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"previous_median_sec": 100`, `"recent_median_sec": 130`, `"duration_ratio": 1.3`, `"duration_regression": true`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("JSON should contain %s; got:\n%s", want, b.String())
		}
	}
}

// The flaky-jobs section exists to show dilution. A job merely matching its
// workflow's score is the same finding twice and must not print; a job well
// above it must; and a section with nothing to say must say nothing.
func TestWriteFlakyJobsShowsOnlyDilution(t *testing.T) {
	var b strings.Builder
	diluted := analyze.JobStats{
		Workflow: "CI", Name: "Test (windows)",
		Scored: 8, Failures: 4, FailureRate: 0.5,
		FlakinessScore: 0.9, WorkflowScore: 0.2, ScoreConfident: true,
	}
	tracking := analyze.JobStats{
		Workflow: "CI", Name: "Test (linux)",
		Scored: 8, FailureRate: 0.5,
		FlakinessScore: 0.2, WorkflowScore: 0.2, ScoreConfident: true,
	}
	writeFlakyJobs(&b, []analyze.JobStats{diluted, tracking})
	out := b.String()
	if !strings.Contains(out, "Test (windows)") {
		t.Errorf("diluted flaky job must be shown, got:\n%s", out)
	}
	if strings.Contains(out, "Test (linux)") {
		t.Errorf("job tracking its workflow's score must not be shown, got:\n%s", out)
	}

	b.Reset()
	writeFlakyJobs(&b, nil)
	if b.Len() != 0 {
		t.Errorf("no jobs must mean no section, got:\n%s", b.String())
	}

	// An unconfident score never earns a row, however dramatic it looks.
	b.Reset()
	thin := diluted
	thin.ScoreConfident = false
	writeFlakyJobs(&b, []analyze.JobStats{thin})
	if b.Len() != 0 {
		t.Errorf("unconfident scores must not print, got:\n%s", b.String())
	}
}
