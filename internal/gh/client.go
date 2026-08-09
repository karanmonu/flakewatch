// Package gh is a minimal, dependency-free GitHub REST API client scoped to
// what flakewatch needs: listing workflow runs.
package gh

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// Client talks to the GitHub REST API.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// NewClient returns a Client. token may be empty for unauthenticated use.
func NewClient(token string) *Client {
	return &Client{
		token:   token,
		baseURL: "https://api.github.com",
		http:    &http.Client{Timeout: 30 * time.Second},
	}
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
		url := fmt.Sprintf("%s/repos/%s/actions/runs?per_page=%d&page=%d", c.baseURL, repo, perPage, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		var pageData runsPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&pageData)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub API returned %s for %s", resp.Status, url)
		}
		if decodeErr != nil {
			return nil, decodeErr
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

