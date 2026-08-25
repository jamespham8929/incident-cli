// Package github reads deploy history from the GitHub Deployments API.
//
// It is a small hand-rolled client rather than the full SDK because the CLI
// needs two endpoints, and a dependency that pulls in the rest of the GitHub API
// surface to list deployments is not a trade worth making.
package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.github.com"
	// GitHub caps page size at 100. Deployments come back newest first, so most
	// investigations are satisfied by the first page.
	pageSize = 100
	// A two-hour lookback across a handful of repos should never need more than
	// this. The cap exists so a misconfigured `since` cannot walk years of
	// history and exhaust the rate limit.
	maxPages = 5
)

// Client talks to the GitHub REST API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// Deployment is one recorded deploy.
type Deployment struct {
	ID          int64
	SHA         string
	Ref         string
	Environment string
	Description string
	Creator     string
	CreatedAt   time.Time
	URL         string
}

// Comparison is the diff between two commits, used to size a deploy.
type Comparison struct {
	TotalCommits int
	FilesChanged int
}

// NewClient returns a client for api.github.com. An empty token works against
// public repositories at a much lower rate limit.
func NewClient(token string) *Client {
	return &Client{
		baseURL: defaultBaseURL,
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// WithBaseURL points the client at a different API host, for GitHub Enterprise
// or for tests.
func (c *Client) WithBaseURL(base string) *Client {
	c.baseURL = strings.TrimSuffix(base, "/")
	return c
}

// ListDeployments returns deployments created at or after `since`, newest first.
// An empty environment means every environment.
func (c *Client) ListDeployments(owner, repo, environment string, since time.Time) ([]Deployment, error) {
	var out []Deployment

	for page := 1; page <= maxPages; page++ {
		query := url.Values{}
		query.Set("per_page", fmt.Sprint(pageSize))
		query.Set("page", fmt.Sprint(page))
		if environment != "" {
			query.Set("environment", environment)
		}

		var raw []deploymentJSON
		path := fmt.Sprintf("/repos/%s/%s/deployments?%s",
			url.PathEscape(owner), url.PathEscape(repo), query.Encode())
		if err := c.get(path, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}

		// Deployments arrive newest first, so the first one older than the
		// window means every remaining page is older still.
		reachedEnd := false
		for _, d := range raw {
			if d.CreatedAt.Before(since) {
				reachedEnd = true
				break
			}
			out = append(out, Deployment{
				ID:          d.ID,
				SHA:         d.SHA,
				Ref:         d.Ref,
				Environment: d.Environment,
				Description: d.Description,
				Creator:     d.Creator.Login,
				CreatedAt:   d.CreatedAt,
				URL:         d.URL,
			})
		}
		if reachedEnd || len(raw) < pageSize {
			break
		}
	}
	return out, nil
}

// CompareCommits returns the diff size between two commits. GitHub truncates the
// file list at 300 entries, so FilesChanged is a floor on a very large diff
// rather than an exact count.
func (c *Client) CompareCommits(owner, repo, base, head string) (*Comparison, error) {
	if base == "" || head == "" || base == head {
		return nil, fmt.Errorf("compare needs two different commits")
	}
	var raw compareJSON
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(base), url.PathEscape(head))
	if err := c.get(path, &raw); err != nil {
		return nil, err
	}
	return &Comparison{TotalCommits: raw.TotalCommits, FilesChanged: len(raw.Files)}, nil
}

func (c *Client) get(path string, into any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
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
		return fmt.Errorf("GitHub API request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading GitHub API response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("parsing GitHub API response: %w", err)
	}
	return nil
}

// apiError turns a failure into something a responder can act on. Rate limiting
// is called out by name because it is the failure people actually hit, and
// "403 Forbidden" sends them looking at permissions instead.
func apiError(resp *http.Response, body []byte) error {
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		reset := resp.Header.Get("X-RateLimit-Reset")
		return fmt.Errorf("GitHub API rate limit exhausted (resets at unix %s); set GITHUB_TOKEN for a higher limit", reset)
	}
	var errBody struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &errBody)
	if errBody.Message != "" {
		return fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, errBody.Message)
	}
	return fmt.Errorf("GitHub API error %d", resp.StatusCode)
}

type deploymentJSON struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Ref         string    `json:"ref"`
	Environment string    `json:"environment"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	URL         string    `json:"url"`
	Creator     struct {
		Login string `json:"login"`
	} `json:"creator"`
}

type compareJSON struct {
	TotalCommits int `json:"total_commits"`
	Files        []struct {
		Filename string `json:"filename"`
	} `json:"files"`
}
