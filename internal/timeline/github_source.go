package timeline

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jamespham/incident-cli/internal/github"
)

// DeploymentLister is the slice of the GitHub client this source uses. Keeping
// it narrow means the tests fake two methods rather than an API.
type DeploymentLister interface {
	ListDeployments(owner, repo, environment string, since time.Time) ([]github.Deployment, error)
	CompareCommits(owner, repo, base, head string) (*github.Comparison, error)
}

// RepoMapping ties a GitHub repository to the service name that appears on
// alerts and in the dependency graph. They are usually the same word and
// sometimes not, which is why it is a mapping rather than an assumption.
type RepoMapping struct {
	Owner   string
	Repo    string
	Service string
}

// ParseRepoMapping reads "owner/repo" or "owner/repo=service". Without the
// suffix the service name is the repository name.
func ParseRepoMapping(spec string) (RepoMapping, error) {
	repoPart, service, hasService := strings.Cut(spec, "=")
	owner, repo, ok := strings.Cut(strings.TrimSpace(repoPart), "/")
	owner, repo = strings.TrimSpace(owner), strings.TrimSpace(repo)
	if !ok || owner == "" || repo == "" {
		return RepoMapping{}, fmt.Errorf("expected owner/repo[=service], got %q", spec)
	}
	service = strings.TrimSpace(service)
	if !hasService || service == "" {
		service = repo
	}
	return RepoMapping{Owner: owner, Repo: repo, Service: service}, nil
}

// GitHubDeploymentSource turns recorded GitHub deployments into candidate deploy
// events. It is the adapter that moves the investigator from replaying a JSON
// file to reading what actually shipped.
type GitHubDeploymentSource struct {
	client      DeploymentLister
	repos       []RepoMapping
	environment string
	// SizeDeploys asks GitHub how much changed between consecutive deployments
	// and turns that into the event's magnitude. It costs one extra API call per
	// deployment in the window, so it can be turned off.
	sizeDeploys bool
}

func NewGitHubDeploymentSource(client DeploymentLister, repos []RepoMapping, environment string, sizeDeploys bool) *GitHubDeploymentSource {
	return &GitHubDeploymentSource{
		client:      client,
		repos:       repos,
		environment: environment,
		sizeDeploys: sizeDeploys,
	}
}

func (g *GitHubDeploymentSource) Name() string {
	if g.environment == "" {
		return "github-deployments"
	}
	return "github-deployments:" + g.environment
}

func (g *GitHubDeploymentSource) Fetch(window time.Duration, before time.Time) ([]Event, error) {
	since := before.Add(-window)
	var events []Event

	for _, mapping := range g.repos {
		// Ask for one extra window's worth of history. The deployment before the
		// window is the diff base for the oldest one inside it, and without it
		// that deploy has no size.
		deployments, err := g.client.ListDeployments(mapping.Owner, mapping.Repo, g.environment, since.Add(-window))
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", mapping.Owner, mapping.Repo, err)
		}
		// Newest first, so deployments[i+1] is the previous deploy of the same
		// environment and therefore the base for deployments[i].
		sort.SliceStable(deployments, func(i, j int) bool {
			return deployments[i].CreatedAt.After(deployments[j].CreatedAt)
		})

		for i, d := range deployments {
			if d.CreatedAt.Before(since) || d.CreatedAt.After(before) {
				continue
			}
			var previous string
			if i+1 < len(deployments) {
				previous = deployments[i+1].SHA
			}
			events = append(events, g.toEvent(mapping, d, previous))
		}
	}
	return events, nil
}

func (g *GitHubDeploymentSource) toEvent(mapping RepoMapping, d github.Deployment, previousSHA string) Event {
	metadata := map[string]string{
		"repo":        mapping.Owner + "/" + mapping.Repo,
		"sha":         d.SHA,
		"ref":         d.Ref,
		"environment": d.Environment,
	}
	if d.Creator != "" {
		metadata["deployed_by"] = d.Creator
	}
	if d.URL != "" {
		metadata["url"] = d.URL
	}

	magnitude := 0.0 // unknown; the correlator treats it as neutral
	if g.sizeDeploys && previousSHA != "" {
		if diff, err := g.client.CompareCommits(mapping.Owner, mapping.Repo, previousSHA, d.SHA); err == nil {
			magnitude = magnitudeFromDiff(diff.FilesChanged, diff.TotalCommits)
			metadata["files_changed"] = strconv.Itoa(diff.FilesChanged)
			metadata["commits"] = strconv.Itoa(diff.TotalCommits)
		}
		// A failed comparison is not a failed investigation. The deploy still
		// belongs on the timeline; it just has no size signal.
	}

	return Event{
		ID:        fmt.Sprintf("gh-%s-%d", mapping.Repo, d.ID),
		Source:    SourceDeploy,
		Service:   mapping.Service,
		Title:     deployTitle(mapping.Service, d),
		Timestamp: d.CreatedAt,
		Magnitude: magnitude,
		Metadata:  metadata,
	}
}

func deployTitle(service string, d github.Deployment) string {
	ref := d.Ref
	if len(d.SHA) >= 7 {
		ref = d.SHA[:7]
	}
	description := strings.TrimSpace(d.Description)
	if description == "" {
		description = "deploy " + d.Ref
	}
	return fmt.Sprintf("%s %s: %s", service, ref, description)
}

// magnitudeFromDiff turns a diff size into the [0,1] signal the correlator
// expects. It is logarithmic because the difference between a 1-file and a
// 10-file deploy is much larger than between 100 and 110: past a point, a deploy
// is simply big and the exact count stops carrying information.
//
// The reference point is a 200-file change, which scores 1.0.
func magnitudeFromDiff(filesChanged, totalCommits int) float64 {
	size := filesChanged
	if size == 0 {
		// The compare API truncates its file list, but it never reports zero
		// files for a real diff. Zero means the field was missing, so fall back
		// to commit count.
		size = totalCommits
	}
	if size <= 0 {
		return 0
	}
	const reference = 200.0
	scaled := math.Log10(1+float64(size)) / math.Log10(1+reference)
	return clamp01(scaled)
}
