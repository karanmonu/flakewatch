// Package analyze computes flakiness scores and zombie-run detection from
// GitHub Actions workflow run history.
package analyze

import (
	"sort"
	"strings"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

// Options configures the analysis.
type Options struct {
	// ZombieHours: a run still in progress after this many hours is a zombie.
	ZombieHours float64
	// Now allows tests to pin the clock; zero value means time.Now().
	Now time.Time
	// ChangedPaths are workflow files the caller cares about, in repository-root
	// form (".github/workflows/ci.yml"). Used by the Action to mark the
	// workflows a pull request actually touches. Empty means "all of them".
	ChangedPaths []string
}

// WorkflowStats summarizes one workflow's recent history.
type WorkflowStats struct {
	Name           string  `json:"name"`
	Runs           int     `json:"runs"`
	Failures       int     `json:"failures"`
	FailureRate    float64 `json:"failure_rate"`
	Transitions    int     `json:"transitions"` // pass<->fail flips in chronological order
	FlakinessScore float64 `json:"flakiness_score"`
	AvgDurationSec float64 `json:"avg_duration_sec"`
	CostUSD        float64 `json:"cost_usd"` // populated only when -cost is used
	// ScoreConfident reports whether there were enough runs for the flakiness
	// score to mean anything. See MinRunsForScore.
	ScoreConfident bool `json:"score_confident"`
	// Path is the workflow file, e.g. ".github/workflows/ci.yml".
	Path string `json:"path,omitempty"`
	// Touched reports whether this workflow is one of Options.ChangedPaths.
	Touched bool `json:"touched,omitempty"`
}

// MinRunsForScore is the fewest completed runs before a flakiness score is
// worth reporting.
//
// With two runs, one pass and one fail, the maths produces 1.0 -- the maximum
// possible score -- from what is really a single coin flip. Any threshold here
// is a judgement call; five is low enough to stay useful on quiet repos and
// high enough that one flip cannot pin the score to an extreme.
const MinRunsForScore = 5

// Zombie is a run stuck in progress.
type Zombie struct {
	Workflow string  `json:"workflow"`
	URL      string  `json:"url"`
	Hours    float64 `json:"hours_running"`
}

// Result is the full analysis output.
type Result struct {
	Workflows []WorkflowStats `json:"workflows"`
	Zombies   []Zombie        `json:"zombies"`
	Cost      CostSummary     `json:"cost"`
}

// Analyze groups runs by workflow name and computes stats.
//
// Flakiness score: transitions / (runs - 1), weighted by failure rate.
// A workflow that alternates pass/fail every run scores near 1.0; a workflow
// that always passes (or always fails — that's broken, not flaky) scores 0.
func Analyze(runs []gh.WorkflowRun, opts Options) Result {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	changed := make(map[string]struct{}, len(opts.ChangedPaths))
	for _, p := range opts.ChangedPaths {
		if p = strings.TrimSpace(p); p != "" {
			changed[p] = struct{}{}
		}
	}

	byWorkflow := make(map[string][]gh.WorkflowRun)
	var zombies []Zombie

	for _, r := range runs {
		if r.Status == "in_progress" || r.Status == "queued" {
			hours := now.Sub(r.RunStartedAt).Hours()
			if hours > opts.ZombieHours {
				zombies = append(zombies, Zombie{Workflow: r.Name, URL: r.HTMLURL, Hours: hours})
			}
			continue // not part of pass/fail history yet
		}
		if r.Conclusion == "success" || r.Conclusion == "failure" {
			byWorkflow[r.Name] = append(byWorkflow[r.Name], r)
		}
	}

	var stats []WorkflowStats
	for name, wr := range byWorkflow {
		// Chronological order (API returns newest first).
		sort.Slice(wr, func(i, j int) bool { return wr[i].RunStartedAt.Before(wr[j].RunStartedAt) })

		s := WorkflowStats{Name: name, Runs: len(wr), Path: wr[0].Path}
		if _, ok := changed[s.Path]; ok && s.Path != "" {
			s.Touched = true
		}
		var totalDur float64
		for i, r := range wr {
			if r.Conclusion == "failure" {
				s.Failures++
			}
			if i > 0 && wr[i-1].Conclusion != r.Conclusion {
				s.Transitions++
			}
			totalDur += r.UpdatedAt.Sub(r.RunStartedAt).Seconds()
		}
		s.FailureRate = float64(s.Failures) / float64(s.Runs)
		s.AvgDurationSec = totalDur / float64(s.Runs)
		s.FlakinessScore = flakinessScore(s.Runs, s.Transitions, s.FailureRate)
		s.ScoreConfident = s.Runs >= MinRunsForScore
		stats = append(stats, s)
	}

	// Confident scores sort above unreliable ones, so a workflow with two runs
	// cannot head the table on a score built from a single flip.
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].ScoreConfident != stats[j].ScoreConfident {
			return stats[i].ScoreConfident
		}
		return stats[i].FlakinessScore > stats[j].FlakinessScore
	})
	return Result{Workflows: stats, Zombies: zombies}
}

// flakinessScore returns 0..1. Instability (transition rate) is damped by how
// far the failure rate is from the extremes: always-pass and always-fail are
// not flaky by definition.
func flakinessScore(runs, transitions int, failureRate float64) float64 {
	if runs < 2 {
		return 0
	}
	transitionRate := float64(transitions) / float64(runs-1)
	// 4*p*(1-p) peaks at 1.0 when failureRate=0.5 and is 0 at both extremes.
	extremeDamp := 4 * failureRate * (1 - failureRate)
	return transitionRate * extremeDamp
}
