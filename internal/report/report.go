// Package report renders analysis results for terminals and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"

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
	}

	if len(r.Workflows) == 0 {
		fmt.Fprintln(w, "No completed workflow runs found.")
	} else {
		if showCost {
			fmt.Fprintf(w, "%-28s %5s %6s %7s %7s %9s\n", "WORKFLOW", "RUNS", "FAIL%", "FLAKY", "AVG(s)", "COST")
		} else {
			fmt.Fprintf(w, "%-28s %5s %6s %7s %7s\n", "WORKFLOW", "RUNS", "FAIL%", "FLAKY", "AVG(s)")
		}
		for _, s := range r.Workflows {
			fmt.Fprintf(w, "%-28s %5d %5.0f%% %7.2f %7.0f",
				truncate(s.Name, 28), s.Runs, s.FailureRate*100, s.FlakinessScore, s.AvgDurationSec)
			if showCost {
				fmt.Fprintf(w, " %9s", usd(s.CostUSD))
			}
			fmt.Fprintf(w, "  %s\n", badge(s.FlakinessScore))
		}
	}

	if len(r.Zombies) > 0 {
		fmt.Fprintf(w, "\nZombie runs (stuck in progress):\n")
		for _, z := range r.Zombies {
			fmt.Fprintf(w, "  %-28s %.1fh  %s\n", truncate(z.Workflow, 28), z.Hours, z.URL)
		}
	}

	if showCost {
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
		fmt.Fprintf(w, "%d job(s) used a runner label with no published rate and are excluded, so this is an undercount.\n", c.UnknownRunnerJobs)
	}
	if c.RunsMissingJobs > 0 {
		fmt.Fprintf(w, "%d of %d runs had no job data (aged out) and are excluded.\n",
			c.RunsMissingJobs, c.RunsPriced+c.RunsMissingJobs)
	}
	fmt.Fprintln(w)
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

func badge(score float64) string {
	switch {
	case score >= 0.5:
		return "🔴 flaky"
	case score >= 0.2:
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
