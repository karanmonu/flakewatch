package analyze

import (
	"math"
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/pricing"
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

	got := RunCostUSD(jobs, nil)
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
	approx(t, RunCostUSD(jobs, nil), 0.006+0.062+0.010)
}

// A larger runner costs multiples of the standard one. Reading the label is the
// only way to know, which is why this uses the jobs endpoint and not timing.
func TestRunCostPricesLargerRunners(t *testing.T) {
	standard := RunCostUSD([]gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}, nil)
	larger := RunCostUSD([]gh.Job{mkjob([]string{"ubuntu-latest-16-core"}, epoch, time.Minute)}, nil)

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
	approx(t, RunCostUSD(jobs, nil), 0.006)
}

func TestJobDurationHandlesUnfinishedJobs(t *testing.T) {
	unfinished := gh.Job{Labels: []string{"ubuntu-latest"}, StartedAt: epoch}
	if d := unfinished.DurationMS(); d != 0 {
		t.Errorf("job with no completion time should have zero duration, got %d", d)
	}
	if c := RunCostUSD([]gh.Job{unfinished}, nil); c != 0 {
		t.Errorf("unfinished job should cost nothing, got %v", c)
	}
}

func TestSummarizeCostAttributesPerWorkflowAndExtrapolates(t *testing.T) {
	oneMinute := []gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}

	runs := []gh.WorkflowRun{
		{ID: 1, Name: "ci", RunStartedAt: epoch},
		{ID: 2, Name: "ci", RunStartedAt: epoch.Add(10 * 24 * time.Hour)},
		{ID: 3, Name: "release", RunStartedAt: epoch.Add(2 * 24 * time.Hour)},
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: oneMinute, 2: oneMinute, 3: oneMinute}}
	stats := []WorkflowStats{{Name: "ci"}, {Name: "release"}}

	summary := SummarizeCost(runs, jobs, stats, nil)

	approx(t, stats[0].CostUSD, 2*0.006)
	approx(t, stats[1].CostUSD, 0.006)
	approx(t, summary.TotalUSD, 3*0.006)
	if summary.RunsPriced != 3 {
		t.Errorf("RunsPriced = %d, want 3", summary.RunsPriced)
	}
	// A 10-day window scales 3x to reach 30 days. It has to clear the one-week
	// floor, below which no monthly figure is offered at all.
	approx(t, summary.MonthlyUSD, 3*0.006*3)
}

func TestSummarizeCostDoesNotExtrapolateShortWindows(t *testing.T) {
	oneMinute := []gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}
	runs := []gh.WorkflowRun{
		{ID: 1, Name: "ci", RunStartedAt: epoch},
		{ID: 2, Name: "ci", RunStartedAt: epoch.Add(time.Hour)},
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: oneMinute, 2: oneMinute}}

	summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}}, nil)

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

	summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}}, nil)

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

	summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}}, nil)

	if summary.RunsSkippedForBudget != 160 {
		t.Errorf("RunsSkippedForBudget = %d, want 160", summary.RunsSkippedForBudget)
	}
}

// CI load is weekly-periodic, so a window shorter than a full cycle cannot be
// scaled to a month honestly. Surveying public repositories showed the old
// 24-hour floor turning 1.8 weekdays of golangci-lint into "$143/month".
func TestSummarizeCostRefusesToProjectFromLessThanAWeek(t *testing.T) {
	tests := []struct {
		name        string
		span        time.Duration
		wantMonthly bool
	}{
		{"under two days", 42 * time.Hour, false},
		{"just under a week", 6*24*time.Hour - time.Hour, false},
		{"a full week", 7 * 24 * time.Hour, true},
		{"a month", 30 * 24 * time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := []gh.WorkflowRun{
				{ID: 1, Name: "ci", RunStartedAt: epoch},
				{ID: 2, Name: "ci", RunStartedAt: epoch.Add(tt.span)},
			}
			oneMinute := []gh.Job{mkjob([]string{"ubuntu-latest"}, epoch, time.Minute)}
			jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: oneMinute, 2: oneMinute}}

			summary := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}}, nil)

			if got := summary.MonthlyUSD > 0; got != tt.wantMonthly {
				t.Errorf("MonthlyUSD = %v over %v; wanted a projection: %v",
					summary.MonthlyUSD, tt.span, tt.wantMonthly)
			}
			if summary.TotalUSD == 0 {
				t.Error("the measured total must be reported regardless")
			}
		})
	}
}

// The undercount this fixes is not hypothetical: the survey of eight public
// repositories hit it once, and the repositories most likely to hit it are
// private ones on larger or self-hosted runners -- exactly the ones paying a
// bill. A total that silently drops those jobs is wrong for the only audience
// that has a reason to care.
func TestSummarizeCostPricesUserSuppliedRunners(t *testing.T) {
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", Path: ".github/workflows/ci.yml", RunStartedAt: epoch}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {
			mkjob([]string{"ubuntu-latest"}, epoch, time.Minute),
			mkjob([]string{"self-hosted", "gpu-large"}, epoch, 2*time.Minute),
		},
	}}

	without := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}}, nil)
	if without.SelfHostedJobs != 1 {
		t.Fatalf("want the self-hosted job counted as excluded, got %d", without.SelfHostedJobs)
	}
	approx(t, without.TotalUSD, 0.006)

	with := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}},
		pricing.Overrides{"gpu-large": 0.42})

	approx(t, with.TotalUSD, 0.006+2*0.42)
	if with.SelfHostedJobs != 0 {
		t.Fatalf("a priced job must stop being reported as excluded, got %d", with.SelfHostedJobs)
	}
	if with.UserPricedJobs != 1 {
		t.Fatalf("want 1 user-priced job, got %d", with.UserPricedJobs)
	}
	if len(with.UserSuppliedLabels) != 1 || with.UserSuppliedLabels[0] != "gpu-large" {
		t.Fatalf("want the label named in the summary, got %v", with.UserSuppliedLabels)
	}
}

// An unrecognised GitHub-hosted label is the other half of the same gap, and
// the one the survey actually hit: github-hosted-windows-x64-large.
func TestSummarizeCostClearsUnknownLabelsOncePriced(t *testing.T) {
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", Path: ".github/workflows/ci.yml", RunStartedAt: epoch}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"github-hosted-windows-x64-large"}, epoch, time.Minute)},
	}}

	before := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}}, nil)
	if before.UnknownRunnerJobs != 1 || before.TotalUSD != 0 {
		t.Fatalf("want an unpriced job and a zero total, got %+v", before)
	}

	after := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}},
		pricing.Overrides{"github-hosted-windows-x64-large": 0.064})
	if after.UnknownRunnerJobs != 0 || len(after.UnknownLabels) != 0 {
		t.Fatalf("the undercount warning must go away once the label is priced, got %+v", after)
	}
	approx(t, after.TotalUSD, 0.064)
}

// The platform table claims "these same minutes on a standard Linux runner
// would cost this instead". That swap is only meaningful between GitHub-hosted
// SKUs; a self-hosted macOS build machine is not something you move to
// ubuntu-latest, and pricing the move would invent a saving.
func TestUserPricedJobsStayOutOfThePlatformTable(t *testing.T) {
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", Path: ".github/workflows/ci.yml", RunStartedAt: epoch}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"self-hosted", "macos-m2-rack"}, epoch, 10*time.Minute)},
	}}

	s := SummarizeCost(runs, jobs, []WorkflowStats{{Name: "ci"}},
		pricing.Overrides{"macos-m2-rack": 0.30})

	approx(t, s.TotalUSD, 10*0.30)
	if len(s.Opportunities) != 0 {
		t.Fatalf("want no Linux counterfactual for a user-priced runner, got %+v", s.Opportunities)
	}
}
