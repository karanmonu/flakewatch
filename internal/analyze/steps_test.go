package analyze

import (
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

func mkstep(name string, start time.Time, d time.Duration) gh.Step {
	return gh.Step{Name: name, StartedAt: start, CompletedAt: start.Add(d)}
}

func jobWithSteps(labels []string, start time.Time, d time.Duration, steps ...gh.Step) gh.Job {
	j := mkjob(labels, start, d)
	j.Steps = steps
	return j
}

func TestStepCostsAreProRataNotRoundedUp(t *testing.T) {
	// The trap. Cost elsewhere in this package rounds each *job* up to a whole
	// minute because that is how GitHub bills. Applying the same rounding per
	// step would charge a whole minute for a four-second step, and a job with
	// twelve short steps would report twelve minutes against a job billed one.
	// Steps are not a billing unit, so they get a share, not a rounding.
	runs := []gh.WorkflowRun{{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {jobWithSteps([]string{"ubuntu-latest"}, epoch, time.Minute,
			mkstep("Build", epoch, 30*time.Second),
			mkstep("Test", epoch.Add(30*time.Second), 30*time.Second),
		)},
	}}

	got := findStepCosts(runs, jobs, 0, nil)
	if len(got) != 2 {
		t.Fatalf("want two steps, got %+v", got)
	}
	// Half a minute at $0.006/min is $0.003, and the two together come to the
	// job's one billed minute exactly because this job happens to be exactly a
	// minute long.
	for _, s := range got {
		approx(t, s.USD, 0.003)
	}
}

func TestStepCostsSumToNoMoreThanTheJob(t *testing.T) {
	// A job billed one minute (rounded up from ten seconds) with five steps.
	// The steps must not add up to more than the job cost -- a drill-down that
	// exceeds the thing it drills into is worse than no drill-down.
	var steps []gh.Step
	at := epoch
	for i := 0; i < 5; i++ {
		steps = append(steps, mkstep("step", at, 2*time.Second))
		at = at.Add(2 * time.Second)
	}
	runs := []gh.WorkflowRun{{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {jobWithSteps([]string{"ubuntu-latest"}, epoch, 10*time.Second, steps...)},
	}}

	jobCost := RunCostUSD(jobs.ByRun[1], nil)
	var total float64
	for _, s := range findStepCosts(runs, jobs, 0, nil) {
		total += s.USD
	}
	if total > jobCost {
		t.Fatalf("steps totalled %v against a job billed %v", total, jobCost)
	}
}

func TestStepCostsCountMatrixLegsSeparately(t *testing.T) {
	// A four-second step is cheap. A four-second step in a twelve-leg matrix,
	// run fifty times, is not, and that is precisely the shape of thing this
	// table exists to surface.
	var legs []gh.Job
	for i := 0; i < 12; i++ {
		legs = append(legs, jobWithSteps([]string{"ubuntu-latest"}, epoch, time.Minute,
			mkstep("Install deps", epoch, 4*time.Second)))
	}
	runs := []gh.WorkflowRun{{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{1: legs}}

	got := findStepCosts(runs, jobs, 0, nil)
	if len(got) != 1 || got[0].Executions != 12 {
		t.Fatalf("want one step with 12 executions, got %+v", got)
	}
	approx(t, got[0].Seconds, 48)
}

func TestSubSecondStepsAreCountedButMeasureZero(t *testing.T) {
	// Step timestamps have no sub-second component, so a step that started and
	// finished in the same second measures as zero. It still ran, and the RAN
	// column says how many times a step ran -- dropping these made that column
	// silently mean "ran for at least a second", which showed up against
	// gohugoio/hugo as "Install Go" 154 and "Post Install Go" 86.
	runs := []gh.WorkflowRun{{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {jobWithSteps([]string{"ubuntu-latest"}, epoch, time.Minute,
			mkstep("Complete job", epoch, 0),
			mkstep("Test", epoch, 30*time.Second),
		)},
	}}

	got := findStepCosts(runs, jobs, 0, nil)

	var complete *StepCost
	for i := range got {
		if got[i].Step == "Complete job" {
			complete = &got[i]
		}
	}
	if complete == nil {
		t.Fatalf("a sub-second step still ran and must still be counted, got %+v", got)
	}
	if complete.Executions != 1 {
		t.Fatalf("want the execution counted, got %d", complete.Executions)
	}
	if complete.Seconds != 0 || complete.USD != 0 {
		t.Fatalf("want no measurable time attributed to it, got %+v", *complete)
	}
}

func TestStepCostsGroupByFileNotDisplayName(t *testing.T) {
	a := gh.WorkflowRun{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"}
	b := gh.WorkflowRun{ID: 2, Name: "CI", Path: ".github/workflows/nightly.yml"}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {jobWithSteps([]string{"ubuntu-latest"}, epoch, time.Minute, mkstep("Build", epoch, 30*time.Second))},
		2: {jobWithSteps([]string{"ubuntu-latest"}, epoch, time.Minute, mkstep("Build", epoch, 30*time.Second))},
	}}

	got := findStepCosts([]gh.WorkflowRun{a, b}, jobs, 0, nil)
	if len(got) != 2 {
		t.Fatalf("two files sharing a name are two workflows, got %+v", got)
	}
}

func TestStepCostsCapTheTable(t *testing.T) {
	var steps []gh.Step
	at := epoch
	for i := 0; i < maxStepCosts+5; i++ {
		steps = append(steps, mkstep(string(rune('a'+i)), at, time.Duration(i+2)*time.Second))
		at = at.Add(time.Minute)
	}
	runs := []gh.WorkflowRun{{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"}}
	jobs := gh.JobsResult{ByRun: map[int64][]gh.Job{
		1: {jobWithSteps([]string{"ubuntu-latest"}, epoch, time.Hour, steps...)},
	}}

	if got := findStepCosts(runs, jobs, 0, nil); len(got) != maxStepCosts {
		t.Fatalf("want the table capped at %d, got %d", maxStepCosts, len(got))
	}
}
