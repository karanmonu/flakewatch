package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/karanmonu/flakewatch/internal/analyze"
	"github.com/karanmonu/flakewatch/internal/pricing"
)

// CommentMarker identifies a comment as ours so the Action can update the same
// one on every push instead of leaving a trail of them down the thread.
const CommentMarker = "<!-- flakewatch:report -->"

// maxRows keeps the comment readable. A PR comment competing with the diff for
// attention loses; the full table is one command away.
const maxRows = 5

// WriteMarkdown renders the analysis as a pull request comment.
func WriteMarkdown(w io.Writer, repo string, r analyze.Result) error {
	var b strings.Builder

	b.WriteString(CommentMarker + "\n")
	b.WriteString("## flakewatch\n\n")

	// Naming the workflows this pull request actually touches is the difference
	// between a comment someone reads and a comment someone mutes. The whole-repo
	// table stays below it, because "is this change expensive" is only answerable
	// against what the rest of CI costs.
	touched := touchedNames(r.Workflows)
	switch len(touched) {
	case 0:
		b.WriteString("This pull request changes workflow files. Here is what CI on `")
		b.WriteString(repo)
		b.WriteString("` costs today.\n\n")
	case 1:
		fmt.Fprintf(&b, "This pull request touches **%s**. Here is what it costs today, against the rest of CI on `%s`.\n\n",
			touched[0], repo)
	default:
		fmt.Fprintf(&b, "This pull request touches **%s**. Here is what they cost today, against the rest of CI on `%s`.\n\n",
			strings.Join(touched, "**, **"), repo)
	}

	c := r.Cost
	switch {
	case c.RunsPriced == 0:
		b.WriteString("No job data was available for the recent runs, so cost could not be estimated.\n")
		_, err := io.WriteString(w, b.String())
		return err
	case c.MonthlyUSD > 0:
		fmt.Fprintf(&b, "**%s over %.1f days** — roughly **%s/month** at this rate.\n\n",
			usd(c.TotalUSD), c.WindowDays, usd(c.MonthlyUSD))
	default:
		fmt.Fprintf(&b, "**%s over %.1f days.** The window is too short to project a monthly figure.\n\n",
			usd(c.TotalUSD), c.WindowDays)
	}

	// Workflows this pull request touches first, then costliest — those are the
	// two questions someone editing CI has, in that order.
	byCost := make([]analyze.WorkflowStats, len(r.Workflows))
	copy(byCost, r.Workflows)
	sort.Slice(byCost, func(i, j int) bool {
		if byCost[i].Touched != byCost[j].Touched {
			return byCost[i].Touched
		}
		return byCost[i].CostUSD > byCost[j].CostUSD
	})

	// The largest line is found independently of the display order. Reading it
	// off the top row was correct while the table was sorted purely by cost, and
	// silently wrong the moment touched workflows started sorting above it.
	if top, ok := costliest(r.Workflows); ok && c.TotalUSD > 0 {
		fmt.Fprintf(&b, "`%s` is the largest single line at %s — %.0f%% of the total.\n\n",
			top.Name, usd(top.CostUSD), top.CostUSD/c.TotalUSD*100)
	}

	b.WriteString("| Workflow | Runs | Scored | Fail % | Flakiness | Cost |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	rows := maxRows
	if n := len(touched); n > rows {
		// A pull request touching seven workflows must still see all seven.
		rows = n
	}
	for i, s := range byCost {
		if i >= rows {
			fmt.Fprintf(&b, "| _…and %d more_ | | | | | |\n", len(byCost)-rows)
			break
		}
		flaky := fmt.Sprintf("%.2f", s.FlakinessScore)
		switch {
		case !s.ScoreConfident:
			flaky = fmt.Sprintf("– _(%d scored)_", s.Scored)
		case s.FailureRate >= alwaysFailingThreshold:
			// Consistently broken scores zero for flakiness, which is correct
			// and reads as a clean bill of health unless it is labelled.
			flaky = fmt.Sprintf("%.2f ⛔ **always failing**", s.FlakinessScore)
		}
		name := fmt.Sprintf("`%s`", s.Name)
		if s.Touched {
			name += " ←"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.0f%% | %s | %s |\n",
			name, s.Runs, s.Scored, s.FailureRate*100, flaky, usd(s.CostUSD))
	}
	b.WriteString("\n")

	if len(c.Opportunities) > 0 {
		b.WriteString("### Spend on platforms dearer than Linux\n\n")
		b.WriteString("| Workflow | Platform | Jobs | Cost | On Linux | Difference |\n")
		b.WriteString("|---|---|---:|---:|---:|---:|\n")
		for i, o := range c.Opportunities {
			if i >= maxRows {
				break
			}
			diff := usd(o.DeltaUSD)
			if o.MonthlyDeltaUSD > 0 {
				diff = fmt.Sprintf("%s (~%s/mo)", usd(o.DeltaUSD), usd(o.MonthlyDeltaUSD))
			}
			fmt.Fprintf(&b, "| `%s` | %s | %d | %s | %s | **%s** |\n",
				o.Workflow, o.Platform, o.Jobs, usd(o.CurrentUSD), usd(o.OnLinuxUSD), diff)
		}
		b.WriteString("\nThese are observations, not recommendations. ")
		b.WriteString("Jobs that genuinely need macOS or Windows should stay there — ")
		b.WriteString("this only shows what that choice costs.\n\n")
	}

	if len(c.Superseded) > 0 {
		b.WriteString("### Runs that kept going after a newer commit replaced them\n\n")
		b.WriteString("| Workflow | Runs | Minutes | Cost | Per month |\n")
		b.WriteString("|---|---:|---:|---:|---:|\n")
		for i, o := range c.Superseded {
			if i >= maxRows {
				break
			}
			monthly := "—"
			if o.MonthlyUSD > 0 {
				monthly = "~" + usd(o.MonthlyUSD) + "/mo"
			}
			fmt.Fprintf(&b, "| `%s` | %d | %d | %s | %s |\n",
				o.Workflow, o.Runs, o.WastedMinutes, usd(o.WastedUSD), monthly)
		}
		b.WriteString("\nPull request runs only, counted from the moment the newer run started ")
		b.WriteString("rather than the whole run. Adding this to the workflow stops it:\n\n")
		b.WriteString("```yaml\nconcurrency:\n  group: ${{ github.workflow }}-${{ github.ref }}\n  cancel-in-progress: true\n```\n\n")
		b.WriteString("`cancel-in-progress` is the load-bearing line — without it the group queues ")
		b.WriteString("the newer run behind the older one instead of replacing it.\n\n")
	}

	b.WriteString("<details>\n<summary>What these numbers do and do not include</summary>\n\n")
	fmt.Fprintf(&b, "- Priced against [GitHub's published runner rates](%s), retrieved %s.\n",
		pricing.RatesSource, pricing.RatesRetrieved)
	b.WriteString("- **Runs** is everything that ran; **Scored** is the subset that concluded success or failure. ")
	b.WriteString("Cancelled runs cost money but say nothing about flakiness, so they count in one column and not the other.\n")
	b.WriteString("- Billing rounds **each job** up to the whole minute, not the run. ")
	b.WriteString("A run of ten 30-second jobs bills ten minutes, not five.\n")
	b.WriteString("- Public repositories are not billed for standard GitHub-hosted runners. ")
	b.WriteString("For those, this is what the same runs would cost on a private repository.\n")
	if c.UserPricedJobs > 0 {
		fmt.Fprintf(&b, "- %d job(s) were priced from a user-supplied rate file rather than GitHub's published table: `%s`\n",
			c.UserPricedJobs, strings.Join(c.UserSuppliedLabels, "`, `"))
	}
	if c.SelfHostedJobs > 0 {
		fmt.Fprintf(&b, "- %d job(s) ran on self-hosted runners and are excluded — GitHub does not currently bill them.\n", c.SelfHostedJobs)
	}
	if c.UnknownRunnerJobs > 0 {
		fmt.Fprintf(&b, "- %d job(s) used a runner label with no published rate and are excluded, so the total is an undercount. Labels: `%s`\n",
			c.UnknownRunnerJobs, strings.Join(c.UnknownLabels, "`, `"))
	}
	if c.RunsMissingJobs > 0 {
		fmt.Fprintf(&b, "- %d run(s) had no job data left (logs aged out) and are excluded.\n", c.RunsMissingJobs)
	}
	if c.WindowTruncated && c.RequestedWindowDays > 0 {
		fmt.Fprintf(&b, "- Asked for %.0f days but the run cap was reached first, so this covers %.1f days. The monthly figure is extrapolated from the shorter window.\n",
			c.RequestedWindowDays, c.WindowDays)
	}
	if c.RunsSkippedForBudget > 0 {
		fmt.Fprintf(&b, "- %d run(s) were not fetched, to leave the repository's shared API rate limit intact. This sample is smaller than requested.\n", c.RunsSkippedForBudget)
	}
	b.WriteString("- The monthly figure is a projection from the observed window and is the least reliable number here.\n")
	b.WriteString("\n</details>\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// touchedNames lists the workflows the caller flagged as changed, costliest
// first so the lead sentence names the expensive one when there are several.
func touchedNames(stats []analyze.WorkflowStats) []string {
	var t []analyze.WorkflowStats
	for _, s := range stats {
		if s.Touched {
			t = append(t, s)
		}
	}
	sort.Slice(t, func(i, j int) bool { return t[i].CostUSD > t[j].CostUSD })

	names := make([]string, 0, len(t))
	for _, s := range t {
		names = append(names, s.Name)
	}
	return names
}

// costliest returns the highest-spending workflow, regardless of how the table
// happens to be ordered.
func costliest(stats []analyze.WorkflowStats) (analyze.WorkflowStats, bool) {
	var top analyze.WorkflowStats
	found := false
	for _, s := range stats {
		if !found || s.CostUSD > top.CostUSD {
			top, found = s, true
		}
	}
	return top, found
}
