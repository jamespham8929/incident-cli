package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// route serves canned responses keyed by request path, and records the queries
// it was asked, so pagination and filtering can be asserted rather than assumed.
type route struct {
	responses map[string]string
	status    int
	headers   map[string]string
	requests  []url.Values
}

func serve(t *testing.T, r *route) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requests = append(r.requests, req.URL.Query())
		for key, value := range r.headers {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "application/json")
		status := r.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		body, ok := r.responses[req.URL.Path]
		if !ok {
			body = r.responses["*"]
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient("test-token").WithBaseURL(srv.URL)
}

func deploymentJSONBody(id int, sha, createdAt string) string {
	return fmt.Sprintf(`{"id":%d,"sha":%q,"ref":"main","environment":"production",
	  "description":"ship it","created_at":%q,"url":"https://api.github.com/d/%d",
	  "creator":{"login":"octocat"}}`, id, sha, createdAt, id)
}

func TestListDeployments(t *testing.T) {
	r := &route{responses: map[string]string{
		"/repos/acme/checkout/deployments": "[" +
			deploymentJSONBody(3, "ccc3333", "2026-01-15T11:52:00Z") + "," +
			deploymentJSONBody(2, "bbb2222", "2026-01-15T11:00:00Z") + "]",
	}}
	c := serve(t, r)

	since := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	deployments, err := c.ListDeployments("acme", "checkout", "production", since)
	require.NoError(t, err)
	require.Len(t, deployments, 2)

	assert.Equal(t, int64(3), deployments[0].ID)
	assert.Equal(t, "ccc3333", deployments[0].SHA)
	assert.Equal(t, "production", deployments[0].Environment)
	assert.Equal(t, "ship it", deployments[0].Description)
	assert.Equal(t, "octocat", deployments[0].Creator)
	assert.Equal(t, "main", deployments[0].Ref)

	require.Len(t, r.requests, 1)
	assert.Equal(t, "production", r.requests[0].Get("environment"))
	assert.Equal(t, "100", r.requests[0].Get("per_page"))
}

// Deployments come back newest first, so the first one older than the window
// means the rest of the page and every later page is older too.
func TestListDeploymentsStopsAtWindowEdge(t *testing.T) {
	r := &route{responses: map[string]string{
		"/repos/acme/checkout/deployments": "[" +
			deploymentJSONBody(3, "ccc3333", "2026-01-15T11:52:00Z") + "," +
			deploymentJSONBody(1, "aaa1111", "2026-01-14T09:00:00Z") + "]",
	}}
	c := serve(t, r)

	since := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	deployments, err := c.ListDeployments("acme", "checkout", "production", since)
	require.NoError(t, err)
	require.Len(t, deployments, 1)
	assert.Equal(t, int64(3), deployments[0].ID)
	assert.Len(t, r.requests, 1, "a short page should not trigger another request")
}

func TestListDeploymentsWithoutEnvironmentFilter(t *testing.T) {
	r := &route{responses: map[string]string{"*": "[]"}}
	c := serve(t, r)

	_, err := c.ListDeployments("acme", "checkout", "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, r.requests, 1)
	assert.Empty(t, r.requests[0].Get("environment"))
}

func TestCompareCommits(t *testing.T) {
	r := &route{responses: map[string]string{
		"/repos/acme/checkout/compare/bbb2222...ccc3333": `{"total_commits":4,
		  "files":[{"filename":"a.go"},{"filename":"b.go"},{"filename":"c.go"}]}`,
	}}
	c := serve(t, r)

	diff, err := c.CompareCommits("acme", "checkout", "bbb2222", "ccc3333")
	require.NoError(t, err)
	assert.Equal(t, 4, diff.TotalCommits)
	assert.Equal(t, 3, diff.FilesChanged)
}

func TestCompareCommitsRejectsUnusableInput(t *testing.T) {
	c := NewClient("")
	for _, tc := range []struct{ base, head string }{
		{"", "abc"},
		{"abc", ""},
		{"abc", "abc"},
	} {
		_, err := c.CompareCommits("acme", "checkout", tc.base, tc.head)
		assert.Error(t, err)
	}
}

func TestAPIErrorsAreReadable(t *testing.T) {
	t.Run("message from the body", func(t *testing.T) {
		c := serve(t, &route{
			status:    http.StatusNotFound,
			responses: map[string]string{"*": `{"message":"Not Found"}`},
		})
		_, err := c.ListDeployments("acme", "nope", "production", time.Now())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "GitHub API error 404")
		assert.Contains(t, err.Error(), "Not Found")
	})

	t.Run("rate limit is named", func(t *testing.T) {
		c := serve(t, &route{
			status:    http.StatusForbidden,
			headers:   map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1800000000"},
			responses: map[string]string{"*": `{"message":"API rate limit exceeded"}`},
		})
		_, err := c.ListDeployments("acme", "checkout", "production", time.Now())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate limit exhausted")
		assert.Contains(t, err.Error(), "GITHUB_TOKEN")
	})

	t.Run("unparseable body", func(t *testing.T) {
		c := serve(t, &route{responses: map[string]string{"*": `not json`}})
		_, err := c.ListDeployments("acme", "checkout", "production", time.Now())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing GitHub API response")
	})

	t.Run("unreachable host", func(t *testing.T) {
		c := NewClient("").WithBaseURL("http://127.0.0.1:1")
		_, err := c.ListDeployments("acme", "checkout", "production", time.Now())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "GitHub API request")
	})
}

func TestTokenIsSentWhenSet(t *testing.T) {
	var auth, apiVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auth = req.Header.Get("Authorization")
		apiVersion = req.Header.Get("X-GitHub-Api-Version")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)

	_, err := NewClient("secret").WithBaseURL(srv.URL).
		ListDeployments("acme", "checkout", "production", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret", auth)
	assert.Equal(t, "2022-11-28", apiVersion)
}

func TestNoTokenSendsNoAuthorizationHeader(t *testing.T) {
	var hasAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, hasAuth = req.Header["Authorization"]
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)

	_, err := NewClient("").WithBaseURL(srv.URL).
		ListDeployments("acme", "checkout", "production", time.Now())
	require.NoError(t, err)
	assert.False(t, hasAuth, "an empty token should mean no header, not an empty one")
}
