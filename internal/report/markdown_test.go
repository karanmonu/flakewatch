package report

import (
	"strings"
	"testing"

	"github.com/karanmonu/flakewatch/internal/analyze"
)

func sample() analyze.Result {
	return analyze.Result{
		Workflows: []analyze.WorkflowStats{
			{Name: "E2E", Runs: 12, Scored: 12, FailureRate: 0.08, FlakinessScore: 0.06, ScoreConfident: true, CostUSD: 9.68},
			{Name: "Test", Runs: 10, Scored: 10, FailureRate: 0.5, FlakinessScore: 0.33, ScoreConfident: true, CostUSD: 4.38},
			{Name: "TC39", Runs: 3, Scored: 3, FailureRate: 0, FlakinessScore: 0, ScoreConfident: false, CostUSD: 0.02},
		},
		Cost: analyze.CostSummary{
			TotalUSD: 24.12, MonthlyUSD: 224.0, WindowDays: 3.2, RunsPriced: 200,
			UnknownRunnerJobs: 8, UnknownLabels: []string{"github-hosted-windows-x64-large"},
			Opportunities: []analyze.Opportunity{
				{Workflow: "E2E", Platform: "macos", Jobs: 24, CurrentUSD: 7.94, OnLinuxUSD: 0.77, DeltaUSD: 7.17, MonthlyDeltaUSD: 66.67},
			},
		},
	}
}

// The Action finds its own previous comment by this marker and edits it. Without
// it, every push leaves another comment on the thread.
func TestMarkdownIncludesCommentMarker(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "grafana/k6", sample()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), CommentMarker) {
		t.Error("comment must start with the marker so the Action can find and update it")
	}
}

func TestMarkdownSortsByCostNotFlakiness(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	// E2E costs most but is the least flaky; someone editing CI cares about cost.
	iE2E, iTest := strings.Index(out, "`E2E`"), strings.Index(out, "`Test`")
	if iE2E == -1 || iTest == -1 {
		t.Fatal("expected both workflows in the table")
	}
	if iE2E > iTest {
		t.Error("expected the costliest workflow first")
	}
}

func TestMarkdownNamesTheTopSpenderWithItsShare(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "largest single line") || !strings.Contains(out, "40%") {
		t.Errorf("expected the top spender called out with its share of total; got:\n%s", out)
	}
}

// A thin sample must not show a number that looks authoritative.
func TestMarkdownSuppressesUnreliableFlakinessScores(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", sample()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "(3 scored)") {
		t.Error("a workflow with too few runs should show its run count instead of a score")
	}
}

func TestMarkdownDisclosesExclusions(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"github-hosted-windows-x64-large",
		"least reliable",
		"whole minute",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comment should disclose %q", want)
		}
	}
}

func TestMarkdownHandlesNoCostData(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", analyze.Result{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "could not be estimated") {
		t.Error("expected a clear message rather than a table of zeroes")
	}
}

// A comment built from a truncated sample must say so. The alternative is a
// confident-looking total that quietly covers a fraction of the window.
func TestMarkdownDisclosesABudgetTruncatedSample(t *testing.T) {
	r := sample()
	r.Cost.RunsSkippedForBudget = 160

	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "160 run(s) were not fetched") {
		t.Error("a sample cut short to protect the rate limit must be disclosed in the comment")
	}
}

func TestMarkdownStaysSilentWhenNothingWasSkipped(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", sample()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "were not fetched") {
		t.Error("caveats for things that did not happen are noise")
	}
}

// The comment that opens with the whole repository's bill is a comment people
// mute. Naming the workflow the pull request actually edits is the difference.
func TestMarkdownLeadsWithTheTouchedWorkflow(t *testing.T) {
	r := sample()
	for i := range r.Workflows {
		if r.Workflows[i].Name == "Test" {
			r.Workflows[i].Touched = true
		}
	}

	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", r); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	if !strings.Contains(out, "This pull request touches **Test**") {
		t.Error("the lead sentence should name the workflow the PR touches")
	}
	// E2E costs more, so it goes first in the table -- but the lead paragraph
	// names E2E as the largest line before the table starts, so the comparison
	// has to be scoped to the table or it reads the wrong occurrence.
	table := out[strings.Index(out, "| Workflow |"):]
	if strings.Index(table, "`Test`") > strings.Index(table, "`E2E`") {
		t.Error("a touched workflow must sort above a costlier untouched one")
	}
}

func TestMarkdownNamesEveryTouchedWorkflow(t *testing.T) {
	r := sample()
	r.Workflows[0].Touched = true
	r.Workflows[1].Touched = true

	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "**E2E**, **Test**") {
		t.Error("both touched workflows should be named, costliest first")
	}
}

// Without -changed there is nothing to scope to, and the comment must fall back
// to the whole-repository framing rather than claiming the PR touches nothing.
func TestMarkdownFallsBackWhenNothingIsMarked(t *testing.T) {
	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "This pull request changes workflow files") {
		t.Error("expected the unscoped lead sentence")
	}
	if strings.Contains(out, "touches **") {
		t.Error("nothing was marked, so nothing should be named as touched")
	}
}

// Regression: the "largest single line" sentence used to read the top table
// row. Once touched workflows started sorting above costlier ones, that made
// the sentence name the wrong workflow and quote the wrong share.
func TestMarkdownNamesTheRealLargestLineNotTheTopRow(t *testing.T) {
	r := sample()
	for i := range r.Workflows {
		if r.Workflows[i].Name == "Test" { // $4.38, against E2E at $9.68
			r.Workflows[i].Touched = true
		}
	}

	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "`E2E` is the largest single line") {
		t.Error("the largest line is E2E even though Test is displayed first")
	}
}

// A monthly figure extrapolated from four days when thirty were requested is a
// different claim from one measured over thirty, and has to read as one.
func TestMarkdownDisclosesATruncatedWindow(t *testing.T) {
	r := sample()
	r.Cost.RequestedWindowDays = 30
	r.Cost.WindowTruncated = true

	var b strings.Builder
	if err := WriteMarkdown(&b, "o/r", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Asked for 30 days") {
		t.Error("a window cut short by the run cap must be disclosed")
	}
}
