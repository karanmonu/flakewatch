package analyze

import (
	"math"
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func mkjob(labels []string, start time.Time, d time.Duration) gh.Job {
	return gh.Job{
		Labels:      labels,
		StartedAt:   start,
		CompletedAt: start.Add(d),
	}
}

var epoch = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

// The whole reason this package exists rather than summing durations and
// dividing by 60.
func TestRunCostRoundsEachJobUpNotTheTotal(t *testing.T) {
	// Ten 30-second Ubuntu jobs. Summing first: 300s = 5 billable minutes.
	// GitHub's way: each job rounds up to 1 minute = 10 billable minutes.
	jobs := make([]gh.Job, 10)
	for i := range jobs {
		jobs[i] = mkjob([]string{"ubuntu-latest"}, epoch, 30*time.Second)
	}

	got := RunCostUSD(jobs)
	approx(t, got, 10*0.006)

	if math.Abs(got-5*0.006) < 1e-9 {
		t.Error("cost matches the sum-then-round calculation; per-job rounding is not being applied")
	}
}

func TestRunCostUsesRunnerLabels(t *testing.T) {
	jobs := []gh.Job{
		mkjob([]string{"ubuntu-latest"}, epoch, time.Minute),
		mkjob([]string{"macos-latest"}, epoch, time.Minute),
		mkjob([]string{"windows-latest"}, epoch, time.Minute),
	}
	approx(t, RunCostUSD(jobs), 0.006+0.062+0.010)
}

// A larger runner costs multiples of the standard one. Reading the label is the
// only way to know, which is why this uses the jobs endpoint and not timing.
func TestRunCostPricesLargerRunners(t *testing.T) {
	standard := RunCostUSD([]gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)})
	larger := RunCostUSD([]gh.Job{mkjob([]string{"ubuntu-latest-16-core"}, epoch, time.Minute)})

	if larger <= standard {
		t.Fatalf("16-core (%v) should cost more than standard (%v)", larger, standard)
	}
	approx(t, larger, 0.042)
}

func TestRunCostSkipsSelfHostedAndUnknownRunners(t *testing.T) {
	jobs := []gh.Job{
		mkjob([]string{"ubuntu-latest"}, epoch, time.Minute),
		mkjob([]string{"self-hosted", "linux"}, epoch, 60*time.Minute),
		mkjob([]string{"some-custom-pool"}, epoch, 60*time.Minute),
	}
	approx(t, RunCostUSD(jobs), 0.006)
}

func TestJobDurationHandlesUnfinishedJobs(t *testing.T) {
	unfinished := gh.Job{Labels: []string{"ubuntu-latest"}, StartedAt: epoch}
	if d := unfinished.DurationMS(); d != 0 {
		t.Errorf("job with no completion time should have zero duration, got %d", d)
	}
	if c := RunCostUSD([]gh.Job{unfinished}); c != 0 {
		t.Errorf("unfinished job should cost nothing, got %v", c)
	}
}

func TestSummarizeCostAttributesPerWorkflowAndExtrapolates(t *testing.T) {
	oneMinute := []gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}

	runs := []gh.WorkflowRun{
		{ID: 1, Name: "ci", RunStartedAt: epoch},
		{ID: 2, Name: "ci", RunStartedAt: epoch.Add(5 * 24 * time.Hour)},
		{ID: 3, Name: "release", RunStartedAt: epoch.Add(2 * 24 * time.Hour)},
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: oneMinute, 2: oneMinute, 3: oneMinute}}
	stats := []WorkflowStats{{Name: "ci"}, {Name: "release"}}

	summary := SummarizeCost(runs, jobs, stats)

	approx(t, stats[0].CostUSD, 2*0.006)
	approx(t, stats[1].CostUSD, 0.006)
	approx(t, summary.TotalUSD, 3*0.006)
	if summary.RunsPriced != 3 {
		t.Errorf("RunsPriced = %d, want 3", summary.RunsPriced)
	}
	// A 5-day window scales 6x to reach 30 days.
	approx(t, summary.MonthlyUSD, 3*0.006*6)
}

func TestSummarizeCostDoesNotExtrapolateShortWindows(t *testing.T) {
	oneMinute := []gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}
	runs := []gh.WorkflowRun{
		{ID: 1, Name: "ci", RunStartedAt: epoch},
		{ID: 2, Name: "ci", RunStartedAt: epoch.Add(time.Hour)},
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: oneMinute, 2: oneMinute}}

	summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}})

	if summary.MonthlyUSD != 0 {
		t.Errorf("MonthlyUSD = %v; a one-hour window must not be scaled to a month", summary.MonthlyUSD)
	}
	if summary.TotalUSD == 0 {
		t.Error("TotalUSD should still be reported for short windows")
	}
}

func TestSummarizeCostCountsSkippedJobs(t *testing.T) {
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", RunStartedAt: epoch}}
	jobs := gh.JobsResult{
		ByRun: map[int64][]gh.Job{1: {
			mkjob([]string{"ubuntu-latest"}, epoch, time.Minute),
			mkjob([]string{"self-hosted"}, epoch, time.Minute),
			mkjob([]string{"mystery-pool"}, epoch, time.Minute),
		}},
		Missing: 2,
	}

	summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}})

	if summary.SelfHostedJobs != 1 {
		t.Errorf("SelfHostedJobs = %d, want 1", summary.SelfHostedJobs)
	}
	if summary.UnknownRunnerJobs != 1 {
		t.Errorf("UnknownRunnerJobs = %d, want 1", summary.UnknownRunnerJobs)
	}
	if summary.RunsMissingJobs != 2 {
		t.Errorf("RunsMissingJobs = %d, want 2", summary.RunsMissingJobs)
	}
}

// A truncated sample has to reach the report. Silently analyzing 40 runs when
// the caller asked for 200 is the kind of thing that makes a number wrong in a
// way nobody can see.
func TestSummarizeCostCarriesTheBudgetShortfall(t *testing.T) {
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", RunStartedAt: epoch}}
	jobs := gh.JobsResult{
		ByRun:            map[int64][]gh.Job{1: {mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}},
		SkippedForBudget: 160,
	}

	summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}})

	if summary.RunsSkippedForBudget != 160 {
		t.Errorf("RunsSkippedForBudget = %d, want 160", summary.RunsSkippedForBudget)
	}
}
