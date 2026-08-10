// Package gh is a minimal, dependency-free GitHub REST API client scoped to
// what flakewatch needs: listing workflow runs and their jobs.
package gh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// WorkflowRun is the subset of the GitHub Actions run object we analyze.
type WorkflowRun struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	HeadBranch   string    `json:"head_branch"`
	Status       string    `json:"status"`     // queued | in_progress | completed
	Conclusion   string    `json:"conclusion"` // success | failure | cancelled | ...
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
	// RateLimited is true for a 429, or a 403 that came with an exhausted
	// rate-limit budget. GitHub uses 403 for both permissions and rate limits,
	// so the header is the only way to tell them apart.
	RateLimited bool
}

func (e *APIError) Error() string {
	if e.RateLimited {
		return fmt.Sprintf("GitHub API rate limit exhausted (%s) for %s", e.Status, e.URL)
	}
	return fmt.Sprintf("GitHub API returned %s for %s", e.Status, e.URL)
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

	mu   sync.Mutex
	rate RateLimit
}

// NewClient returns a Client. token may be empty for unauthenticated use.
func NewClient(token string) *Client {
	return &Client{
		token:   token,
		baseURL: "https://api.github.com",
		http:    &http.Client{Timeout: 30 * time.Second},
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

// get performs a GET against path and decodes the JSON body into out.
func (c *Client) get(path string, out any) error {
	url := c.baseURL + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
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
		return &APIError{
			StatusCode:  resp.StatusCode,
			Status:      resp.Status,
			URL:         url,
			RateLimited: limited,
		}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ListWorkflowRuns fetches up to max recent workflow runs for repo ("owner/name"),
// newest first, paginating as needed (100 per page).
func (c *Client) ListWorkflowRuns(repo string, max int) ([]WorkflowRun, error) {
	var all []WorkflowRun
	perPage := 100
	if max < perPage {
		perPage = max
	}
	for page := 1; len(all) < max; page++ {
		var pageData runsPage
		path := fmt.Sprintf("/repos/%s/actions/runs?per_page=%d&page=%d", repo, perPage, page)
		if err := c.get(path, &pageData); err != nil {
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
	if len(all) > max {
		all = all[:max]
	}
	return all, nil
}
