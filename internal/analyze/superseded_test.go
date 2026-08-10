package analyze

import (
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

// prRun builds a pull request run on a branch, started at t.
func prRun(id int64, branch string, start time.Time, conclusion string) gh.WorkflowRun {
	return gh.WorkflowRun{
		ID:           id,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		Event:        "pull_request",
		HeadBranch:   branch,
		Status:       "completed",
		Conclusion:   conclusion,
		RunStartedAt: start,
		HTMLURL:      "https://github.com/o/r/actions/runs/1",
	}
}

func TestSupersededPricesOnlyTheMinutesAfterTheNewerRunStarted(t *testing.T) {
	// Run 1 starts at 0 and runs for ten minutes. Run 2 starts at minute four.
	// A concurrency group would have killed run 1 at minute four, so six
	// minutes were wasted -- not ten. Reporting ten would be the bigger, more
	// impressive number and it would be a lie: minutes zero to four were
	// buying a result that was still wanted when they were spent.
	runs := []gh.WorkflowRun{
		prRun(1, "feature", epoch, "success"),
		prRun(2, "feature", epoch.Add(4*time.Minute), "success"),
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"ubuntu-latest"}, epoch, 10*time.Minute)},
		2: {mkjob([]string{"ubuntu-latest"}, epoch.Add(4*time.Minute), 10*time.Minute)},
	}}

	got := findSuperseded(runs, jobs, 0, nil)
	if len(got) != 1 {
		t.Fatalf("want one workflow reported, got %+v", got)
	}
	if got[0].Runs != 1 {
		t.Fatalf("want 1 superseded run, got %d", got[0].Runs)
	}
	if got[0].WastedMinutes != 6 {
		t.Fatalf("want 6 wasted minutes, got %d", got[0].WastedMinutes)
	}
	approx(t, got[0].WastedUSD, 6*0.006)
}

func TestSupersededIgnoresRunsThatFinishedFirst(t *testing.T) {
	// Two pushes an hour apart. The first run was long finished, so nothing
	// was wasted and a concurrency group would have changed nothing.
	runs := []gh.WorkflowRun{
		prRun(1, "feature", epoch, "success"),
		prRun(2, "feature", epoch.Add(time.Hour), "success"),
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"ubuntu-latest"}, epoch, 2*time.Minute)},
		2: {mkjob([]string{"ubuntu-latest"}, epoch.Add(time.Hour), 2*time.Minute)},
	}}

	if got := findSuperseded(runs, jobs, 0, nil); len(got) != 0 {
		t.Fatalf("want nothing reported, got %+v", got)
	}
}

func TestSupersededIgnoresCancelledRuns(t *testing.T) {
	// A cancelled run is the outcome this would have recommended. Counting it
	// as waste would tell someone who already has a concurrency group that
	// their concurrency group is costing them money.
	runs := []gh.WorkflowRun{
		prRun(1, "feature", epoch, "cancelled"),
		prRun(2, "feature", epoch.Add(1*time.Minute), "success"),
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"ubuntu-latest"}, epoch, 10*time.Minute)},
		2: {mkjob([]string{"ubuntu-latest"}, epoch.Add(time.Minute), 10*time.Minute)},
	}}

	if got := findSuperseded(runs, jobs, 0, nil); len(got) != 0 {
		t.Fatalf("cancelled runs are not waste, got %+v", got)
	}
}

func TestSupersededIgnoresPushesToTheSameBranch(t *testing.T) {
	// Two overlapping runs on the default branch are one build per commit,
	// which is how you find out which commit broke something. Flagging that
	// would be wrong on nearly every repository that exists, and a tool that
	// is wrong about main gets its whole output ignored.
	push := func(id int64, start time.Time) gh.WorkflowRun {
		r := prRun(id, "main", start, "success")
		r.Event = "push"
		return r
	}
	runs := []gh.WorkflowRun{push(1, epoch), push(2, epoch.Add(time.Minute))}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"ubuntu-latest"}, epoch, 10*time.Minute)},
		2: {mkjob([]string{"ubuntu-latest"}, epoch.Add(time.Minute), 10*time.Minute)},
	}}

	if got := findSuperseded(runs, jobs, 0, nil); len(got) != 0 {
		t.Fatalf("push events are out of scope, got %+v", got)
	}
}

func TestSupersededDoesNotCrossBranches(t *testing.T) {
	// Two people working at once is not one person superseding themselves.
	runs := []gh.WorkflowRun{
		prRun(1, "alice", epoch, "success"),
		prRun(2, "bob", epoch.Add(time.Minute), "success"),
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"ubuntu-latest"}, epoch, 10*time.Minute)},
		2: {mkjob([]string{"ubuntu-latest"}, epoch.Add(time.Minute), 10*time.Minute)},
	}}

	if got := findSuperseded(runs, jobs, 0, nil); len(got) != 0 {
		t.Fatalf("different branches are unrelated, got %+v", got)
	}
}

func TestSupersededRoundsEachJobSeparately(t *testing.T) {
	// Billing rounds per job, so a fan-out of short jobs wastes far more than
	// the wall clock suggests. Four jobs each overlapping by ten seconds bill
	// four minutes, not one.
	runs := []gh.WorkflowRun{
		prRun(1, "feature", epoch, "success"),
		prRun(2, "feature", epoch.Add(50*time.Second), "success"),
	}
	var fanout []gh.Job
	for i := 0; i < 4; i++ {
		fanout = append(fanout, mkjob([]string{"ubuntu-latest"}, epoch, time.Minute))
	}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: fanout}}

	got := findSuperseded(runs, jobs, 0, nil)
	if len(got) != 1 || got[0].WastedMinutes != 4 {
		t.Fatalf("want 4 billable minutes across 4 jobs, got %+v", got)
	}
}

func TestSupersededGroupsByFileNotDisplayName(t *testing.T) {
	// Same identity rule as the rest of the package: two files both called
	// "CI" are two workflows, and their waste must not be added together.
	a := prRun(1, "feature", epoch, "success")
	b := prRun(2, "feature", epoch.Add(time.Minute), "success")
	c := prRun(3, "feature", epoch, "success")
	c.Path = ".github/workflows/nightly.yml"
	d := prRun(4, "feature", epoch.Add(time.Minute), "success")
	d.Path = ".github/workflows/nightly.yml"

	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {mkjob([]string{"ubuntu-latest"}, epoch, 10*time.Minute)},
		3: {mkjob([]string{"ubuntu-latest"}, epoch, 10*time.Minute)},
	}}

	got := findSuperseded([]gh.WorkflowRun{a, b, c, d}, jobs, 0, nil)
	if len(got) != 2 {
		t.Fatalf("want two separate workflows, got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Runs != 1 {
			t.Fatalf("waste from two files was merged: %+v", got)
		}
	}
}
