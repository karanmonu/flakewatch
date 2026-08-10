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
