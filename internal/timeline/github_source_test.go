package timeline

import (
	"errors"
	"testing"
	"time"

	"github.com/jamespham/incident-cli/internal/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDeployments stands in for the GitHub client. It records what it was asked
// so the source's window arithmetic can be checked, not just its output.
type fakeDeployments struct {
	deployments map[string][]github.Deployment
	compares    map[string]*github.Comparison
	listErr     error
	compareErr  error
	since       time.Time
	compareCall int
}

func (f *fakeDeployments) ListDeployments(owner, repo, environment string, since time.Time) ([]github.Deployment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.since = since
	return f.deployments[owner+"/"+repo], nil
}

func (f *fakeDeployments) CompareCommits(owner, repo, base, head string) (*github.Comparison, error) {
	f.compareCall++
	if f.compareErr != nil {
		return nil, f.compareErr
	}
	diff, ok := f.compares[base+"..."+head]
	if !ok {
		return nil, errors.New("no such comparison")
	}
	return diff, nil
}

func deployment(id int64, sha string, at time.Time) github.Deployment {
	return github.Deployment{
		ID: id, SHA: sha, Ref: "main", Environment: "production",
		Description: "ship it", Creator: "octocat", CreatedAt: at,
		URL: "https://api.github.com/d",
	}
}

func TestParseRepoMapping(t *testing.T) {
	cases := []struct {
		spec    string
		want    RepoMapping
		wantErr bool
	}{
		{spec: "acme/checkout", want: RepoMapping{Owner: "acme", Repo: "checkout", Service: "checkout"}},
		{spec: "acme/checkout-service=checkout", want: RepoMapping{Owner: "acme", Repo: "checkout-service", Service: "checkout"}},
		{spec: " acme / checkout = checkout ", want: RepoMapping{Owner: "acme", Repo: "checkout", Service: "checkout"}},
		{spec: "acme/checkout=", want: RepoMapping{Owner: "acme", Repo: "checkout", Service: "checkout"}},
		{spec: "checkout", wantErr: true},
		{spec: "/checkout", wantErr: true},
		{spec: "acme/", wantErr: true},
		{spec: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := ParseRepoMapping(tc.spec)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGitHubSourceBuildsDeployEvents(t *testing.T) {
	fake := &fakeDeployments{
		deployments: map[string][]github.Deployment{
			"acme/checkout-service": {
				deployment(3, "ccc3333", baseTime.Add(-8*time.Minute)),
				deployment(2, "bbb2222", baseTime.Add(-70*time.Minute)),
			},
		},
		compares: map[string]*github.Comparison{
			"bbb2222...ccc3333": {TotalCommits: 4, FilesChanged: 14},
		},
	}
	src := NewGitHubDeploymentSource(fake,
		[]RepoMapping{{Owner: "acme", Repo: "checkout-service", Service: "checkout"}},
		"production", true)

	assert.Equal(t, "github-deployments:production", src.Name())

	events, err := src.Fetch(30*time.Minute, baseTime)
	require.NoError(t, err)
	require.Len(t, events, 1, "only the deploy inside the window is a candidate")

	e := events[0]
	assert.Equal(t, "gh-checkout-service-3", e.ID)
	assert.Equal(t, SourceDeploy, e.Source)
	assert.Equal(t, "checkout", e.Service, "the event carries the service name, not the repo name")
	assert.Equal(t, "checkout ccc3333: ship it", e.Title)
	assert.Equal(t, "acme/checkout-service", e.Metadata["repo"])
	assert.Equal(t, "octocat", e.Metadata["deployed_by"])
	assert.Equal(t, "14", e.Metadata["files_changed"])
	assert.Equal(t, "4", e.Metadata["commits"])
	assert.InDelta(t, magnitudeFromDiff(14, 4), e.Magnitude, 1e-9)
}

// The deploy before the window is the diff base for the oldest one inside it, so
// the source has to ask for more history than it returns.
func TestGitHubSourceFetchesExtraHistoryForTheDiffBase(t *testing.T) {
	fake := &fakeDeployments{deployments: map[string][]github.Deployment{"acme/checkout": nil}}
	src := NewGitHubDeploymentSource(fake,
		[]RepoMapping{{Owner: "acme", Repo: "checkout", Service: "checkout"}}, "production", true)

	_, err := src.Fetch(30*time.Minute, baseTime)
	require.NoError(t, err)
	assert.Equal(t, baseTime.Add(-60*time.Minute), fake.since)
}

func TestGitHubSourceSkipsDeploysAfterDetection(t *testing.T) {
	fake := &fakeDeployments{
		deployments: map[string][]github.Deployment{
			"acme/checkout": {
				deployment(4, "ddd4444", baseTime.Add(5*time.Minute)),
				deployment(3, "ccc3333", baseTime.Add(-8*time.Minute)),
			},
		},
	}
	src := NewGitHubDeploymentSource(fake,
		[]RepoMapping{{Owner: "acme", Repo: "checkout", Service: "checkout"}}, "production", false)

	events, err := src.Fetch(time.Hour, baseTime)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "gh-checkout-3", events[0].ID)
}

// A comparison that fails should cost the event its magnitude, not its place on
// the timeline. Losing a deploy from an investigation is far worse than losing
// one of its four scoring signals.
func TestGitHubSourceSurvivesAFailedComparison(t *testing.T) {
	fake := &fakeDeployments{
		deployments: map[string][]github.Deployment{
			"acme/checkout": {
				deployment(3, "ccc3333", baseTime.Add(-8*time.Minute)),
				deployment(2, "bbb2222", baseTime.Add(-40*time.Minute)),
			},
		},
		compareErr: errors.New("boom"),
	}
	src := NewGitHubDeploymentSource(fake,
		[]RepoMapping{{Owner: "acme", Repo: "checkout", Service: "checkout"}}, "production", true)

	events, err := src.Fetch(time.Hour, baseTime)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Zero(t, events[0].Magnitude)
	assert.NotContains(t, events[0].Metadata, "files_changed")
}

func TestGitHubSourceSkipsSizingWhenDisabled(t *testing.T) {
	fake := &fakeDeployments{
		deployments: map[string][]github.Deployment{
			"acme/checkout": {
				deployment(3, "ccc3333", baseTime.Add(-8*time.Minute)),
				deployment(2, "bbb2222", baseTime.Add(-40*time.Minute)),
			},
		},
	}
	src := NewGitHubDeploymentSource(fake,
		[]RepoMapping{{Owner: "acme", Repo: "checkout", Service: "checkout"}}, "production", false)

	_, err := src.Fetch(time.Hour, baseTime)
	require.NoError(t, err)
	assert.Zero(t, fake.compareCall)
}

func TestGitHubSourceFailsLoudly(t *testing.T) {
	fake := &fakeDeployments{listErr: errors.New("401 Unauthorized")}
	src := NewGitHubDeploymentSource(fake,
		[]RepoMapping{{Owner: "acme", Repo: "checkout", Service: "checkout"}}, "production", true)

	_, err := src.Fetch(time.Hour, baseTime)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/checkout")
}

func TestGitHubSourceNameWithoutEnvironment(t *testing.T) {
	src := NewGitHubDeploymentSource(&fakeDeployments{}, nil, "", false)
	assert.Equal(t, "github-deployments", src.Name())
}

func TestDeployTitleFallsBackToTheRef(t *testing.T) {
	d := github.Deployment{SHA: "abcdef1234", Ref: "release-9", Description: "  "}
	assert.Equal(t, "checkout abcdef1: deploy release-9", deployTitle("checkout", d))

	short := github.Deployment{SHA: "abc", Ref: "release-9", Description: "hotfix"}
	assert.Equal(t, "checkout release-9: hotfix", deployTitle("checkout", short))
}

func TestMagnitudeFromDiff(t *testing.T) {
	cases := []struct {
		name    string
		files   int
		commits int
		want    float64
	}{
		{"nothing known", 0, 0, 0},
		{"falls back to commits when files are missing", 0, 14, magnitudeFromDiff(14, 0)},
		{"a one-file change is a weak signal", 1, 1, 0.13},
		{"a 200-file change saturates", 200, 40, 1.0},
		{"beyond the reference point stays clamped", 5000, 900, 1.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, magnitudeFromDiff(tc.files, tc.commits), 0.01)
		})
	}

	// The curve has to be monotonic, or a bigger deploy could score lower than a
	// smaller one.
	previous := -1.0
	for _, files := range []int{1, 2, 5, 10, 25, 50, 100, 200} {
		got := magnitudeFromDiff(files, 0)
		assert.Greater(t, got, previous)
		previous = got
	}
}
