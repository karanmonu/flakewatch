package analyze

import (
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

// mkRun builds a completed run n minutes after a fixed epoch.
func mkRun(id int64, path, name, conclusion string, minute int) gh.WorkflowRun {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(minute) * time.Minute)
	return gh.WorkflowRun{
		ID: id, Name: name, Path: path,
		Status: "completed", Conclusion: conclusion,
		RunStartedAt: start, UpdatedAt: start.Add(5 * time.Minute),
	}
}

func mkJob(runID int64, name, conclusion string) gh.Job {
	return gh.Job{RunID: runID, Name: name, Conclusion: conclusion}
}

// The dilution case from issue #1: a matrix with one alternating leg and one
// stable leg. The workflow-level score is damped because most runs pass, but
// the flaky leg alternates every single execution and must surface with a
// higher score than its workflow.
func TestAnalyzeJobsSurfacesDilutedFlakyLeg(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	var runs []gh.WorkflowRun
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{}}

	// 8 runs; "Test (windows)" alternates pass/fail, "Test (linux)" always
	// passes. The run's own conclusion mirrors the windows leg.
	for i := 0; i < 8; i++ {
		conclusion := "success"
		winConclusion := "success"
		if i%2 == 1 {
			conclusion = "failure"
			winConclusion = "failure"
		}
		id := int64(100 + i)
		runs = append(runs, mkRun(id, wf, "CI", conclusion, i))
		jobs.ByRun[id] = []gh.Job{
			mkJob(id, "Test (linux)", "success"),
			mkJob(id, "Test (windows)", winConclusion),
		}
	}

	res := Analyze(runs, Options{})
	got := AnalyzeJobs(runs, jobs, res.Workflows)

	if len(got) != 2 {
		t.Fatalf("want 2 job rows, got %d: %+v", len(got), got)
	}

	win := got[0]
	if win.Name != "Test (windows)" {
		t.Fatalf("flakiest job should sort first, got %q", win.Name)
	}
	if !win.ScoreConfident {
		t.Errorf("8 executions should be confident")
	}
	if win.Scored != 8 || win.Failures != 4 || win.Transitions != 7 {
		t.Errorf("windows leg: scored=%d failures=%d transitions=%d, want 8/4/7",
			win.Scored, win.Failures, win.Transitions)
	}
	if win.FlakinessScore <= 0.9 {
		t.Errorf("alternating leg should score near 1.0, got %f", win.FlakinessScore)
	}
	if win.WorkflowScore <= 0 {
		t.Errorf("workflow score should be attached for comparison, got %f", win.WorkflowScore)
	}

	linux := got[1]
	if linux.Name != "Test (linux)" || linux.FlakinessScore != 0 {
		t.Errorf("stable leg should score 0, got %q %f", linux.Name, linux.FlakinessScore)
	}
}

// Skipped and cancelled executions are no evidence, and a single scored
// execution cannot transition; neither may produce a row that pretends
// otherwise.
func TestAnalyzeJobsIgnoresNonEvidence(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	runs := []gh.WorkflowRun{
		mkRun(1, wf, "CI", "success", 0),
		mkRun(2, wf, "CI", "success", 1),
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkJob(1, "lint", "success"), mkJob(1, "deploy", "skipped"), mkJob(1, "once", "success")},
		2: {mkJob(2, "lint", "success"), mkJob(2, "deploy", "cancelled")},
	}}

	res := Analyze(runs, Options{})
	got := AnalyzeJobs(runs, jobs, res.Workflows)

	if len(got) != 1 {
		t.Fatalf("want only the twice-scored lint job, got %d rows: %+v", len(got), got)
	}
	if got[0].Name != "lint" || got[0].Scored != 2 {
		t.Errorf("got %q scored=%d, want lint scored=2", got[0].Name, got[0].Scored)
	}
	if got[0].ScoreConfident {
		t.Errorf("2 executions must not be confident (min is %d)", MinRunsForScore)
	}
}

// Jobs from cached runs outside the current sample must not leak into the
// score: the report describes the window it claims to describe.
func TestAnalyzeJobsIgnoresRunsOutsideSample(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	runs := []gh.WorkflowRun{
		mkRun(1, wf, "CI", "success", 0),
		mkRun(2, wf, "CI", "failure", 1),
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkJob(1, "test", "success")},
		2: {mkJob(2, "test", "failure")},
		// Run 99 is in the cache but not in this window's run list.
		99: {mkJob(99, "test", "failure")},
	}}

	res := Analyze(runs, Options{})
	got := AnalyzeJobs(runs, jobs, res.Workflows)

	if len(got) != 1 || got[0].Scored != 2 {
		t.Fatalf("cached out-of-window run leaked into the sample: %+v", got)
	}
}

// Chronology comes from the run clock, not map iteration order: the same data
// must produce the same transition count every time.
func TestAnalyzeJobsDeterministicOrdering(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	var runs []gh.WorkflowRun
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{}}
	// fail, pass, fail, pass, fail: 4 transitions -- but only when ordered.
	concls := []string{"failure", "success", "failure", "success", "failure"}
	for i, c := range concls {
		id := int64(10 + i)
		runs = append(runs, mkRun(id, wf, "CI", c, i))
		jobs.ByRun[id] = []gh.Job{mkJob(id, "test", c)}
	}

	res := Analyze(runs, Options{})
	for i := 0; i < 20; i++ {
		got := AnalyzeJobs(runs, jobs, res.Workflows)
		if len(got) != 1 || got[0].Transitions != 4 {
			t.Fatalf("iteration %d: transitions=%d, want 4 (ordering unstable?)", i, got[0].Transitions)
		}
	}
}
