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
	Name string `json:"name"`
	// Runs is every completed run of this workflow in the window, whatever it
	// concluded. Cancelled runs are included because they cost money.
	Runs int `json:"runs"`
	// Scored is the subset that concluded success or failure, which is the only
	// subset a flakiness score can be computed over: a cancelled run says
	// nothing about whether a workflow is flaky.
	//
	// Reporting both is the point. One number covering two different questions
	// is what made the cost and flakiness columns fail to reconcile (#10).
	Scored         int     `json:"scored"`
	Failures       int     `json:"failures"`
	FailureRate    float64 `json:"failure_rate"`
	Transitions    int     `json:"transitions"` // pass<->fail flips in chronological order
	FlakinessScore float64 `json:"flakiness_score"`
	AvgDurationSec float64 `json:"avg_duration_sec"`
	CostUSD        float64 `json:"cost_usd"` // populated only when -cost is used
	// ScoreConfident reports whether there were enough runs for the flakiness
	// score to mean anything. See MinRunsForScore.
	ScoreConfident bool `json:"score_confident"`
	// Path is the workflow file, e.g. ".github/workflows/ci.yml". Empty when
	// every run of this workflow in the window was cancelled.
	Path string `json:"path,omitempty"`
	// Touched reports whether this workflow is one of Options.ChangedPaths.
	Touched bool `json:"touched,omitempty"`

	// key is what runs were grouped by. Unexported: it is Path when there is
	// one and Name otherwise, which is an implementation detail rather than
	// something a consumer of the JSON should have to reason about.
	key string
}

// workflowKey is the identity a workflow is grouped by.
//
// The file, not the display name. A workflow's `name:` can change without the
// file changing, which splits one workflow into two rows each holding half its
// history; and two different files can share a name -- `name: CI` is not rare
// -- which merges two workflows into one row whose cost is the sum of both and
// whose path is whichever run happened to sort first.
//
// Runs old enough to predate GitHub returning a path fall back to the name,
// which is the pre-existing behaviour and no worse than it was.
func workflowKey(r gh.WorkflowRun) string {
	if r.Path != "" {
		return r.Path
	}
	return r.Name
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
	totalRuns := make(map[string]int)
	displayName := make(map[string]string)
	newestRun := make(map[string]time.Time)
	var zombies []Zombie

	for _, r := range runs {
		if r.Status == "in_progress" || r.Status == "queued" {
			hours := now.Sub(r.RunStartedAt).Hours()
			if hours > opts.ZombieHours {
				zombies = append(zombies, Zombie{Workflow: r.Name, URL: r.HTMLURL, Hours: hours})
			}
			continue // not part of pass/fail history yet
		}
		// Every completed run counts towards Runs, whatever it concluded.
		k := workflowKey(r)
		totalRuns[k]++
		// The newest run wins the display name, so a renamed workflow shows
		// what it is called now rather than what it used to be called.
		if _, seen := displayName[k]; !seen || r.RunStartedAt.After(newestRun[k]) {
			displayName[k] = r.Name
			newestRun[k] = r.RunStartedAt
		}
		if r.Conclusion == "success" || r.Conclusion == "failure" {
			byWorkflow[k] = append(byWorkflow[k], r)
		}
	}

	// A workflow can appear only as cancelled runs: no score, but real spend.
	for k := range totalRuns {
		if _, ok := byWorkflow[k]; !ok {
			byWorkflow[k] = nil
		}
	}

	var stats []WorkflowStats
	for k, wr := range byWorkflow {
		// Chronological order (API returns newest first).
		sort.Slice(wr, func(i, j int) bool { return wr[i].RunStartedAt.Before(wr[j].RunStartedAt) })

		s := WorkflowStats{Name: displayName[k], Runs: totalRuns[k], Scored: len(wr), key: k}
		if len(wr) > 0 {
			s.Path = wr[len(wr)-1].Path
		}
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
		if s.Scored > 0 {
			s.FailureRate = float64(s.Failures) / float64(s.Scored)
			s.AvgDurationSec = totalDur / float64(s.Scored)
		}
		s.FlakinessScore = flakinessScore(s.Scored, s.Transitions, s.FailureRate)
		s.ScoreConfident = s.Scored >= MinRunsForScore
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
//
// scored, not total runs: a cancelled run is not evidence either way.
func flakinessScore(scored, transitions int, failureRate float64) float64 {
	if scored < 2 {
		return 0
	}
	transitionRate := float64(transitions) / float64(scored-1)
	// 4*p*(1-p) peaks at 1.0 when failureRate=0.5 and is 0 at both extremes.
	extremeDamp := 4 * failureRate * (1 - failureRate)
	return transitionRate * extremeDamp
}
