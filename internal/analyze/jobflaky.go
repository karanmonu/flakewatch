package analyze

import (
	"sort"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

// JobStats summarizes one job's recent history inside one workflow.
//
// The workflow-level score dilutes a flaky job among its stable siblings: a
// matrix with seven green legs and one alternating leg scores low at the
// workflow level while one leg wastes a retry on every second run. Scoring the
// job directly is what issue #1 asked for, and the data costs nothing extra --
// jobs are already fetched (and cached) for cost attribution, so this adds
// zero API requests.
type JobStats struct {
	// Workflow is the owning workflow's display name; WorkflowKey its identity
	// (file path when known). Matrix legs are separate jobs on purpose: each
	// leg has its own history, and "Test (windows-latest)" being flaky while
	// "Test (ubuntu-latest)" is stable is exactly the finding.
	Workflow    string `json:"workflow"`
	WorkflowKey string `json:"-"`
	Name        string `json:"name"`
	// Scored counts job executions that concluded success or failure. Skipped
	// and cancelled executions are no evidence either way, same rule as runs.
	Scored         int     `json:"scored"`
	Failures       int     `json:"failures"`
	FailureRate    float64 `json:"failure_rate"`
	Transitions    int     `json:"transitions"`
	FlakinessScore float64 `json:"flakiness_score"`
	ScoreConfident bool    `json:"score_confident"`
	// WorkflowScore is the owning workflow's score over the same window, so a
	// report can show the dilution rather than assert it: 0.62 against 0.10
	// is the whole story in two numbers.
	WorkflowScore float64 `json:"workflow_score"`
}

// AnalyzeJobs scores each job's pass/fail history with the same formula used
// for workflows.
//
// Jobs are grouped by (workflow identity, exact job name). A job's own
// conclusion is what counts: a run can fail overall while this job passed,
// and folding the run's verdict into the job is how dilution happened in the
// first place.
//
// Returns only jobs with at least two scored executions, sorted confident
// first, then by score. Callers decide how many rows are worth showing.
func AnalyzeJobs(runs []gh.WorkflowRun, jobs gh.JobsResult, workflows []WorkflowStats) []JobStats {
	if len(jobs.ByRun) == 0 {
		return nil
	}

	// Job order must be the run's chronology, not map order. Job StartedAt can
	// be zero for legs that never left the queue, so the run's start time is
	// the stable clock, with the run ID as a tiebreak for same-second starts.
	type runMeta struct {
		key     string
		name    string
		started time.Time
	}
	meta := make(map[int64]runMeta, len(runs))
	for _, r := range runs {
		meta[r.ID] = runMeta{key: workflowKey(r), name: r.Name, started: r.RunStartedAt}
	}

	wfScore := make(map[string]float64, len(workflows))
	wfName := make(map[string]string, len(workflows))
	for _, w := range workflows {
		wfScore[w.key] = w.FlakinessScore
		wfName[w.key] = w.Name
	}

	type execution struct {
		started time.Time
		runID   int64
		failed  bool
	}
	type groupKey struct{ wf, job string }
	groups := make(map[groupKey][]execution)

	for runID, jj := range jobs.ByRun {
		m, ok := meta[runID]
		if !ok {
			continue // a cached run outside this window; not part of this sample
		}
		for _, j := range jj {
			if j.Conclusion != "success" && j.Conclusion != "failure" {
				continue
			}
			k := groupKey{wf: m.key, job: j.Name}
			groups[k] = append(groups[k], execution{
				started: m.started,
				runID:   runID,
				failed:  j.Conclusion == "failure",
			})
		}
	}

	var stats []JobStats
	for k, execs := range groups {
		if len(execs) < 2 {
			// One execution cannot transition; reporting it is noise.
			continue
		}
		sort.Slice(execs, func(i, j int) bool {
			if !execs[i].started.Equal(execs[j].started) {
				return execs[i].started.Before(execs[j].started)
			}
			return execs[i].runID < execs[j].runID
		})

		s := JobStats{
			WorkflowKey:   k.wf,
			Workflow:      wfName[k.wf],
			Name:          k.job,
			Scored:        len(execs),
			WorkflowScore: wfScore[k.wf],
		}
		if s.Workflow == "" {
			// A workflow whose every run was cancelled has no stats row; the
			// run's own name is still the honest label.
			s.Workflow = meta[execs[0].runID].name
		}
		for i, e := range execs {
			if e.failed {
				s.Failures++
			}
			if i > 0 && execs[i-1].failed != e.failed {
				s.Transitions++
			}
		}
		s.FailureRate = float64(s.Failures) / float64(s.Scored)
		s.FlakinessScore = flakinessScore(s.Scored, s.Transitions, s.FailureRate)
		s.ScoreConfident = s.Scored >= MinRunsForScore
		stats = append(stats, s)
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].ScoreConfident != stats[j].ScoreConfident {
			return stats[i].ScoreConfident
		}
		if stats[i].FlakinessScore != stats[j].FlakinessScore {
			return stats[i].FlakinessScore > stats[j].FlakinessScore
		}
		// Deterministic order for equal scores, so output diffs are real diffs.
		if stats[i].Workflow != stats[j].Workflow {
			return stats[i].Workflow < stats[j].Workflow
		}
		return stats[i].Name < stats[j].Name
	})
	return stats
}
