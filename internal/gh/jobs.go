package gh

import (
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
func (c *Client) RunJobs(repo string, runID int64) ([]Job, error) {
	var all []Job
	for page := 1; ; page++ {
		var p jobsPage
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=100&page=%d", repo, runID, page)
		if err := c.get(path, &p); err != nil {
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
}

// defaultJobsConcurrency is deliberately modest. Jobs cost one request per run,
// so a 200-run analysis is 200 requests: comfortably inside the 5,000/hour
// authenticated limit, but firing them all at once risks GitHub's secondary
// limits on concurrent requests.
const defaultJobsConcurrency = 8

// RunAllJobs fetches jobs for many runs concurrently.
//
// A 404 is counted in Missing and skipped. Any other error aborts, because a
// 401 or a rate-limit response would otherwise silently produce a cost estimate
// that is far too low.
func (c *Client) RunAllJobs(repo string, runIDs []int64, concurrency int) (JobsResult, error) {
	if concurrency <= 0 {
		concurrency = defaultJobsConcurrency
	}

	var (
		mu      sync.Mutex
		byRun   = make(map[int64][]Job, len(runIDs))
		missing int
		firstEr error
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, id := range runIDs {
		mu.Lock()
		stop := firstEr != nil
		mu.Unlock()
		if stop {
			break
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(runID int64) {
			defer wg.Done()
			defer func() { <-sem }()

			jobs, err := c.RunJobs(repo, runID)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				byRun[runID] = jobs
			case NotFound(err):
				missing++
			case firstEr == nil:
				firstEr = err
			}
		}(id)
	}
	wg.Wait()

	if firstEr != nil {
		return JobsResult{}, firstEr
	}
	return JobsResult{ByRun: byRun, Missing: missing}, nil
}

