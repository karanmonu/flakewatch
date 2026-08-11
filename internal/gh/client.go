// Package gh is a minimal, dependency-free GitHub REST API client scoped to
// what flakewatch needs: listing workflow runs and their jobs.
package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WorkflowRun is the subset of the GitHub Actions run object we analyze.
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | cancelled | ...
	// Event is what triggered the run: push, pull_request, schedule, and so on.
	// It matters because the same observation means different things per event
	// -- two overlapping runs on a pull request branch are waste, two on the
	// default branch are one build per commit, which is the point.
	Event string `json:"event"`
	// Path is the workflow file this run came from, e.g.
	// ".github/workflows/ci.yml". Names collide and get renamed; the path is
	// what a pull request diff actually gives you.
	Path         string    `json:"path"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	HTMLURL      string    `json:"html_url"`
}

type runsPage struct {
	TotalCount   int           `json:"total_count"`
	WorkflowRuns []WorkflowRun `json:"workflow_runs"`
}

// APIError is a non-2xx response from the GitHub API.
//
// Callers need to tell three cases apart: 404 (routine — job data ages out),
// rate limiting (back off, do not retry in a loop), and everything else.
type APIError struct {
	StatusCode int
	Status     string
	URL        string
	// Message is whatever GitHub put in the response body. Without it a 403 is
	// just "403", and the reader has to guess between a missing scope, SAML
	// enforcement, and a repository that does not exist.
	Message string
	// RateLimited is true for a 429, or a 403 that came with an exhausted
	// rate-limit budget. GitHub uses 403 for both permissions and rate limits,
	// so the header is the only way to tell them apart.
	RateLimited bool
	// retryAfter is GitHub's Retry-After header, when the response carried
	// one. Consumed by the retry loop; not part of the error's public face.
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	if e.RateLimited {
		fmt.Fprintf(&b, "GitHub API rate limit exhausted (%s) for %s", e.Status, e.URL)
	} else {
		fmt.Fprintf(&b, "GitHub API returned %s for %s", e.Status, e.URL)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// NotFound reports whether err is a 404 from the GitHub API.
func NotFound(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.StatusCode == http.StatusNotFound
}

// RateLimited reports whether err was caused by exhausting the API budget.
func RateLimited(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.RateLimited
}

// RateLimit is what the API last told us about our remaining budget.
//
// This matters more than it looks: inside GitHub Actions the automatic
// GITHUB_TOKEN is limited to roughly 1,000 requests per hour per repository,
// shared with every other workflow in that repository. A tool that spends the
// budget without looking can break someone else's deploy.
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
	// Known is false until a response has actually carried the headers.
	Known bool
}

// Client talks to the GitHub REST API.
type Client struct {
	token   string
	baseURL string
	http    *http.Client

	mu      sync.Mutex
	rate    RateLimit
	retried int

	// retryWait is the base backoff before the first retry. Tests shrink it;
	// nothing else should.
	retryWait time.Duration
}

// NewClient returns a Client. token may be empty for unauthenticated use.
func NewClient(token string) *Client {
	return &Client{
		token:     token,
		baseURL:   "https://api.github.com",
		http:      &http.Client{Timeout: 30 * time.Second},
		retryWait: time.Second,
	}
}

// RateLimit returns the budget reported by the most recent response.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate
}

func (c *Client) recordRate(h http.Header) {
	rem, err1 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	lim, err2 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	if err1 != nil || err2 != nil {
		return
	}
	var reset time.Time
	if sec, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		reset = time.Unix(sec, 0)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rate = RateLimit{Limit: lim, Remaining: rem, Reset: reset, Known: true}
}

// maxRetries bounds how many times one request is re-issued after a transient
// server error. Three is enough to ride out a blip and small enough that a
// genuinely down API fails in seconds rather than minutes.
const maxRetries = 3

// maxRetryWait caps the pause before a retry, whatever Retry-After says.
const maxRetryWait = 30 * time.Second

// transientStatus reports whether a status code is worth retrying: server-side
// failures that routinely clear on their own. No 4xx belongs here -- a bad
// token does not get better with patience -- and rate limits are excluded
// explicitly, because retrying those would spend exactly the budget this
// client exists to protect.
func transientStatus(code int) bool {
	switch code {
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// get performs a GET against path and decodes the JSON body into out.
//
// Transient server errors are retried with exponential backoff, honouring
// Retry-After when GitHub sends one. One 502 in the middle of a
// several-hundred-request analysis used to abort the whole thing, after the
// API budget for everything already fetched had been spent -- the worst
// possible exchange rate. Each retry is itself a real request against the same
// shared budget, which is one more reason the attempts are bounded.
func (c *Client) get(ctx context.Context, path string, out any) error {
	url := c.baseURL + path
	for attempt := 0; ; attempt++ {
		err := c.getOnce(ctx, url, out)
		if err == nil {
			return nil
		}
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.RateLimited || !transientStatus(apiErr.StatusCode) || attempt == maxRetries {
			return err
		}

		timer := time.NewTimer(c.retryDelay(attempt, apiErr.retryAfter))
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		c.noteRetry()
	}
}

// retryDelay is the pause before re-issuing attempt's request: exponential
// backoff from the client's base wait, raised to Retry-After when GitHub asked
// for longer, and capped so no single pause outlasts patience.
func (c *Client) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	wait := c.retryWait << attempt
	if retryAfter > wait {
		wait = retryAfter
	}
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	return wait
}

func (c *Client) noteRetry() {
	c.mu.Lock()
	c.retried++
	c.mu.Unlock()
}

// Retried reports how many requests were re-issued after transient server
// errors. The report discloses it, because an analysis that limped through
// network weather should not read identically to one that did not.
func (c *Client) Retried() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retried
}

// getOnce is a single request: no retries, no waiting.
func (c *Client) getOnce(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", url, err)
	}
	defer resp.Body.Close()

	c.recordRate(resp.Header)

	if resp.StatusCode != http.StatusOK {
		limited := resp.StatusCode == http.StatusTooManyRequests
		// GitHub answers 403 for both "you may not" and "you have asked too
		// often". Only the remaining-count header separates them.
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			limited = true
		}
		var retryAfter time.Duration
		if sec, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && sec > 0 {
			retryAfter = time.Duration(sec) * time.Second
		}
		return &APIError{
			StatusCode:  resp.StatusCode,
			Status:      resp.Status,
			URL:         url,
			RateLimited: limited,
			Message:     errorMessage(resp.Body),
			retryAfter:  retryAfter,
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	return nil
}

// maxErrorBody caps how much of an error response we read. GitHub's are small;
// anything large is a proxy or a captive portal, and quoting all of it into
// somebody's terminal helps nobody.
const maxErrorBody = 4 << 10

// errorMessage pulls GitHub's own explanation out of an error response.
//
// It never returns an error of its own. This runs on a path that already has
// one, and failing to read the body is not more interesting than the status.
func errorMessage(r io.Reader) string {
	body, err := io.ReadAll(io.LimitReader(r, maxErrorBody))
	if err != nil || len(body) == 0 {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Message != "" {
		return payload.Message
	}
	return strings.TrimSpace(string(body))
}

// ListWorkflowRuns fetches up to limit recent workflow runs for repo
// ("owner/name"), newest first, paginating as needed (100 per page).
func (c *Client) ListWorkflowRuns(ctx context.Context, repo string, limit int) ([]WorkflowRun, error) {
	var all []WorkflowRun
	perPage := 100
	if limit < perPage {
		perPage = limit
	}
	for page := 1; len(all) < limit; page++ {
		var pageData runsPage
		path := fmt.Sprintf("/repos/%s/actions/runs?per_page=%d&page=%d", repo, perPage, page)
		if err := c.get(ctx, path, &pageData); err != nil {
			return nil, err
		}
		if len(pageData.WorkflowRuns) == 0 {
			break
		}
		all = append(all, pageData.WorkflowRuns...)
		if len(all) >= pageData.TotalCount {
			break
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// ListWorkflowRunsSince fetches runs started on or after since, newest first,
// up to limit runs.
//
// It reports complete=false when limit was reached before the window was covered,
// which matters: the caller asked about 30 days and is holding 4 days of data,
// and a monthly projection built from that should say so.
//
// The created filter GitHub accepts is date-granular, so the last page is
// trimmed here to the exact instant.
func (c *Client) ListWorkflowRunsSince(ctx context.Context, repo string, since time.Time, limit int) ([]WorkflowRun, bool, error) {
	created := url.QueryEscape(">=" + since.UTC().Format("2006-01-02"))

	var all []WorkflowRun
	reachedWindow := false

	// per_page stays fixed for every page. GitHub paginates by offset, so page N
	// returns items [(N-1)*per_page, N*per_page). Shrinking per_page on later
	// pages to "only ask for what is left" therefore re-requests rows already
	// seen and skips the ones after them: with limit=150 the second page would
	// come back as items 51-100 again, double-counting fifty runs' cost and
	// losing runs 101-150 entirely. Over-fetch and trim at the end instead.
	perPage := 100
	if limit < perPage {
		perPage = limit
	}

	for page := 1; len(all) < limit; page++ {

		var pageData runsPage
		path := fmt.Sprintf("/repos/%s/actions/runs?per_page=%d&page=%d&created=%s", repo, perPage, page, created)
		if err := c.get(ctx, path, &pageData); err != nil {
			return nil, false, err
		}
		if len(pageData.WorkflowRuns) == 0 {
			reachedWindow = true // nothing older left to ask for
			break
		}

		for _, r := range pageData.WorkflowRuns {
			if r.RunStartedAt.Before(since) {
				// The date-granular filter let in part of the day before.
				reachedWindow = true
				continue
			}
			all = append(all, r)
		}
		if reachedWindow || len(all) >= pageData.TotalCount {
			reachedWindow = true
			break
		}
	}

	if len(all) > limit {
		all = all[:limit]
	}
	return all, reachedWindow, nil
}
