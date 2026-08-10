package analyze

import (
	"math"
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

func TestOpportunitiesPriceTheLinuxCounterfactual(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{{ID: 1, Name: "build", RunStartedAt: start}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: {
		{Labels: []string{"macos-latest"}, StartedAt: start, CompletedAt: start.Add(10 * time.Minute)},
		{Labels: []string{"ubuntu-latest"}, StartedAt: start, CompletedAt: start.Add(10 * time.Minute)},
	}}}

	opps := findOpportunities(runs, jobs, 0)

	if len(opps) != 1 {
		t.Fatalf("expected only the macOS spend to be reported, got %d entries", len(opps))
	}
	o := opps[0]
	if o.Platform != "macos" {
		t.Errorf("platform = %q, want macos", o.Platform)
	}
	// 10 minutes of macOS at 0.062 against the same minutes on Linux at 0.006.
	if math.Abs(o.CurrentUSD-10*0.062) > 1e-9 {
		t.Errorf("CurrentUSD = %v, want %v", o.CurrentUSD, 10*0.062)
	}
	if math.Abs(o.OnLinuxUSD-10*0.006) > 1e-9 {
		t.Errorf("OnLinuxUSD = %v, want %v", o.OnLinuxUSD, 10*0.006)
	}
	if math.Abs(o.DeltaUSD-(10*0.062-10*0.006)) > 1e-9 {
		t.Errorf("DeltaUSD = %v", o.DeltaUSD)
	}
}

// Linux spend is not an opportunity; reporting it would be noise on repos that
// already do the cheap thing.
func TestLinuxOnlyRepoHasNoOpportunities(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", RunStartedAt: start}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: {
		{Labels: []string{"ubuntu-latest"}, StartedAt: start, CompletedAt: start.Add(time.Hour)},
	}}}

	if opps := findOpportunities(runs, jobs, 0); len(opps) != 0 {
		t.Errorf("expected no opportunities for a Linux-only repo, got %d", len(opps))
	}
}

// Unpriceable jobs must not silently become a $0 "opportunity", which would
// read as a saving that does not exist.
func TestUnknownAndSelfHostedJobsAreNotOpportunities(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", RunStartedAt: start}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: {
		{Labels: []string{"github-hosted-windows-x64-large"}, StartedAt: start, CompletedAt: start.Add(time.Hour)},
		{Labels: []string{"self-hosted", "windows"}, StartedAt: start, CompletedAt: start.Add(time.Hour)},
	}}}

	if opps := findOpportunities(runs, jobs, 0); len(opps) != 0 {
		t.Errorf("expected no opportunities from unpriceable jobs, got %+v", opps)
	}
}

func TestOpportunitiesSortByLargestDifference(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{ID: 1, Name: "small", RunStartedAt: start},
		{ID: 2, Name: "large", RunStartedAt: start},
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {{Labels: []string{"macos-latest"}, StartedAt: start, CompletedAt: start.Add(time.Minute)}},
		2: {{Labels: []string{"macos-latest"}, StartedAt: start, CompletedAt: start.Add(30 * time.Minute)}},
	}}

	opps := findOpportunities(runs, jobs, 0)

	if len(opps) != 2 {
		t.Fatalf("expected 2 opportunities, got %d", len(opps))
	}
	if opps[0].Workflow != "large" {
		t.Errorf("expected the costliest workflow first, got %q", opps[0].Workflow)
	}
}

func TestMonthlyDeltaUsesTheSuppliedFactor(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{{ID: 1, Name: "ci", RunStartedAt: start}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: {
		{Labels: []string{"macos-latest"}, StartedAt: start, CompletedAt: start.Add(time.Minute)},
	}}}

	// A zero factor means the window was too short to extrapolate; no monthly
	// figure should be invented.
	if opps := findOpportunities(runs, jobs, 0); opps[0].MonthlyDeltaUSD != 0 {
		t.Errorf("MonthlyDeltaUSD = %v, want 0 for an unextrapolatable window", opps[0].MonthlyDeltaUSD)
	}

	opps := findOpportunities(runs, jobs, 10)
	if math.Abs(opps[0].MonthlyDeltaUSD-opps[0].DeltaUSD*10) > 1e-9 {
		t.Errorf("MonthlyDeltaUSD = %v, want %v", opps[0].MonthlyDeltaUSD, opps[0].DeltaUSD*10)
	}
}
