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
	b.WriteString("This pull request changes workflow files. Here is what CI on `")
	b.WriteString(repo)
	b.WriteString("` costs today.\n\n")

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

	// Costliest workflows first — that is the question someone editing CI has.
	byCost := make([]analyze.WorkflowStats, len(r.Workflows))
	copy(byCost, r.Workflows)
	sort.Slice(byCost, func(i, j int) bool { return byCost[i].CostUSD > byCost[j].CostUSD })

	if len(byCost) > 0 && c.TotalUSD > 0 {
		top := byCost[0]
		fmt.Fprintf(&b, "`%s` is the largest single line at %s — %.0f%% of the total.\n\n",
			top.Name, usd(top.CostUSD), top.CostUSD/c.TotalUSD*100)
	}

	b.WriteString("| Workflow | Runs | Fail % | Flakiness | Cost |\n")
	b.WriteString("|---|---:|---:|---:|---:|\n")
	for i, s := range byCost {
		if i >= maxRows {
			fmt.Fprintf(&b, "| _…and %d more_ | | | | |\n", len(byCost)-maxRows)
			break
		}
		flaky := fmt.Sprintf("%.2f", s.FlakinessScore)
		if !s.ScoreConfident {
			flaky = fmt.Sprintf("– _(%d runs)_", s.Runs)
		}
		fmt.Fprintf(&b, "| `%s` | %d | %.0f%% | %s | %s |\n",
			s.Name, s.Runs, s.FailureRate*100, flaky, usd(s.CostUSD))
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

	b.WriteString("<details>\n<summary>What these numbers do and do not include</summary>\n\n")
	fmt.Fprintf(&b, "- Priced against [GitHub's published runner rates](%s), retrieved %s.\n",
		pricing.RatesSource, pricing.RatesRetrieved)
	b.WriteString("- Billing rounds **each job** up to the whole minute, not the run. ")
	b.WriteString("A run of ten 30-second jobs bills ten minutes, not five.\n")
	b.WriteString("- Public repositories are not billed for standard GitHub-hosted runners. ")
	b.WriteString("For those, this is what the same runs would cost on a private repository.\n")
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
	b.WriteString("- The monthly figure is a projection from the observed window and is the least reliable number here.\n")
	b.WriteString("\n</details>\n")

	_, err := io.WriteString(w, b.String())
	return err
}
