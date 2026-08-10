// Package report renders analysis results for terminals and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

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
			fmt.Fprintf(w, "%-28s %5s %6s %6s %7s %7s %9s\n", "WORKFLOW", "RUNS", "SCORED", "FAIL%", "FLAKY", "AVG(s)", "COST")
		} else {
			fmt.Fprintf(w, "%-28s %5s %6s %6s %7s %7s\n", "WORKFLOW", "RUNS", "SCORED", "FAIL%", "FLAKY", "AVG(s)")
		}
		var thin, touched int
		for _, s := range r.Workflows {
			score := fmt.Sprintf("%7.2f", s.FlakinessScore)
			if !s.ScoreConfident {
				score = fmt.Sprintf("%7s", "-")
				thin++
			}
			name := truncate(s.Name, 26)
			if s.Touched {
				name += " *"
				touched++
			}
			fmt.Fprintf(w, "%-28s %5d %6d %5.0f%% %s %7.0f",
				name, s.Runs, s.Scored, s.FailureRate*100, score, s.AvgDurationSec)
			if showCost {
				fmt.Fprintf(w, " %9s", usd(s.CostUSD))
			}
			fmt.Fprintf(w, "  %s\n", badge(s))
		}
		if touched > 0 {
			fmt.Fprintf(w, "\n* marks the %d workflow(s) named by -changed.\n", touched)
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
		writeSuperseded(w, r.Cost.Superseded)
		writeStepCosts(w, r.Cost.StepCosts)
		fmt.Fprintf(w, "\nRates: %s (retrieved %s)\n", pricing.RatesSource, pricing.RatesRetrieved)
		writeRateStaleness(w, time.Now())
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

	if c.UserPricedJobs > 0 {
		fmt.Fprintf(w, "%d job(s) were priced from your -rates file, not GitHub's table: %s\n",
			c.UserPricedJobs, strings.Join(c.UserSuppliedLabels, ", "))
	}
	if c.SelfHostedJobs > 0 {
		fmt.Fprintf(w, "%d job(s) ran on self-hosted runners and are excluded (not currently billed).\n", c.SelfHostedJobs)
	}
	if c.UnknownRunnerJobs > 0 {
		fmt.Fprintf(w, "%d job(s) used a runner label with no published rate and are excluded,\n"+
			"so this is an undercount. Labels: %s\n",
			c.UnknownRunnerJobs, strings.Join(c.UnknownLabels, ", "))
		writeRatesHint(w, c.UnknownLabels)
	}
	if c.RunsFromCache > 0 {
		fmt.Fprintf(w, "%d of %d run(s) were priced from local history instead of being\n"+
			"re-fetched. Pass -no-cache to measure everything from the API again.\n",
			c.RunsFromCache, c.RunsPriced)
	}
	if c.RunsFromHistory > 0 {
		fmt.Fprintf(w, "%d of those predate this run's sample, so the window is wider than\n"+
			"one invocation could have reached.\n", c.RunsFromHistory)
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

// alwaysFailingThreshold is the failure rate above which a workflow is called
// broken rather than scored for flakiness.
//
// Not 1.0. A workflow that fails 19 times out of 20 has the same problem as one
// that fails 20 out of 20, and the reader does not care about the distinction.
const alwaysFailingThreshold = 0.9

// badge summarises a workflow's health in one glyph.
//
// The flakiness score is deliberately zero for a workflow that always fails --
// consistently broken is not flaky, and 4p(1-p) is built to say so. But zero
// score fell through to "stable", which meant a workflow failing 100% of the
// time was reported with a green dot and the word stable. Arithmetically
// defensible, and completely wrong to a human reading the line. "Not flaky" and
// "fine" are different claims and the output has to keep them apart.
func badge(s analyze.WorkflowStats) string {
	if !s.ScoreConfident {
		return fmt.Sprintf("only %d scored", s.Scored)
	}
	switch {
	case s.FailureRate >= alwaysFailingThreshold:
		return "⛔ always failing"
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

// writeRateStaleness warns when the built-in price list is old enough that it
// may no longer match GitHub's.
//
// The rates are hardcoded data carrying a retrieval date, which is honest but
// passive: nobody reads a date and does the subtraction. A stale table produces
// a confident wrong number, which is the failure this tool exists to avoid, so
// it does the subtraction itself.
func writeRateStaleness(w io.Writer, now time.Time) {
	if !pricing.RatesStale(now) {
		return
	}
	months := int(pricing.RatesAge(now).Hours() / 24 / 30)
	fmt.Fprintf(w, "Those rates were last checked %d months ago and may have moved since.\n"+
		"Treat the totals as indicative and check the source above.\n", months)
}

// writeStepCosts lists the dearest individual steps.
//
// The share caveat is printed every time rather than tucked into a footnote,
// because the number invites exactly the wrong reading: it looks like a charge
// and it is a slice of one. Someone who adds these up and compares the total to
// the workflow figure should find the explanation on the same screen.
func writeStepCosts(w io.Writer, steps []analyze.StepCost) {
	if len(steps) == 0 {
		return
	}

	fmt.Fprintf(w, "\nWhere the time goes, by step:\n")
	fmt.Fprintf(w, "%-22s %-26s %-8s %7s %9s %10s\n", "WORKFLOW", "STEP", "PLATFORM", "RAN", "MINUTES", "SHARE")
	for _, s := range steps {
		platform := s.Platform
		if platform == "" {
			platform = "-"
		}
		fmt.Fprintf(w, "%-22s %-26s %-8s %7d %9.0f %10s\n",
			truncate(s.Workflow, 22), truncate(s.Step, 26), platform, s.Executions, s.Seconds/60, usd(s.USD))
	}
	fmt.Fprintln(w, "\nPLATFORM is what joins this to the table above: the same step on Windows")
	fmt.Fprintln(w, "and on Linux is two rows, because those differ by a factor of ten in price.")
	fmt.Fprintln(w, "RAN counts every execution; a step that finishes inside a second measures")
	fmt.Fprintln(w, "as zero minutes because step timestamps have no sub-second component.")
	fmt.Fprintln(w, "GitHub bills the job, not the step, so these are each step's share of its")
	fmt.Fprintln(w, "job's cost and they sum to slightly less than the workflow totals above --")
	fmt.Fprintln(w, "the difference is the per-job rounding. Matrix legs are counted separately,")
	fmt.Fprintln(w, "so RAN is higher than the run count wherever a job fans out.")
}

// writeSuperseded lists compute spent on pull request commits that had already
// been replaced.
//
// This is the one table in the report that ends with something to do. Every
// other number here is deliberately phrased as an observation, because the tool
// cannot see whether a workflow needs macOS or whether a slow test is slow for
// a reason. It can see that a run finished for a commit that no longer existed,
// and there is no version of that which is working as intended.
func writeSuperseded(w io.Writer, sup []analyze.Superseded) {
	if len(sup) == 0 {
		return
	}

	fmt.Fprintf(w, "\nRuns that kept going after a newer commit replaced them:\n")
	fmt.Fprintf(w, "%-28s %5s %9s %11s  %s\n", "WORKFLOW", "RUNS", "MINUTES", "COST", "PER MONTH")
	for _, o := range sup {
		monthly := "-"
		if o.MonthlyUSD > 0 {
			monthly = "~" + usd(o.MonthlyUSD) + "/mo"
		}
		fmt.Fprintf(w, "%-28s %5d %9d %11s  %s\n",
			truncate(o.Workflow, 28), o.Runs, o.WastedMinutes, usd(o.WastedUSD), monthly)
	}

	fmt.Fprintln(w, "\nPull request runs only. Counted from the moment the newer run started,")
	fmt.Fprintln(w, "not the whole run -- the minutes before that were buying a result someone")
	fmt.Fprintln(w, "still wanted. Adding this to the workflow stops it:")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  concurrency:")
	fmt.Fprintln(w, "    group: ${{ github.workflow }}-${{ github.ref }}")
	fmt.Fprintln(w, "    cancel-in-progress: true")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "cancel-in-progress is the load-bearing line. Without it the group queues")
	fmt.Fprintln(w, "the newer run behind the older one instead of replacing it.")
	if sup[0].ExampleURL != "" {
		fmt.Fprintf(w, "Example: %s\n", sup[0].ExampleURL)
	}
}

// writeRatesHint turns the undercount warning into something the reader can act
// on, with their own labels already filled in.
//
// Naming the gap was the old behaviour and it was only half the job: a reader
// who learns their biggest runner is unpriced still has to go and find out that
// a rate file exists, what shape it is, and which flag takes it. Printing the
// file they need removes all three steps, and the labels are already known
// because the same code just counted them.
func writeRatesHint(w io.Writer, labels []string) {
	printable := make([]string, 0, len(labels))
	for _, l := range labels {
		// A job that reported no labels has nothing to key a rate off. Offering
		// the reader a line they cannot use would be worse than leaving it out.
		if l != noLabelsReported {
			printable = append(printable, l)
		}
	}
	if len(printable) == 0 {
		return
	}

	fmt.Fprintln(w, "To include them, put your own per-minute rates in a file:")
	fmt.Fprintln(w, "  {")
	for i, l := range printable {
		comma := ","
		if i == len(printable)-1 {
			comma = ""
		}
		fmt.Fprintf(w, "    %q: 0.000%s\n", l, comma)
	}
	fmt.Fprintln(w, "  }")
	fmt.Fprintln(w, "and pass it with -rates. Self-hosted runners work the same way.")
}

// noLabelsReported mirrors the placeholder analyze uses for a job that reported
// no runner labels at all.
const noLabelsReported = "(no labels reported)"
