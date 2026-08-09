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
	// UnknownLabels lists the distinct labels behind UnknownRunnerJobs.
	//
	// Naming them rather than only counting them means the tool reports its own
	// blind spots: anyone can read the list and open an issue, and it is the
	// fastest way to find rates missing from the table.
	UnknownLabels []string `json:"unknown_labels,omitempty"`
}

// minWindowForExtrapolation is the shortest observed window we will scale to a
// monthly figure. Below this, one busy afternoon dominates and produces a
// number that looks authoritative and is not.
const minWindowForExtrapolation = 24 * time.Hour

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
		TotalUSD:          total,
		RunsPriced:        priced,
		RunsMissingJobs:   jobs.Missing,
		SelfHostedJobs:    selfHosted,
		UnknownRunnerJobs: unknownRunners,
		UnknownLabels:     labels,
	}

	if window := newest.Sub(oldest); window > 0 {
		summary.WindowDays = window.Hours() / 24
		if window >= minWindowForExtrapolation {
			summary.MonthlyUSD = total * (30 * 24 * float64(time.Hour) / float64(window))
		}
	}
	return summary
}
