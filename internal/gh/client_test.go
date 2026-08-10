package gh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at a local test server. baseURL is unexported,
// which is why these tests live in package gh rather than gh_test.
func newTestClient(t *testing.T, token string, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c := NewClient(token)
	c.baseURL = srv.URL
	return c
}

func TestGetSendsExpectedHeaders(t *testing.T) {
	var gotAuth, gotAccept, gotVersion string
	c := newTestClient(t, "secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		json.NewEncoder(w).Encode(runsPage{})
	}))

	if _, err := c.ListWorkflowRuns("o/r", 1); err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret")
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", gotVersion)
	}
}

// An empty token must mean no Authorization header at all, not an empty one.
// "Bearer " with nothing after it is rejected rather than treated as anonymous.
func TestGetOmitsAuthorizationWhenTokenEmpty(t *testing.T) {
	var hadAuth bool
	c := newTestClient(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		json.NewEncoder(w).Encode(runsPage{})
	}))

	if _, err := c.ListWorkflowRuns("o/r", 1); err != nil {
		t.Fatal(err)
	}
	if hadAuth {
		t.Error("unauthenticated client sent an Authorization header")
	}
}

func TestListWorkflowRunsPaginates(t *testing.T) {
	const total = 150
	var pages int32

	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pages, 1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))

		n := 100
		if page == 2 {
			n = 50
		}
		runs := make([]WorkflowRun, n)
		for i := range runs {
			runs[i] = WorkflowRun{ID: int64((page-1)*100 + i)}
		}
		json.NewEncoder(w).Encode(runsPage{TotalCount: total, WorkflowRuns: runs})
	}))

	got, err := c.ListWorkflowRuns("o/r", total)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Errorf("got %d runs, want %d", len(got), total)
	}
	if pages != 2 {
		t.Errorf("made %d requests, want 2", pages)
	}
}

func TestListWorkflowRunsRespectsMax(t *testing.T) {
	var gotPerPage string
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPerPage = r.URL.Query().Get("per_page")
		runs := make([]WorkflowRun, 100)
		json.NewEncoder(w).Encode(runsPage{TotalCount: 1000, WorkflowRuns: runs})
	}))

	got, err := c.ListWorkflowRuns("o/r", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("got %d runs, want 10", len(got))
	}
	if gotPerPage != "10" {
		t.Errorf("per_page = %q, want 10 -- asking for more than needed wastes rate limit", gotPerPage)
	}
}

// A server that keeps returning rows while under-reporting total_count must not
// spin forever.
func TestListWorkflowRunsStopsOnEmptyPage(t *testing.T) {
	var pages int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := atomic.AddInt32(&pages, 1)
		if page > 1 {
			json.NewEncoder(w).Encode(runsPage{TotalCount: 1000})
			return
		}
		json.NewEncoder(w).Encode(runsPage{TotalCount: 1000, WorkflowRuns: make([]WorkflowRun, 100)})
	}))

	got, err := c.ListWorkflowRuns("o/r", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Errorf("got %d runs, want 100", len(got))
	}
	if pages > 3 {
		t.Errorf("made %d requests; an empty page should end pagination", pages)
	}
}

func TestAPIErrorCarriesStatusAndNotFoundIdentifiesIt(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))

	_, err := c.RunJobs("o/r", 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !NotFound(err) {
		t.Errorf("NotFound(%v) = false, want true", err)
	}

	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
}

func TestNotFoundRejectsOtherErrors(t *testing.T) {
	if NotFound(fmt.Errorf("connection refused")) {
		t.Error("a plain error must not be reported as a 404")
	}
	if NotFound(&APIError{StatusCode: http.StatusUnauthorized}) {
		t.Error("a 401 must not be reported as a 404")
	}
}

func TestRunJobsPaginates(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		n := 100
		if page == 2 {
			n = 20
		}
		if page > 2 {
			n = 0
		}
		json.NewEncoder(w).Encode(jobsPage{TotalCount: 120, Jobs: make([]Job, n)})
	}))

	jobs, err := c.RunJobs("o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 120 {
		t.Errorf("got %d jobs, want 120", len(jobs))
	}
}

// Runs whose jobs have aged out return 404. That is routine and must not abort
// the analysis -- it should be counted and reported.
func TestRunAllJobsCountsMissingRatherThanFailing(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/actions/runs/2/jobs" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 7}}})
	}))

	res, err := c.RunAllJobs("o/r", []int64{1, 2, 3}, 2)
	if err != nil {
		t.Fatalf("a 404 on one run must not fail the batch: %v", err)
	}
	if res.Missing != 1 {
		t.Errorf("Missing = %d, want 1", res.Missing)
	}
	if len(res.ByRun) != 2 {
		t.Errorf("collected %d runs, want 2", len(res.ByRun))
	}
}

// The opposite case, and the more dangerous one: a 401 or a rate-limit response
// must surface. Swallowing it would silently produce a cost estimate built from
// a fraction of the data, which is worse than no estimate.
func TestRunAllJobsAbortsOnNonNotFoundError(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	}))

	res, err := c.RunAllJobs("o/r", []int64{1, 2, 3}, 2)
	if err == nil {
		t.Fatal("a 401 must be returned, not swallowed")
	}
	if len(res.ByRun) != 0 {
		t.Error("a failed batch must not return partial results that look complete")
	}
}

func TestRunAllJobsRespectsConcurrencyLimit(t *testing.T) {
	const limit = 3

	var inFlight, maxInFlight int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if n <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		json.NewEncoder(w).Encode(jobsPage{TotalCount: 0})
	}))

	ids := make([]int64, 20)
	for i := range ids {
		ids[i] = int64(i)
	}
	if _, err := c.RunAllJobs("o/r", ids, limit); err != nil {
		t.Fatal(err)
	}

	if maxInFlight > limit {
		t.Errorf("peak concurrency was %d, limit was %d", maxInFlight, limit)
	}
	if maxInFlight < 2 {
		t.Errorf("peak concurrency was %d; requests do not appear to run in parallel at all", maxInFlight)
	}
}

func TestRunAllJobsDefaultsConcurrencyWhenNonPositive(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jobsPage{TotalCount: 0})
	}))

	if _, err := c.RunAllJobs("o/r", []int64{1, 2}, 0); err != nil {
		t.Fatalf("zero concurrency should fall back to the default, not deadlock: %v", err)
	}
}

func TestJobDurationMS(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		job  Job
		want int64
	}{
		{"normal", Job{StartedAt: start, CompletedAt: start.Add(90 * time.Second)}, 90_000},
		{"never started", Job{CompletedAt: start}, 0},
		{"still running", Job{StartedAt: start}, 0},
		{"completed before started", Job{StartedAt: start, CompletedAt: start.Add(-time.Hour)}, 0},
		{"zero length", Job{StartedAt: start, CompletedAt: start}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.DurationMS(); got != tt.want {
				t.Errorf("DurationMS() = %d, want %d", got, tt.want)
			}
		})
	}
}

// GitHub answers 403 for both "you are not allowed" and "you have asked too
// often". Only X-RateLimit-Remaining separates them, and getting this wrong in
// either direction is costly: treat a permission error as rate limiting and the
// tool silently reports partial data; treat rate limiting as a permission error
// and it aborts a run it could have completed.
func TestForbiddenIsRateLimitedOnlyWhenBudgetIsExhausted(t *testing.T) {
	tests := []struct {
		name      string
		remaining string
		status    int
		want      bool
	}{
		{"403 with budget spent", "0", http.StatusForbidden, true},
		{"403 with budget left", "412", http.StatusForbidden, false},
		{"403 with no header at all", "", http.StatusForbidden, false},
		{"429", "0", http.StatusTooManyRequests, true},
		{"404", "500", http.StatusNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.remaining != "" {
					w.Header().Set("X-RateLimit-Remaining", tt.remaining)
					w.Header().Set("X-RateLimit-Limit", "1000")
				}
				w.WriteHeader(tt.status)
			}))

			_, err := c.RunJobs("o/r", 1)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := RateLimited(err); got != tt.want {
				t.Errorf("RateLimited(%v) = %v, want %v", err, got, tt.want)
			}
		})
	}
}

func TestRateLimitedRejectsOtherErrors(t *testing.T) {
	if RateLimited(fmt.Errorf("connection refused")) {
		t.Error("a plain error must not be reported as rate limiting")
	}
}

func TestClientRecordsRateLimitHeaders(t *testing.T) {
	reset := time.Now().Add(42 * time.Minute).Truncate(time.Second)

	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1000")
		w.Header().Set("X-RateLimit-Remaining", "873")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		json.NewEncoder(w).Encode(runsPage{})
	}))

	if rl := c.RateLimit(); rl.Known {
		t.Error("a fresh client must not claim to know the budget")
	}
	if _, err := c.ListWorkflowRuns("o/r", 1); err != nil {
		t.Fatal(err)
	}

	rl := c.RateLimit()
	if !rl.Known {
		t.Fatal("Known = false after a response carrying the headers")
	}
	if rl.Remaining != 873 || rl.Limit != 1000 {
		t.Errorf("got %d/%d, want 873/1000", rl.Remaining, rl.Limit)
	}
	if !rl.Reset.Equal(reset) {
		t.Errorf("Reset = %v, want %v", rl.Reset, reset)
	}
}

// The budget belongs to the whole repository, not to us. Inside Actions the
// GITHUB_TOKEN allows roughly 1,000 requests an hour shared with every other
// workflow, so a large analysis must shrink itself rather than spend the lot.
func TestRunAllJobsTrimsWorkToFitRemainingBudget(t *testing.T) {
	const remaining = 140 // reservedBudget is 100, so 40 runs are affordable

	var requests int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1000")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		if r.URL.Path == "/repos/o/r/actions/runs" {
			json.NewEncoder(w).Encode(runsPage{})
			return
		}
		atomic.AddInt32(&requests, 1)
		json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 1}}})
	}))

	// One call so the client has seen the headers.
	if _, err := c.ListWorkflowRuns("o/r", 1); err != nil {
		t.Fatal(err)
	}

	ids := make([]int64, 200)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	res, err := c.RunAllJobs("o/r", ids, 4)
	if err != nil {
		t.Fatalf("running short of budget is not an error: %v", err)
	}

	const affordable = remaining - reservedBudget
	if len(res.ByRun) != affordable {
		t.Errorf("fetched %d runs, want %d", len(res.ByRun), affordable)
	}
	if res.SkippedForBudget != len(ids)-affordable {
		t.Errorf("SkippedForBudget = %d, want %d", res.SkippedForBudget, len(ids)-affordable)
	}
	if requests > affordable {
		t.Errorf("made %d job requests; the budget allowed %d", requests, affordable)
	}
}

// Below the reserve, the right answer is to fetch nothing and say so, not to
// spend the last requests someone else's deploy may need.
func TestRunAllJobsFetchesNothingBelowTheReserve(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1000")
		w.Header().Set("X-RateLimit-Remaining", "5")
		json.NewEncoder(w).Encode(runsPage{})
	}))
	if _, err := c.ListWorkflowRuns("o/r", 1); err != nil {
		t.Fatal(err)
	}

	res, err := c.RunAllJobs("o/r", []int64{1, 2, 3}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ByRun) != 0 {
		t.Errorf("fetched %d runs with the budget nearly gone", len(res.ByRun))
	}
	if res.SkippedForBudget != 3 {
		t.Errorf("SkippedForBudget = %d, want 3", res.SkippedForBudget)
	}
}

// Hitting the limit mid-flight is different from a 401: we have real data for
// the runs already fetched. Returning it, labelled, beats throwing it away.
func TestRunAllJobsReturnsPartialResultWhenRateLimitedMidway(t *testing.T) {
	var served int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&served, 1) > 3 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Limit", "1000")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 1}}})
	}))

	ids := make([]int64, 50)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	res, err := c.RunAllJobs("o/r", ids, 1)
	if err != nil {
		t.Fatalf("rate limiting must not fail the batch: %v", err)
	}
	if len(res.ByRun) == 0 {
		t.Error("no results returned; the runs fetched before the limit should survive")
	}
	if len(res.ByRun) == len(ids) {
		t.Error("every run was fetched; the client did not stop when told to")
	}
	if res.SkippedForBudget == 0 {
		t.Error("a truncated batch must report how much it skipped")
	}
}
// The window filter GitHub accepts is date-granular, so asking for "since
// 36 hours ago" returns everything from that calendar day, including runs that
// started before the window. Those have to be dropped here or the window is
// silently wider than requested -- which is the whole thing -since exists to fix.
func TestListWorkflowRunsSinceTrimsTheDateGranularEdge(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-36 * time.Hour)

	inside := WorkflowRun{ID: 1, RunStartedAt: now.Add(-time.Hour)}
	edge := WorkflowRun{ID: 2, RunStartedAt: since.Add(-2 * time.Hour)} // same day, older
	var gotCreated string

	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCreated = r.URL.Query().Get("created")
		json.NewEncoder(w).Encode(runsPage{TotalCount: 2, WorkflowRuns: []WorkflowRun{inside, edge}})
	}))

	runs, complete, err := c.ListWorkflowRunsSince("o/r", since, 100)
	if err != nil {
		t.Fatal(err)
	}
	if want := ">=" + since.Format("2006-01-02"); gotCreated != want {
		t.Errorf("created = %q, want %q", gotCreated, want)
	}
	if len(runs) != 1 || runs[0].ID != 1 {
		t.Errorf("got %d runs %v, want only the one inside the window", len(runs), runs)
	}
	if !complete {
		t.Error("reaching a run older than the window means the window was covered")
	}
}

// Hitting the run cap before reaching the start of the window is not an error,
// but the caller has to know: a monthly figure extrapolated from four days is a
// different claim from one measured over thirty.
func TestListWorkflowRunsSinceReportsAnIncompleteWindow(t *testing.T) {
	now := time.Now().UTC()

	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		runs := make([]WorkflowRun, perPage)
		for i := range runs {
			runs[i] = WorkflowRun{ID: int64(i + 1), RunStartedAt: now.Add(-time.Duration(i) * time.Minute)}
		}
		json.NewEncoder(w).Encode(runsPage{TotalCount: 5000, WorkflowRuns: runs})
	}))

	runs, complete, err := c.ListWorkflowRunsSince("o/r", now.Add(-30*24*time.Hour), 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 150 {
		t.Errorf("got %d runs, want the cap of 150", len(runs))
	}
	if complete {
		t.Error("the cap was reached before the window was covered; complete must be false")
	}
}

func TestListWorkflowRunsSinceStopsOnAnEmptyPage(t *testing.T) {
	var pages int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pages, 1)
		json.NewEncoder(w).Encode(runsPage{TotalCount: 5000})
	}))

	runs, complete, err := c.ListWorkflowRunsSince("o/r", time.Now().Add(-24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("got %d runs from an empty page", len(runs))
	}
	if !complete {
		t.Error("nothing left to fetch means the window was covered")
	}
	if pages != 1 {
		t.Errorf("made %d requests; an empty page should end pagination", pages)
	}
}
