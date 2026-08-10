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

