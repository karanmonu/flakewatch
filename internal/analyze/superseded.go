package analyze

import (
	"sort"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/pricing"
)

// Superseded is compute a workflow spent on a commit that had already been
// replaced.
//
// The situation: someone pushes to a pull request branch, CI starts, they push
// again two minutes later. Without a concurrency group the first run carries on
// to completion, and every minute it spends after the second push buys a result
// for a commit nobody will ever look at.
//
// This is the first thing flakewatch reports that is a defect rather than an
// observation. The platform table deliberately refuses to recommend anything,
// because a workflow may genuinely need macOS. There is no equivalent reading
// here: a finished run whose commit was superseded before it finished produced
// an answer to a question nobody was still asking. The only judgement left is
// whether the fix is worth the change, which is why the cost is priced.
type Superseded struct {
	Workflow string `json:"workflow"`
	// Runs is how many runs kept going after being superseded.
	Runs int `json:"runs"`
	// WastedUSD is what those runs spent *after* the moment a concurrency group
	// would have cancelled them -- not their whole cost. The minutes before the
	// newer push were buying a result that was still wanted at the time.
	WastedUSD float64 `json:"wasted_usd"`
	// WastedMinutes is the same figure in billable minutes.
	WastedMinutes int64 `json:"wasted_minutes"`
	// MonthlyUSD extrapolates WastedUSD to 30 days, zero when the window was
	// too short to extrapolate honestly.
	MonthlyUSD float64 `json:"monthly_usd,omitempty"`
	// ExampleURL links one of the runs, so the reader can check the claim
	// against GitHub rather than taking this on faith.
	ExampleURL string `json:"example_url,omitempty"`
}

type supersededKey struct {
	workflow string
	branch   string
}

type supersededTally struct {
	runs       int
	minutes    int64
	usd        float64
	exampleURL string
}

// findSuperseded finds runs that were still executing when a newer run for the
// same workflow and branch began, and that were allowed to finish anyway.
//
// Scope is deliberately narrow -- pull request events only. On the default
// branch, overlapping runs are usually the intended behaviour: one build per
// commit is how you find out which commit broke something, and cancelling the
// older one would throw that away. On a pull request branch the older commit is
// gone and its result has no audience. Reporting both would bury the real
// finding under a pile of correct-by-design noise, and a tool that cries wolf
// about main gets its whole output ignored.
//
// Runs that concluded "cancelled" are not counted: something already stopped
// them, which is the outcome this would recommend.
func findSuperseded(runs []gh.WorkflowRun, jobs gh.JobsResult, monthlyFactor float64, rates pricing.Overrides) []Superseded {
	groups := make(map[supersededKey][]gh.WorkflowRun)
	displayName := make(map[string]string)

	for _, r := range runs {
		if r.Event != "pull_request" && r.Event != "pull_request_target" {
			continue
		}
		if r.HeadBranch == "" || r.RunStartedAt.IsZero() {
			continue
		}
		displayName[workflowKey(r)] = r.Name
		key := supersededKey{workflow: workflowKey(r), branch: r.HeadBranch}
		groups[key] = append(groups[key], r)
	}

	tally := make(map[string]*supersededTally)

	for key, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			if !group[i].RunStartedAt.Equal(group[j].RunStartedAt) {
				return group[i].RunStartedAt.Before(group[j].RunStartedAt)
			}
			return group[i].ID < group[j].ID // stable for same-second starts
		})

		for i := 0; i < len(group)-1; i++ {
			r := group[i]
			if r.Status != "completed" || r.Conclusion == "cancelled" || r.Conclusion == "" {
				continue
			}
			runJobs, ok := jobs.ByRun[r.ID]
			if !ok {
				continue
			}

			// The group is sorted by start, so the very next run is the
			// earliest thing that could have superseded this one. If that
			// started after this run finished, nothing later can have
			// overlapped it either.
			cutoff := group[i+1].RunStartedAt
			minutes, usd := billedAfter(runJobs, cutoff, rates)
			if minutes == 0 {
				continue
			}

			t := tally[key.workflow]
			if t == nil {
				t = &supersededTally{}
				tally[key.workflow] = t
			}
			t.runs++
			t.minutes += minutes
			t.usd += usd
			if t.exampleURL == "" {
				t.exampleURL = r.HTMLURL
			}
		}
	}

	out := make([]Superseded, 0, len(tally))
	for workflow, t := range tally {
		out = append(out, Superseded{
			Workflow:      displayName[workflow],
			Runs:          t.runs,
			WastedMinutes: t.minutes,
			WastedUSD:     t.usd,
			MonthlyUSD:    t.usd * monthlyFactor,
			ExampleURL:    t.exampleURL,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].WastedUSD != out[j].WastedUSD {
			return out[i].WastedUSD > out[j].WastedUSD
		}
		if out[i].WastedMinutes != out[j].WastedMinutes {
			return out[i].WastedMinutes > out[j].WastedMinutes
		}
		return out[i].Workflow < out[j].Workflow
	})
	return out
}

// billedAfter prices the part of a run that happened after cutoff.
//
// Per job, because billing is per job: a job that was already finished when the
// newer push landed cost nothing extra, and a job that started afterwards was
// wasted end to end. Each job's overlap is rounded up to a whole minute on its
// own, the same arithmetic GitHub bills by, so a fan-out of short jobs is not
// quietly understated.
//
// This is the number a concurrency group would actually have saved, and it is
// smaller than the runs' total cost -- often much smaller. Reporting the whole
// run would be the easier and more impressive figure, and it would be wrong:
// until the newer commit existed, those minutes were buying a result somebody
// wanted.
func billedAfter(jobs []gh.Job, cutoff time.Time, rates pricing.Overrides) (int64, float64) {
	var (
		minutes int64
		usd     float64
	)
	for _, j := range jobs {
		if j.DurationMS() == 0 || !j.CompletedAt.After(cutoff) {
			continue
		}
		start := j.StartedAt
		if start.Before(cutoff) {
			start = cutoff
		}
		overlap := j.CompletedAt.Sub(start)
		if overlap <= 0 {
			continue
		}
		billable := pricing.CeilMinutes(overlap.Milliseconds())
		minutes += billable

		runner := pricing.ResolveWith(j.Labels, rates)
		if !runner.Known {
			// Same rule as everywhere else: an unpriced runner contributes its
			// minutes to the count but not a made-up number to the total.
			continue
		}
		usd += float64(billable) * runner.USDPerMinute
	}
	return minutes, usd
}
