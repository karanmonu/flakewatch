// Command flakewatch analyzes GitHub Actions workflow history for a repository
// and reports flaky workflows, zombie (stuck) runs, duration trends, and an
// estimate of what the runs cost at published rates.
//
// Usage:
//
//	flakewatch -repo owner/name [-runs 200] [-since 30d] [-zombie-hours 6] [-cost]
//
// Authentication uses the GITHUB_TOKEN environment variable (a classic PAT or
// fine-grained token with actions:read is sufficient).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/karanmonu/flakewatch/internal/analyze"
	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/report"
)

// version is set at build time by goreleaser via ldflags. It stays "dev" for
// local builds, which is the honest answer for a binary built from a working
// tree that may not match any tag.
var version = "dev"

func main() {
	repo := flag.String("repo", "", "repository in owner/name form (required)")
	runs := flag.Int("runs", 200, "maximum number of recent workflow runs to analyze")
	since := flag.String("since", "", "analyze a fixed time window instead of a run count, e.g. 30d, 2w, 48h (-runs still caps the requests)")
	changed := flag.String("changed", "", "comma-separated workflow paths the caller changed, e.g. .github/workflows/ci.yml (marked in -markdown output)")
	zombieHours := flag.Float64("zombie-hours", 6, "runs in progress longer than this are flagged as zombies")
	withCost := flag.Bool("cost", false, "estimate cost at published rates (one extra API request per run)")
	concurrency := flag.Int("concurrency", 8, "parallel requests when fetching job data")
	jsonOut := flag.Bool("json", false, "emit JSON instead of a terminal report")
	markdownOut := flag.Bool("markdown", false, "emit a Markdown pull request comment")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "error: -repo is required (e.g. -repo grafana/k6)")
		flag.Usage()
		os.Exit(2)
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "warning: GITHUB_TOKEN not set; using unauthenticated requests (60 req/hr limit)")
		if *withCost {
			fmt.Fprintln(os.Stderr, "warning: -cost makes one request per run and will exhaust that limit quickly")
		}
	}

	var window time.Duration
	if *since != "" {
		var err error
		window, err = parseWindow(*since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: -since %q: %v\n", *since, err)
			os.Exit(2)
		}
	}

	client := gh.NewClient(token)

	var (
		workflowRuns   []gh.WorkflowRun
		windowComplete = true
		err            error
	)
	if window > 0 {
		workflowRuns, windowComplete, err = client.ListWorkflowRunsSince(*repo, time.Now().Add(-window), *runs)
	} else {
		workflowRuns, err = client.ListWorkflowRuns(*repo, *runs)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching workflow runs: %v\n", err)
		os.Exit(1)
	}

	result := analyze.Analyze(workflowRuns, analyze.Options{
		ZombieHours:  *zombieHours,
		ChangedPaths: splitPaths(*changed),
	})

	// The Markdown comment is built around cost, so asking for it implies -cost.
	if *markdownOut {
		*withCost = true
	}

	if *withCost {
		ids := make([]int64, 0, len(workflowRuns))
		for _, r := range workflowRuns {
			ids = append(ids, r.ID)
		}
		jobs, err := client.RunAllJobs(*repo, ids, *concurrency)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error fetching job data: %v\n", err)
			os.Exit(1)
		}
		result.Cost = analyze.SummarizeCost(workflowRuns, jobs, result.Workflows)
		if window > 0 {
			result.Cost.RequestedWindowDays = window.Hours() / 24
			result.Cost.WindowTruncated = !windowComplete
		}
	}

	switch {
	case *jsonOut:
		if err := report.WriteJSON(os.Stdout, result); err != nil {
			fmt.Fprintf(os.Stderr, "error writing JSON: %v\n", err)
			os.Exit(1)
		}
	case *markdownOut:
		if err := report.WriteMarkdown(os.Stdout, *repo, result); err != nil {
			fmt.Fprintf(os.Stderr, "error writing Markdown: %v\n", err)
			os.Exit(1)
		}
	default:
		report.WriteTerminal(os.Stdout, *repo, result, *withCost)
	}
}

// splitPaths turns a comma-separated flag value into a slice, dropping empties
// so that a trailing comma or an unset shell variable does not become a path.
func splitPaths(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseWindow accepts the units people actually type for a CI window -- 30d,
// 2w, 48h -- as well as anything time.ParseDuration understands.
//
// Go's own parser stops at hours, so "30d" is a parse error there. Rejecting it
// would be technically correct and useless.
func parseWindow(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty window")
	}

	unit := time.Duration(0)
	switch v[len(v)-1] {
	case 'd', 'D':
		unit = 24 * time.Hour
	case 'w', 'W':
		unit = 7 * 24 * time.Hour
	}

	if unit != 0 {
		n, err := strconv.ParseFloat(v[:len(v)-1], 64)
		if err != nil {
			return 0, fmt.Errorf("not a number before %q", v[len(v)-1:])
		}
		if n <= 0 {
			return 0, fmt.Errorf("must be positive")
		}
		return time.Duration(n * float64(unit)), nil
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("use 30d, 2w, 48h or a Go duration")
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return d, nil
}
