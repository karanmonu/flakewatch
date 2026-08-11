package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
		_ = json.NewEncoder(w).Encode(runsPage{})
	}))

	if _, err := c.ListWorkflowRuns(context.Background(), "o/r", 1); err != nil {
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
		_ = json.NewEncoder(w).Encode(runsPage{})
	}))

	if _, err := c.ListWorkflowRuns(context.Background(), "o/r", 1); err != nil {
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
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: total, WorkflowRuns: runs})
	}))

	got, err := c.ListWorkflowRuns(context.Background(), "o/r", total)
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
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 1000, WorkflowRuns: runs})
	}))

	got, err := c.ListWorkflowRuns(context.Background(), "o/r", 10)
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
			_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 1000})
			return
		}
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 1000, WorkflowRuns: make([]WorkflowRun, 100)})
	}))

	got, err := c.ListWorkflowRuns(context.Background(), "o/r", 1000)
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

	_, err := c.RunJobs(context.Background(), "o/r", 1)
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
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 120, Jobs: make([]Job, n)})
	}))

	jobs, err := c.RunJobs(context.Background(), "o/r", 1)
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
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 7}}})
	}))

	res, err := c.RunAllJobs(context.Background(), "o/r", []int64{1, 2, 3}, 2)
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

	res, err := c.RunAllJobs(context.Background(), "o/r", []int64{1, 2, 3}, 2)
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
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 0})
	}))

	ids := make([]int64, 20)
	for i := range ids {
		ids[i] = int64(i)
	}
	if _, err := c.RunAllJobs(context.Background(), "o/r", ids, limit); err != nil {
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
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 0})
	}))

	if _, err := c.RunAllJobs(context.Background(), "o/r", []int64{1, 2}, 0); err != nil {
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

			_, err := c.RunJobs(context.Background(), "o/r", 1)
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
		_ = json.NewEncoder(w).Encode(runsPage{})
	}))

	if rl := c.RateLimit(); rl.Known {
		t.Error("a fresh client must not claim to know the budget")
	}
	if _, err := c.ListWorkflowRuns(context.Background(), "o/r", 1); err != nil {
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
			_ = json.NewEncoder(w).Encode(runsPage{})
			return
		}
		atomic.AddInt32(&requests, 1)
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 1}}})
	}))

	// One call so the client has seen the headers.
	if _, err := c.ListWorkflowRuns(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}

	ids := make([]int64, 200)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	res, err := c.RunAllJobs(context.Background(), "o/r", ids, 4)
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
		_ = json.NewEncoder(w).Encode(runsPage{})
	}))
	if _, err := c.ListWorkflowRuns(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}

	res, err := c.RunAllJobs(context.Background(), "o/r", []int64{1, 2, 3}, 2)
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
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 1}}})
	}))

	ids := make([]int64, 50)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	res, err := c.RunAllJobs(context.Background(), "o/r", ids, 1)
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
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 2, WorkflowRuns: []WorkflowRun{inside, edge}})
	}))

	runs, complete, err := c.ListWorkflowRunsSince(context.Background(), "o/r", since, 100)
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
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 5000, WorkflowRuns: runs})
	}))

	runs, complete, err := c.ListWorkflowRunsSince(context.Background(), "o/r", now.Add(-30*24*time.Hour), 150)
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
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 5000})
	}))

	runs, complete, err := c.ListWorkflowRunsSince(context.Background(), "o/r", time.Now().Add(-24*time.Hour), 500)
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

// GitHub paginates by offset, so page N returns items [(N-1)*per_page, N*per_page).
// per_page therefore has to stay constant across pages: shrinking it to "only ask
// for what is left" silently changes what page 2 means, re-requesting rows already
// held and skipping the ones after them. The visible symptom is a cost total that
// counts some runs twice.
//
// 150 is the smallest cap that exercises it. 50 is one page and 200 is two clean
// pages, which is why the Action default and the README survey never tripped it.
func TestListWorkflowRunsSinceDoesNotRefetchRowsAcrossPages(t *testing.T) {
	now := time.Now().UTC()
	var perPages []string

	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		perPages = append(perPages, q.Get("per_page"))
		perPage, _ := strconv.Atoi(q.Get("per_page"))
		page, _ := strconv.Atoi(q.Get("page"))

		// Offset pagination, the way GitHub actually does it.
		runs := make([]WorkflowRun, 0, perPage)
		for i := 0; i < perPage; i++ {
			id := int64((page-1)*perPage + i + 1)
			runs = append(runs, WorkflowRun{ID: id, RunStartedAt: now.Add(-time.Duration(id) * time.Minute)})
		}
		_ = json.NewEncoder(w).Encode(runsPage{TotalCount: 5000, WorkflowRuns: runs})
	}))

	runs, _, err := c.ListWorkflowRunsSince(context.Background(), "o/r", now.Add(-30*24*time.Hour), 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 150 {
		t.Fatalf("got %d runs, want the cap of 150", len(runs))
	}

	seen := make(map[int64]bool, len(runs))
	for _, r := range runs {
		if seen[r.ID] {
			t.Fatalf("run %d came back twice; its cost would be counted twice", r.ID)
		}
		seen[r.ID] = true
	}

	for i, p := range perPages {
		if p != "100" {
			t.Errorf("request %d used per_page=%s; it must not change between pages", i+1, p)
		}
	}
}

// A long analysis is hundreds of requests. Ctrl-C should stop it now rather
// than after every in-flight request drains, which means the context has to
// reach the requests themselves and not just wrap the call.
func TestRunAllJobsStopsWhenTheContextIsCancelled(t *testing.T) {
	var served int32
	ctx, cancel := context.WithCancel(context.Background())

	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&served, 1) == 2 {
			cancel()
		}
		_ = json.NewEncoder(w).Encode(jobsPage{TotalCount: 1, Jobs: []Job{{ID: 1}}})
	}))

	ids := make([]int64, 200)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	// Serially, so cancellation lands well before the list is exhausted.
	res, err := c.RunAllJobs(ctx, "o/r", ids, 1)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation should not surface as an unrelated error: %v", err)
	}
	if n := atomic.LoadInt32(&served); n > 50 {
		t.Errorf("made %d requests after cancellation; it should stop handing out work", n)
	}
	_ = res
}

// GitHub explains itself in the response body. A bare "403" leaves the reader
// guessing between a missing scope, SAML enforcement and a repo that is not
// there, so the message travels with the error.
func TestAPIErrorCarriesGitHubsMessage(t *testing.T) {
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration","documentation_url":"https://docs.github.com"}`))
	}))

	_, err := c.RunJobs(context.Background(), "o/r", 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.Message != "Resource not accessible by integration" {
		t.Errorf("Message = %q, want GitHub's own text", apiErr.Message)
	}
	if !strings.Contains(err.Error(), "Resource not accessible by integration") {
		t.Errorf("Error() = %q; the message is the useful part, it has to be in there", err)
	}
}

// One transient 502 in the middle of a several-hundred-request analysis must
// not abort it -- the budget for everything already fetched is already spent.
func TestGetRetriesTransientServerErrors(t *testing.T) {
	var calls int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(runsPage{})
	}))
	c.retryWait = time.Millisecond

	if _, err := c.ListWorkflowRuns(context.Background(), "o/r", 1); err != nil {
		t.Fatalf("a single 502 should be retried, not surfaced: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2", calls)
	}
	if c.Retried() != 1 {
		t.Errorf("Retried() = %d, want 1 -- the report discloses this", c.Retried())
	}
}

// Bounded, because a genuinely down API should fail in seconds. The error that
// finally surfaces is the real one, status and all.
func TestGetGivesUpAfterBoundedRetries(t *testing.T) {
	var calls int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	c.retryWait = time.Millisecond

	_, err := c.ListWorkflowRuns(context.Background(), "o/r", 1)
	if err == nil {
		t.Fatal("expected the 503 to surface eventually")
	}
	if want := int32(maxRetries + 1); calls != want {
		t.Errorf("made %d requests, want %d", calls, want)
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("surfaced error = %v, want the underlying 503", err)
	}
}

// A 404 is routine (job data ages out) and a 401 will not get better with
// patience. Neither is worth a second request.
func TestGetDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		var calls int32
		c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			http.Error(w, "no", status)
		}))
		c.retryWait = time.Millisecond

		if _, err := c.RunJobs(context.Background(), "o/r", 1); err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if calls != 1 {
			t.Errorf("status %d: made %d requests, want 1 -- 4xx must not be retried", status, calls)
		}
	}
}

// Retrying a rate limit would spend exactly the budget the reserve protects.
// The budget logic owns that case; the retry loop must keep its hands off.
func TestGetDoesNotRetryRateLimits(t *testing.T) {
	var calls int32
	c := newTestClient(t, "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "1000")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	c.retryWait = time.Millisecond

	_, err := c.RunJobs(context.Background(), "o/r", 1)
	if !RateLimited(err) {
		t.Fatalf("expected a rate-limit error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1 -- rate limits must not be retried", calls)
	}
}

// The delay arithmetic, tested directly so the tests do not have to sleep
// through it: exponential from the base, raised to Retry-After when GitHub
// asks for longer, capped so no pause outlasts patience.
func TestRetryDelayBacksOffHonoursRetryAfterAndCaps(t *testing.T) {
	c := &Client{retryWait: time.Second}
	tests := []struct {
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{0, 0, time.Second},
		{1, 0, 2 * time.Second},
		{2, 0, 4 * time.Second},
		{0, 7 * time.Second, 7 * time.Second},
		{2, time.Second, 4 * time.Second},
		{3, 5 * time.Minute, maxRetryWait},
	}
	for _, tt := range tests {
		if got := c.retryDelay(tt.attempt, tt.retryAfter); got != tt.want {
			t.Errorf("retryDelay(%d, %v) = %v, want %v", tt.attempt, tt.retryAfter, got, tt.want)
		}
	}
}
