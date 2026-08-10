package gh

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Job is one job within a workflow run.
//
// We read jobs rather than the run timing endpoint for two reasons, both found
// by calling the API rather than reading the docs:
//
//  1. timing reports zero billable milliseconds for public repositories, which
//     GitHub does not bill. grafana/k6 returns a literally empty billable
//     object. Durations here are wall-clock and always present.
//  2. timing reports only UBUNTU/MACOS/WINDOWS, so a 32-core job is
//     indistinguishable from a 2-core one. Labels give the actual runner.
type Job struct {
	ID          int64     `json:"id"`
	RunID       int64     `json:"run_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Labels      []string  `json:"labels"`
	// Steps is the job's step timeline. It arrives in the same response as the
	// rest of the job, so reading it costs nothing extra -- no additional
	// request, no additional rate-limit budget.
	//
	// Two things to know before trusting it. Timestamps are second-granularity,
	// so a sub-second step reports as zero. And step numbers are not
	// contiguous: skipped steps are omitted entirely, so a job can go 1, 2, 3,
	// 6, 7. Nothing here should count on the index meaning anything.
	Steps []Step `json:"steps"`
}

// Step is one step of a job.
//
// A step is not a billing unit -- GitHub bills the job -- so a step's cost is
// always a share of its job's bill rather than a charge in its own right.
type Step struct {
	Name        string    `json:"name"`
	Number      int       `json:"number"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// Duration is how long the step ran, or zero if it never completed.
func (s Step) Duration() time.Duration {
	if s.StartedAt.IsZero() || s.CompletedAt.IsZero() || !s.CompletedAt.After(s.StartedAt) {
		return 0
	}
	return s.CompletedAt.Sub(s.StartedAt)
}

// DurationMS is the job's wall-clock duration.
//
// Queued-but-never-started jobs have a zero CompletedAt, which would otherwise
// produce a large negative duration.
func (j Job) DurationMS() int64 {
	if j.StartedAt.IsZero() || j.CompletedAt.IsZero() || !j.CompletedAt.After(j.StartedAt) {
		return 0
	}
	return j.CompletedAt.Sub(j.StartedAt).Milliseconds()
}

type jobsPage struct {
	TotalCount int   `json:"total_count"`
	Jobs       []Job `json:"jobs"`
}

// RunJobs fetches every job for a workflow run.
func (c *Client) RunJobs(ctx context.Context, repo string, runID int64) ([]Job, error) {
	var all []Job
	for page := 1; ; page++ {
		var p jobsPage
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=100&page=%d", repo, runID, page)
		if err := c.get(ctx, path, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Jobs...)
		if len(p.Jobs) == 0 || len(all) >= p.TotalCount {
			break
		}
	}
	return all, nil
}

// JobsResult reports what RunAllJobs managed to collect.
type JobsResult struct {
	// ByRun holds the jobs of every run fetched successfully.
	ByRun map[int64][]Job
	// Missing counts runs whose jobs returned 404, normally because they have
	// aged out. Excluded from totals rather than counted as zero.
	Missing int
	// SkippedForBudget counts runs deliberately not fetched because doing so
	// would have consumed more of the API budget than we are willing to take.
	SkippedForBudget int
}

// defaultJobsConcurrency is deliberately modest. Jobs cost one request per run,
// so a 200-run analysis is 200 requests: comfortably inside the 5,000/hour
// authenticated limit, but firing them all at once risks GitHub's secondary
// limits on concurrent requests.
const defaultJobsConcurrency = 8

// reservedBudget is the number of requests we refuse to spend, leaving room for
// whatever else is running in the same repository. Inside GitHub Actions the
// GITHUB_TOKEN budget is shared across every workflow, so a reporting tool that
// drains it can break someone's deploy. A report is never worth that.
const reservedBudget = 100

// RunAllJobs fetches jobs for many runs concurrently, within budget.
//
// A 404 is counted in Missing and skipped. Rate limiting stops further work
// rather than hammering. Any other error aborts, because a 401 would otherwise
// silently produce a cost estimate built from a fraction of the data.
func (c *Client) RunAllJobs(ctx context.Context, repo string, runIDs []int64, concurrency int) (JobsResult, error) {
	if concurrency <= 0 {
		concurrency = defaultJobsConcurrency
	}

	// Trim the work to what the remaining budget can afford before starting.
	skipped := 0
	if rl := c.RateLimit(); rl.Known {
		affordable := rl.Remaining - reservedBudget
		if affordable < 0 {
			affordable = 0
		}
		if affordable < len(runIDs) {
			skipped = len(runIDs) - affordable
			runIDs = runIDs[:affordable]
		}
	}

	var (
		mu      sync.Mutex
		byRun   = make(map[int64][]Job, len(runIDs))
		missing int
		firstEr error
		stopped bool
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, id := range runIDs {
		mu.Lock()
		halt := firstEr != nil || stopped
		mu.Unlock()
		// Cancellation counts as a reason to stop handing out work, so a Ctrl-C
		// does not sit through every remaining request before returning.
		if halt || ctx.Err() != nil {
			skipped++
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(runID int64) {
			defer wg.Done()
			defer func() { <-sem }()

			jobs, err := c.RunJobs(ctx, repo, runID)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				byRun[runID] = jobs
			case NotFound(err):
				missing++
			case RateLimited(err):
				// Stop asking. Report what we have rather than failing outright:
				// a partial answer, clearly labelled, beats no answer.
				stopped = true
			case firstEr == nil:
				firstEr = err
			}
		}(id)
	}
	wg.Wait()

	if firstEr != nil {
		return JobsResult{}, firstEr
	}
	return JobsResult{ByRun: byRun, Missing: missing, SkippedForBudget: skipped}, nil
}
