// Command flakewatch analyzes GitHub Actions workflow history for a repository
// and reports flaky workflows, zombie (stuck) runs, duration trends, and an
// estimate of what the runs cost at published rates.
//
// Usage:
//
//	flakewatch -repo owner/name [-runs 200] [-since 30d] [-zombie-hours 6] [-cost] [-rates rates.json]
//
// Authentication uses the GITHUB_TOKEN environment variable (a classic PAT or
// fine-grained token with actions:read is sufficient).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/karanmonu/flakewatch/internal/analyze"
	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/pricing"
	"github.com/karanmonu/flakewatch/internal/report"
	"github.com/karanmonu/flakewatch/internal/store"
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
	ratesFile := flag.String("rates", "", "JSON file of runner label to USD per minute, for self-hosted and unrecognised labels")
	noCache := flag.Bool("no-cache", false, "do not read or write the local run history")
	cacheDir := flag.String("cache-dir", "", "where to keep local run history (default: your OS cache directory)")
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

	// Load the rate file before spending any API budget. A typo in it should
	// cost the user a second, not two hundred requests and then an error.
	var rates pricing.Overrides
	if *ratesFile != "" {
		var err error
		rates, err = pricing.LoadOverrides(*ratesFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if !*withCost {
			fmt.Fprintln(os.Stderr, "warning: -rates has no effect without -cost")
		}
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

	// A run of -runs 800 is 800 sequential-ish HTTP requests. Ctrl-C should stop
	// it now, not after the in-flight ones drain, so the signal cancels the same
	// context every request is built from.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := gh.NewClient(token)

	// History outlives the process. A completed run never changes, so jobs
	// fetched by any earlier invocation are still correct, and the window a
	// report can cover becomes the union of every run ever fetched rather than
	// whatever this one invocation could afford.
	var history *store.Cache
	if !*noCache {
		dir := *cacheDir
		if dir == "" {
			if d, err := store.DefaultDir(); err == nil {
				dir = d
			}
		}
		if dir != "" {
			var err error
			history, err = store.Open(dir, *repo)
			if err != nil {
				// A broken cache must never stop a report. It is an
				// optimisation; the API is the source of truth.
				fmt.Fprintf(os.Stderr, "warning: ignoring local history: %v\n", err)
				history = nil
			}
		}
	}

	var (
		workflowRuns   []gh.WorkflowRun
		windowComplete = true
		err            error
	)
	if window > 0 {
		workflowRuns, windowComplete, err = client.ListWorkflowRunsSince(ctx, *repo, time.Now().Add(-window), *runs)
	} else {
		workflowRuns, err = client.ListWorkflowRuns(ctx, *repo, *runs)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching workflow runs: %v\n", err)
		os.Exit(exitCode(err))
	}

	// Runs this process did not fetch, but an earlier one did. Merged before
	// analysis so every table sees the same, wider history.
	var fromHistory int
	if history != nil {
		workflowRuns, fromHistory = mergeHistory(workflowRuns, history, window)
		// History can complete a window the API list alone could not reach.
		// Recomputing rather than trusting the earlier flag is the difference
		// between "we could only see 13 days" and an honest 30.
		if fromHistory > 0 && len(workflowRuns) > 0 {
			oldest := workflowRuns[len(workflowRuns)-1].RunStartedAt
			if !oldest.IsZero() && !oldest.After(time.Now().Add(-window)) {
				windowComplete = true
			}
		}
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
		// Only ask the API for runs whose jobs are not already on disk. This is
		// the whole saving: one request per run, and a run already measured is
		// measured forever.
		ids := make([]int64, 0, len(workflowRuns))
		for _, r := range workflowRuns {
			if history != nil && history.Has(r.ID) {
				continue
			}
			ids = append(ids, r.ID)
		}
		// Runs priced without asking GitHub. Counted separately from
		// fromHistory, which is runs added to the *window*: on a repeat run of
		// the same command the window does not grow at all, yet nearly every
		// run is served from disk. Reporting only the first number meant a
		// report assembled almost entirely from a local file read exactly like
		// one fetched fresh.
		fromCache := len(workflowRuns) - len(ids)
		jobs, err := client.RunAllJobs(ctx, *repo, ids, *concurrency)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error fetching job data: %v\n", err)
			os.Exit(exitCode(err))
		}
		if history != nil {
			jobs = mergeJobs(jobs, workflowRuns, history)
		}
		result.Cost = analyze.SummarizeCost(workflowRuns, jobs, result.Workflows, rates)
		result.Cost.RunsFromHistory = fromHistory
		result.Cost.RunsFromCache = fromCache
		if window > 0 {
			result.Cost.RequestedWindowDays = window.Hours() / 24
			result.Cost.WindowTruncated = !windowComplete
			// What -runs would have covered the ask, at the density observed.
			if !windowComplete && result.Cost.WindowDays > 0 && result.Cost.RunsPriced > 0 {
				need := float64(result.Cost.RunsPriced) * result.Cost.RequestedWindowDays / result.Cost.WindowDays
				result.Cost.RunsForFullWindow = int(math.Ceil(need))
			}
		}
	}

	// Written before the report so an interrupt while printing still keeps the
	// history this run paid for. A failure here costs nothing but the saving:
	// warn on stderr and let the report go out on stdout regardless.
	if history != nil {
		if err := history.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save local history: %v\n", err)
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

// exitCode distinguishes "you interrupted this" from "this went wrong".
//
// 130 is the conventional shell code for a process ended by SIGINT. A script
// wrapping flakewatch can then tell a deliberate Ctrl-C apart from a genuine
// failure, which matters if it is deciding whether to retry.
func exitCode(err error) int {
	if errors.Is(err, context.Canceled) {
		return 130
	}
	return 1
}

// mergeHistory folds stored runs into the freshly listed ones.
//
// The API list is authoritative for what exists now; history contributes runs
// that scrolled past whatever -runs could afford. Returns the union, newest
// first, and how many came only from history.
//
// Without -since there is no window to fill, so history is used to avoid
// re-fetching jobs but not to widen the sample. Silently reporting on 900 runs
// when the user asked for 200 would be a different measurement than the one
// they requested, and a number that changes because of a file on disk is worse
// than a number that is merely small.
func mergeHistory(fetched []gh.WorkflowRun, history *store.Cache, window time.Duration) ([]gh.WorkflowRun, int) {
	if window <= 0 {
		return fetched, 0
	}

	seen := make(map[int64]struct{}, len(fetched))
	for _, r := range fetched {
		seen[r.ID] = struct{}{}
	}

	var added int
	for _, r := range history.Runs(time.Now().Add(-window)) {
		if _, ok := seen[r.ID]; ok {
			continue
		}
		fetched = append(fetched, r)
		seen[r.ID] = struct{}{}
		added++
	}

	sort.Slice(fetched, func(i, j int) bool {
		if !fetched[i].RunStartedAt.Equal(fetched[j].RunStartedAt) {
			return fetched[i].RunStartedAt.After(fetched[j].RunStartedAt)
		}
		return fetched[i].ID > fetched[j].ID
	})
	return fetched, added
}

// mergeJobs fills in job data that came from disk rather than the API, and
// records anything newly fetched.
//
// Runs whose jobs are in neither place stay counted as missing, exactly as
// before: a run whose logs aged out is excluded from totals rather than priced
// at zero.
func mergeJobs(jobs gh.JobsResult, runs []gh.WorkflowRun, history *store.Cache) gh.JobsResult {
	if jobs.ByRun == nil {
		jobs.ByRun = make(map[int64][]gh.Job)
	}
	for _, r := range runs {
		if fetched, ok := jobs.ByRun[r.ID]; ok {
			history.Put(r, fetched)
			continue
		}
		if stored, ok := history.Jobs(r.ID); ok {
			// Not a correction to jobs.Missing: that counts 404s among the runs
			// actually requested, and a cached run was never requested, so it
			// was never in that tally to begin with.
			jobs.ByRun[r.ID] = stored
		}
	}
	return jobs
}
