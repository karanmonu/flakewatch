package analyze

import (
	"math"
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

func run(name, conclusion string, start time.Time) gh.WorkflowRun {
	return gh.WorkflowRun{
		Name:         name,
		Status:       "completed",
		Conclusion:   conclusion,
		RunStartedAt: start,
		UpdatedAt:    start.Add(5 * time.Minute),
	}
}

func TestFlakinessScore(t *testing.T) {
	tests := []struct {
		name        string
		runs        int
		transitions int
		failureRate float64
		want        float64
	}{
		{"always passing", 10, 0, 0.0, 0.0},
		{"always failing is broken not flaky", 10, 0, 1.0, 0.0},
		{"perfectly alternating", 10, 9, 0.5, 1.0},
		{"single run", 1, 0, 0.0, 0.0},
		{"rare flake", 10, 2, 0.1, (2.0 / 9.0) * 4 * 0.1 * 0.9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flakinessScore(tt.runs, tt.transitions, tt.failureRate)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("flakinessScore(%d, %d, %v) = %v, want %v",
					tt.runs, tt.transitions, tt.failureRate, got, tt.want)
			}
		})
	}
}

func TestAnalyzeGroupsAndSorts(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		// "ci" alternates: flaky.
		run("ci", "success", base),
		run("ci", "failure", base.Add(1*time.Hour)),
		run("ci", "success", base.Add(2*time.Hour)),
		run("ci", "failure", base.Add(3*time.Hour)),
		// "release" always passes: stable.
		run("release", "success", base),
		run("release", "success", base.Add(1*time.Hour)),
	}

	result := Analyze(runs, Options{ZombieHours: 6, Now: base.Add(24 * time.Hour)})

	if len(result.Workflows) != 2 {
		t.Fatalf("expected 2 workflows, got %d", len(result.Workflows))
	}
	if result.Workflows[0].Name != "ci" {
		t.Errorf("expected flakiest workflow first, got %q", result.Workflows[0].Name)
	}
	if result.Workflows[0].FlakinessScore <= result.Workflows[1].FlakinessScore {
		t.Errorf("expected descending flakiness order")
	}
}

func TestZombieDetection(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{Name: "stuck", Status: "in_progress", RunStartedAt: base, HTMLURL: "https://example.com/1"},
		{Name: "fresh", Status: "in_progress", RunStartedAt: base.Add(23 * time.Hour)},
	}

	result := Analyze(runs, Options{ZombieHours: 6, Now: base.Add(24 * time.Hour)})

	if len(result.Zombies) != 1 {
		t.Fatalf("expected 1 zombie, got %d", len(result.Zombies))
	}
	if result.Zombies[0].Workflow != "stuck" {
		t.Errorf("expected zombie %q, got %q", "stuck", result.Zombies[0].Workflow)
	}
}

// Workflow names collide and get renamed; a pull request diff gives you paths.
// Matching on the path is the only version that survives a rename.
func TestAnalyzeMarksTheWorkflowsTheCallerChanged(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{Name: "CI", Path: ".github/workflows/ci.yml", Status: "completed", Conclusion: "success", RunStartedAt: start},
		{Name: "Release", Path: ".github/workflows/release.yml", Status: "completed", Conclusion: "success", RunStartedAt: start},
	}

	got := Analyze(runs, Options{ChangedPaths: []string{".github/workflows/ci.yml"}})

	for _, s := range got.Workflows {
		want := s.Name == "CI"
		if s.Touched != want {
			t.Errorf("%s: Touched = %v, want %v", s.Name, s.Touched, want)
		}
		if s.Path == "" {
			t.Errorf("%s: Path is empty; the run record carries it", s.Name)
		}
	}
}

// No -changed flag means the caller did not scope the request. Marking
// everything would be the same as marking nothing, but noisier.
func TestAnalyzeMarksNothingWithoutChangedPaths(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{Name: "CI", Path: ".github/workflows/ci.yml", Status: "completed", Conclusion: "success", RunStartedAt: start},
	}

	for _, s := range Analyze(runs, Options{}).Workflows {
		if s.Touched {
			t.Errorf("%s was marked as touched with no ChangedPaths set", s.Name)
		}
	}
}

// A path that matches nothing must not quietly mark the first workflow, and a
// run with no path recorded must not match an empty entry in the changed list.
func TestAnalyzeIgnoresPathsThatMatchNothing(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{Name: "CI", Status: "completed", Conclusion: "success", RunStartedAt: start},
	}

	for _, s := range Analyze(runs, Options{ChangedPaths: []string{".github/workflows/nope.yml", "", "  "}}).Workflows {
		if s.Touched {
			t.Errorf("%s was marked as touched by a path that does not exist", s.Name)
		}
	}
}

// Cost counts every run that burned minutes; flakiness counts only runs that
// concluded success or failure. Reporting one number for both is what made the
// two columns look like they disagreed (#10), so both are carried explicitly.
func TestAnalyzeSeparatesRunsFromScoredRuns(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{Name: "CI", Status: "completed", Conclusion: "success", RunStartedAt: start},
		{Name: "CI", Status: "completed", Conclusion: "failure", RunStartedAt: start.Add(time.Hour)},
		{Name: "CI", Status: "completed", Conclusion: "cancelled", RunStartedAt: start.Add(2 * time.Hour)},
		{Name: "CI", Status: "completed", Conclusion: "skipped", RunStartedAt: start.Add(3 * time.Hour)},
	}

	got := Analyze(runs, Options{})
	if len(got.Workflows) != 1 {
		t.Fatalf("got %d workflows, want 1", len(got.Workflows))
	}
	s := got.Workflows[0]

	if s.Runs != 4 {
		t.Errorf("Runs = %d, want 4 -- cancelled and skipped runs still cost money", s.Runs)
	}
	if s.Scored != 2 {
		t.Errorf("Scored = %d, want 2 -- only success and failure say anything about flakiness", s.Scored)
	}
	if s.FailureRate != 0.5 {
		t.Errorf("FailureRate = %v, want 0.5 (1 of 2 scored, not 1 of 4)", s.FailureRate)
	}
}

// A workflow every one of whose runs was cancelled has real spend and no
// score. It must not vanish from the report, and it must not divide by zero.
func TestAnalyzeKeepsWorkflowsWithNoScoredRuns(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runs := []gh.WorkflowRun{
		{Name: "Cancelled always", Status: "completed", Conclusion: "cancelled", RunStartedAt: start},
		{Name: "Cancelled always", Status: "completed", Conclusion: "cancelled", RunStartedAt: start.Add(time.Hour)},
	}

	got := Analyze(runs, Options{})
	if len(got.Workflows) != 1 {
		t.Fatalf("got %d workflows, want the cancelled one to survive", len(got.Workflows))
	}
	s := got.Workflows[0]
	if s.Runs != 2 || s.Scored != 0 {
		t.Errorf("Runs = %d, Scored = %d; want 2 and 0", s.Runs, s.Scored)
	}
	if s.FailureRate != 0 || s.FlakinessScore != 0 {
		t.Errorf("a workflow with nothing scored must not produce a rate or a score")
	}
	if s.ScoreConfident {
		t.Error("nothing was scored, so no score can be confident")
	}
}

// Two workflow files are two workflows even when they share a display name,
// and one workflow file stays one workflow even when its name changes. Grouping
// by name gets both of those wrong: it merges the first pair into a single row
// whose cost is the sum of two unrelated files, and splits the second into two
// rows each holding half its history.
func TestAnalyzeGroupsByFileNotDisplayName(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	t.Run("same name, different files", func(t *testing.T) {
		runs := []gh.WorkflowRun{
			{Name: "CI", Path: ".github/workflows/ci.yml", Status: "completed", Conclusion: "success", RunStartedAt: start},
			{Name: "CI", Path: ".github/workflows/ci-nightly.yml", Status: "completed", Conclusion: "success", RunStartedAt: start},
		}
		got := Analyze(runs, Options{})
		if len(got.Workflows) != 2 {
			t.Fatalf("got %d workflows, want 2 -- two files are two workflows", len(got.Workflows))
		}
		paths := map[string]bool{}
		for _, s := range got.Workflows {
			paths[s.Path] = true
		}
		if !paths[".github/workflows/ci.yml"] || !paths[".github/workflows/ci-nightly.yml"] {
			t.Errorf("both files should survive as separate rows, got %v", paths)
		}
	})

	t.Run("same file, renamed", func(t *testing.T) {
		runs := []gh.WorkflowRun{
			{Name: "Old name", Path: ".github/workflows/ci.yml", Status: "completed", Conclusion: "success", RunStartedAt: start},
			{Name: "New name", Path: ".github/workflows/ci.yml", Status: "completed", Conclusion: "failure", RunStartedAt: start.Add(time.Hour)},
		}
		got := Analyze(runs, Options{})
		if len(got.Workflows) != 1 {
			t.Fatalf("got %d workflows, want 1 -- a rename is not a new workflow", len(got.Workflows))
		}
		s := got.Workflows[0]
		if s.Runs != 2 || s.Scored != 2 {
			t.Errorf("Runs = %d, Scored = %d; the history should stay together", s.Runs, s.Scored)
		}
		if s.Name != "New name" {
			t.Errorf("Name = %q, want the most recent name", s.Name)
		}
	})
}
