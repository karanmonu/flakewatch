package analyze

import (
	"sort"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/pricing"
)

// RunCostUSD estimates what a workflow run's jobs cost at published rates.
//
// The arithmetic that matters: GitHub rounds each *job* up to the next whole
// minute, so a run's cost is the sum over its jobs of ceil(job duration), not
// the rounded total of the run. A run of ten 30-second jobs bills ten minutes,
// not five, and the gap widens the wider a matrix fans out.
func RunCostUSD(jobs []gh.Job) float64 {
	var total float64
	for _, j := range jobs {
		if j.DurationMS() == 0 {
			// Skipped or never-started job. It has no runner and no cost, and
			// treating it as an unpriceable job would misreport how much of the
			// estimate is missing.
			continue
		}
		runner := pricing.Resolve(j.Labels)
		if !runner.Known {
			// Self-hosted, or a label we have no published rate for. Skipping
			// is right in both cases: "not billed" and "we don't know" are both
			// wrong to record as a positive number.
			continue
		}
		total += float64(pricing.CeilMinutes(j.DurationMS())) * runner.USDPerMinute
	}
	return total
}

// CostSummary is the money view of an analysis.
type CostSummary struct {
	// TotalUSD is the estimated spend across every run examined.
	TotalUSD float64 `json:"total_usd"`
	// MonthlyUSD extrapolates TotalUSD to 30 days using the observed window.
	// Zero when the window is too short to extrapolate honestly.
	MonthlyUSD float64 `json:"monthly_usd"`
	// WindowDays spans the oldest to newest run examined.
	WindowDays float64 `json:"window_days"`
	// RunsPriced is how many runs contributed to TotalUSD.
	RunsPriced int `json:"runs_priced"`
	// RunsMissingJobs is how many runs had no job data available.
	RunsMissingJobs int `json:"runs_missing_jobs"`
	// SelfHostedJobs counts jobs skipped because they ran on self-hosted
	// runners, which GitHub does not currently bill.
	SelfHostedJobs int `json:"self_hosted_jobs"`
	// UnknownRunnerJobs counts jobs skipped because their runner label had no
	// published rate. A non-zero value means TotalUSD is an undercount.
	UnknownRunnerJobs int `json:"unknown_runner_jobs"`
	// RunsSkippedForBudget counts runs left unfetched to protect the shared API
	// rate limit. Non-zero means the sample is smaller than requested.
	RunsSkippedForBudget int `json:"runs_skipped_for_budget"`
	// WindowTruncated is true when -since asked for a longer window than the run
	// cap allowed. The figures are real; they just cover less time than asked
	// for, which matters most for the monthly projection.
	WindowTruncated bool `json:"window_truncated,omitempty"`
	// RequestedWindowDays is the window -since asked for, zero when unused.
	RequestedWindowDays float64 `json:"requested_window_days,omitempty"`
	// RunsForFullWindow estimates the -runs value that would have covered the
	// requested window, from the run density actually observed. Zero unless the
	// window was truncated.
	RunsForFullWindow int `json:"runs_for_full_window,omitempty"`
	// UnknownLabels lists the distinct labels behind UnknownRunnerJobs.
	//
	// Naming them rather than only counting them means the tool reports its own
	// blind spots: anyone can read the list and open an issue, and it is the
	// fastest way to find rates missing from the table.
	UnknownLabels []string `json:"unknown_labels,omitempty"`
	// Opportunities lists per-workflow spend on platforms dearer than Linux,
	// with the Linux counterfactual priced. Observations, not recommendations.
	Opportunities []Opportunity `json:"opportunities,omitempty"`
}

// minWindowForExtrapolation is the shortest observed window we will scale to a
// monthly figure.
//
// A week, because CI load is weekly-periodic: weekdays are busy and weekends
// are nearly dead, so any window shorter than a full cycle systematically
// misstates the month. Surveying eight public repositories made the size of
// that error concrete -- golangci-lint measured $8.82 over 1.8 consecutive
// weekdays, which the old 24-hour floor happily scaled to "$143/month". The
// $8.82 was measured; the $143 was an artefact of when the sample was taken.
//
// The cost of the stricter rule is that busy repositories get no monthly
// figure until -runs is raised enough to span a week. That is the right
// trade: a missing number invites a second look, a wrong one does not.
const minWindowForExtrapolation = 7 * 24 * time.Hour

// SummarizeCost prices a set of runs and attributes spend per workflow.
//
// It fills in the CostUSD field of each workflow stat, replaces AvgDurationSec
// with measured execution time, and returns the repository-level summary.
func SummarizeCost(runs []gh.WorkflowRun, jobs gh.JobsResult, stats []WorkflowStats) CostSummary {
	costByWorkflow := make(map[string]float64, len(stats))

	var (
		total          float64
		priced         int
		selfHosted     int
		unknownRunners int
		oldest, newest time.Time
	)
	unknownLabels := make(map[string]struct{})
	execSecondsByWorkflow := make(map[string]float64, len(stats))
	execRunsByWorkflow := make(map[string]int, len(stats))

	for _, r := range runs {
		runJobs, ok := jobs.ByRun[r.ID]
		if !ok {
			continue
		}
		cost := RunCostUSD(runJobs)
		costByWorkflow[r.Name] += cost
		total += cost
		priced++

		// Execution wall-clock for this run: first job start to last job finish.
		// The run record's UpdatedAt is not a substitute -- it moves for reasons
		// unrelated to execution, which is how a two-cent auto-assign workflow
		// came out averaging three hours.
		var firstStart, lastEnd time.Time
		for _, j := range runJobs {
			if j.DurationMS() == 0 {
				continue
			}
			if firstStart.IsZero() || j.StartedAt.Before(firstStart) {
				firstStart = j.StartedAt
			}
			if j.CompletedAt.After(lastEnd) {
				lastEnd = j.CompletedAt
			}
		}
		if lastEnd.After(firstStart) {
			execSecondsByWorkflow[r.Name] += lastEnd.Sub(firstStart).Seconds()
			execRunsByWorkflow[r.Name]++
		}

		for _, j := range runJobs {
			if j.DurationMS() == 0 {
				continue
			}
			switch runner := pricing.Resolve(j.Labels); {
			case runner.SelfHosted:
				selfHosted++
			case !runner.Known:
				unknownRunners++
				label := runner.Label
				if label == "" {
					// A job with no labels at all. Recording it as an empty
					// string would hide it in the report.
					label = "(no labels reported)"
				}
				unknownLabels[label] = struct{}{}
			}
		}

		if oldest.IsZero() || r.RunStartedAt.Before(oldest) {
			oldest = r.RunStartedAt
		}
		if r.RunStartedAt.After(newest) {
			newest = r.RunStartedAt
		}
	}

	for i := range stats {
		stats[i].CostUSD = costByWorkflow[stats[i].Name]
		// Prefer measured execution time over the run record's wall clock.
		if n := execRunsByWorkflow[stats[i].Name]; n > 0 {
			stats[i].AvgDurationSec = execSecondsByWorkflow[stats[i].Name] / float64(n)
		}
	}

	labels := make([]string, 0, len(unknownLabels))
	for l := range unknownLabels {
		labels = append(labels, l)
	}
	sort.Strings(labels) // map order is random; a stable report is diffable

	summary := CostSummary{
		TotalUSD:             total,
		RunsPriced:           priced,
		RunsMissingJobs:      jobs.Missing,
		RunsSkippedForBudget: jobs.SkippedForBudget,
		SelfHostedJobs:       selfHosted,
		UnknownRunnerJobs:    unknownRunners,
		UnknownLabels:        labels,
	}

	var monthlyFactor float64
	if window := newest.Sub(oldest); window > 0 {
		summary.WindowDays = window.Hours() / 24
		if window >= minWindowForExtrapolation {
			monthlyFactor = 30 * 24 * float64(time.Hour) / float64(window)
			summary.MonthlyUSD = total * monthlyFactor
		}
	}
	summary.Opportunities = findOpportunities(runs, jobs, monthlyFactor)
	return summary
}
