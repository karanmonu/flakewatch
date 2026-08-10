// Package report renders analysis results for terminals and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/karanmonu/flakewatch/internal/analyze"
	"github.com/karanmonu/flakewatch/internal/pricing"
)

// WriteJSON emits the result as indented JSON.
func WriteJSON(w io.Writer, r analyze.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTerminal renders a human-readable report.
func WriteTerminal(w io.Writer, repo string, r analyze.Result, showCost bool) {
	fmt.Fprintf(w, "\nflakewatch report — %s\n", repo)
	fmt.Fprintf(w, "%s\n\n", line(72))

	if showCost {
		writeCostHeadline(w, r.Cost)
		writeRunReconciliation(w, r)
	}

	if len(r.Workflows) == 0 {
		fmt.Fprintln(w, "No completed workflow runs found.")
	} else {
		if showCost {
			fmt.Fprintf(w, "%-28s %5s %6s %7s %7s %9s\n", "WORKFLOW", "RUNS", "FAIL%", "FLAKY", "AVG(s)", "COST")
		} else {
			fmt.Fprintf(w, "%-28s %5s %6s %7s %7s\n", "WORKFLOW", "RUNS", "FAIL%", "FLAKY", "AVG(s)")
		}
		var thin int
		for _, s := range r.Workflows {
			score := fmt.Sprintf("%7.2f", s.FlakinessScore)
			if !s.ScoreConfident {
				score = fmt.Sprintf("%7s", "-")
				thin++
			}
			fmt.Fprintf(w, "%-28s %5d %5.0f%% %s %7.0f",
				truncate(s.Name, 28), s.Runs, s.FailureRate*100, score, s.AvgDurationSec)
			if showCost {
				fmt.Fprintf(w, " %9s", usd(s.CostUSD))
			}
			fmt.Fprintf(w, "  %s\n", badge(s))
		}
		if thin > 0 {
			fmt.Fprintf(w, "\n%d workflow(s) had fewer than %d runs in this window, so no flakiness\n"+
				"score is shown for them. Raise -runs to widen the sample.\n",
				thin, analyze.MinRunsForScore)
		}
	}

	if len(r.Zombies) > 0 {
		fmt.Fprintf(w, "\nZombie runs (stuck in progress):\n")
		for _, z := range r.Zombies {
			fmt.Fprintf(w, "  %-28s %.1fh  %s\n", truncate(z.Workflow, 28), z.Hours, z.URL)
		}
	}

	if showCost {
		writeOpportunities(w, r.Cost.Opportunities)
		fmt.Fprintf(w, "\nRates: %s (retrieved %s)\n", pricing.RatesSource, pricing.RatesRetrieved)
	}
	fmt.Fprintln(w)
}

func writeCostHeadline(w io.Writer, c analyze.CostSummary) {
	if c.RunsPriced == 0 {
		fmt.Fprint(w, "No job data available for these runs, so cost could not be estimated.\n\n")
		return
	}

	if c.MonthlyUSD > 0 {
		fmt.Fprintf(w, "Estimated spend: %s over %.1f days  (~%s/month at this rate)\n",
			usd(c.TotalUSD), c.WindowDays, usd(c.MonthlyUSD))
	} else {
		fmt.Fprintf(w, "Estimated spend: %s over %.1f days  (window too short to project a month)\n",
			usd(c.TotalUSD), c.WindowDays)
	}

	// Public repositories are not billed for standard runners. Saying so keeps
	// a legitimate number from reading as a bug.
	fmt.Fprintln(w, "This is what the runs would cost at published rates. Public repositories")
	fmt.Fprintln(w, "are not billed for standard GitHub-hosted runners.")

	if c.SelfHostedJobs > 0 {
		fmt.Fprintf(w, "%d job(s) ran on self-hosted runners and are excluded (not currently billed).\n", c.SelfHostedJobs)
	}
	if c.UnknownRunnerJobs > 0 {
		fmt.Fprintf(w, "%d job(s) used a runner label with no published rate and are excluded,\n"+
			"so this is an undercount. Labels: %s\n",
			c.UnknownRunnerJobs, strings.Join(c.UnknownLabels, ", "))
	}
	if c.RunsMissingJobs > 0 {
		fmt.Fprintf(w, "%d of %d runs had no job data (aged out) and are excluded.\n",
			c.RunsMissingJobs, c.RunsPriced+c.RunsMissingJobs)
	}
	if c.RunsSkippedForBudget > 0 {
		fmt.Fprintf(w, "%d run(s) were not fetched to protect the shared API rate limit,\n"+
			"so this covers a smaller sample than requested. Lower -runs to avoid it.\n", c.RunsSkippedForBudget)
	}
	if c.WindowTruncated && c.RequestedWindowDays > 0 {
		fmt.Fprintf(w, "Asked for %.0f days but hit the run cap first, so this covers %.1f days.\n",
			c.RequestedWindowDays, c.WindowDays)
		if c.RunsForFullWindow > 0 {
			fmt.Fprintf(w, "About -runs %d would cover the full window, at one request per run.\n",
				c.RunsForFullWindow)
		}
	}
	fmt.Fprintln(w)
}

// writeOpportunities lists spend on platforms dearer than Linux.
//
// The wording matters. flakewatch knows a workflow spent money on macOS; it does
// not know whether that workflow needs macOS. Printing "you can save $X" would
// be wrong for every repo that builds Apple software, so the heading states what
// was measured and the closing note hands the judgement back.
func writeOpportunities(w io.Writer, opps []analyze.Opportunity) {
	if len(opps) == 0 {
		return
	}

	fmt.Fprintf(w, "\nSpend on platforms dearer than Linux:\n")
	fmt.Fprintf(w, "%-28s %-8s %5s %9s %11s  %s\n", "WORKFLOW", "PLATFORM", "JOBS", "COST", "ON LINUX", "DIFFERENCE")
	for _, o := range opps {
		diff := usd(o.DeltaUSD)
		if o.MonthlyDeltaUSD > 0 {
			diff = fmt.Sprintf("%s (~%s/mo)", usd(o.DeltaUSD), usd(o.MonthlyDeltaUSD))
		}
		fmt.Fprintf(w, "%-28s %-8s %5d %9s %11s  %s\n",
			truncate(o.Workflow, 28), o.Platform, o.Jobs, usd(o.CurrentUSD), usd(o.OnLinuxUSD), diff)
	}
	fmt.Fprintln(w, "\nThese are not recommendations. Jobs that genuinely need macOS or Windows")
	fmt.Fprintln(w, "should stay there -- this only shows what that choice costs.")
}

// usd keeps cents visible for the small numbers one workflow produces without
// making the big ones unreadable.
func usd(v float64) string {
	switch {
	case v > 0 && v < 0.01:
		return "<$0.01"
	case v >= 100:
		return fmt.Sprintf("$%.0f", v)
	default:
		return fmt.Sprintf("$%.2f", v)
	}
}

func badge(s analyze.WorkflowStats) string {
	if !s.ScoreConfident {
		return fmt.Sprintf("only %d run(s)", s.Runs)
	}
	switch {
	case s.FlakinessScore >= 0.5:
		return "🔴 flaky"
	case s.FlakinessScore >= 0.2:
		return "🟡 unstable"
	default:
		return "🟢 stable"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func line(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}
// writeRunReconciliation explains why two counts on the same page differ.
//
// Cost includes every run that had jobs. The flakiness table counts only runs
// that concluded success or failure, because a cancelled run says nothing about
// whether a workflow is flaky. Both are right; printing them next to each other
// without a word of explanation is not, and an unexplained discrepancy is how a
// reader decides to distrust every other number here.
func writeRunReconciliation(w io.Writer, r analyze.Result) {
	if r.Cost.RunsPriced == 0 {
		return
	}
	var scored int
	for _, s := range r.Workflows {
		scored += s.Runs
	}
	if scored == r.Cost.RunsPriced {
		return
	}
	fmt.Fprintf(w, "%d runs priced. %d of them concluded success or failure and are scored\n"+
		"below; the other %d were cancelled or skipped, which costs money but says\n"+
		"nothing about flakiness.\n\n", r.Cost.RunsPriced, scored, r.Cost.RunsPriced-scored)
}
