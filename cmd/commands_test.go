package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/jamespham/incident-cli/internal/slack"
	"github.com/jamespham/incident-cli/internal/timer"
	"github.com/stretchr/testify/require"
)

// fakePagerDuty and fakeSlack stand in for the real clients so the command
// tests never reach the network. Each method returns the configured response
// and records what it was called with.
type fakePagerDuty struct {
	createResp *pagerduty.Incident
	createErr  error
	resolveErr error
	getResp    *pagerduty.Incident
	getErr     error
	listResp   []pagerduty.Incident
	listErr    error

	resolvedID string
}

func (f *fakePagerDuty) CreateIncident(in pagerduty.CreateIncidentInput) (*pagerduty.Incident, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResp != nil {
		return f.createResp, nil
	}
	return &pagerduty.Incident{ID: "PD-1", Title: in.Title, Severity: in.Severity, URL: "https://pd.example/PD-1"}, nil
}

func (f *fakePagerDuty) ResolveIncident(id, resolution string) error {
	f.resolvedID = id
	return f.resolveErr
}

func (f *fakePagerDuty) GetIncident(id string) (*pagerduty.Incident, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &pagerduty.Incident{ID: id, Title: "Test incident", Severity: "P2"}, nil
}

func (f *fakePagerDuty) ListRecentIncidents(since time.Time) ([]pagerduty.Incident, error) {
	return f.listResp, f.listErr
}

type fakeSlack struct {
	createResp *slack.Channel
	createErr  error
	findResp   *slack.Channel
	findErr    error

	posted   []string
	archived []string
}

func (f *fakeSlack) CreateChannel(name string) (*slack.Channel, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResp != nil {
		return f.createResp, nil
	}
	return &slack.Channel{ID: "C1", Name: name}, nil
}

func (f *fakeSlack) FindChannel(name string) (*slack.Channel, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	if f.findResp != nil {
		return f.findResp, nil
	}
	return &slack.Channel{ID: "C1", Name: name}, nil
}

func (f *fakeSlack) PostMessage(channelID, text string) error {
	f.posted = append(f.posted, text)
	return nil
}

func (f *fakeSlack) ArchiveChannel(channelID string) error {
	f.archived = append(f.archived, channelID)
	return nil
}

// useFakes points the client seams at the given fakes for the duration of the
// test. Pass nil for a client the command under test never builds.
func useFakes(t *testing.T, pd pagerDutyAPI, sl slackAPI) {
	t.Helper()
	origPD, origSlack := newPagerDutyClient, newSlackClient
	if pd != nil {
		newPagerDutyClient = func(string) pagerDutyAPI { return pd }
	}
	if sl != nil {
		newSlackClient = func(string) slackAPI { return sl }
	}
	t.Cleanup(func() {
		newPagerDutyClient = origPD
		newSlackClient = origSlack
	})
}

// executeCommand runs the CLI with the given args against a fresh root command
// and returns whatever the commands wrote to stdout along with the run error.
func executeCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs(args)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	outC := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outC <- buf.String()
	}()

	runErr := root.Execute()

	_ = w.Close()
	os.Stdout = old
	out := <-outC
	_ = r.Close()
	return out, runErr
}

func TestCreateCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakes(t,
		&fakePagerDuty{createResp: &pagerduty.Incident{ID: "PD-42", URL: "https://pd.example/PD-42"}},
		&fakeSlack{})

	out, err := executeCommand(t, "create", "--title", "Checkout returns 500s", "--severity", "P1")
	require.NoError(t, err)
	require.Contains(t, out, "PagerDuty incident created: PD-42")
	require.Contains(t, out, "Slack channel created:")
	require.Contains(t, out, "MTTR timer started")
	require.Contains(t, out, "Incident ID: PD-42")
}

func TestCreateRequiresTitle(t *testing.T) {
	_, err := executeCommand(t, "create")
	require.Error(t, err)
	require.Contains(t, err.Error(), "title")
}

func TestCreatePagerDutyError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakes(t, &fakePagerDuty{createErr: errors.New("api down")}, &fakeSlack{})

	_, err := executeCommand(t, "create", "--title", "DB unreachable")
	require.Error(t, err)
	require.Contains(t, err.Error(), "creating PagerDuty incident")
}

func TestListCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakes(t, &fakePagerDuty{listResp: []pagerduty.Incident{
		{ID: "PD-1", Title: "Checkout latency", Service: "checkout", CreatedAt: time.Now()},
	}}, nil)

	out, err := executeCommand(t, "list")
	require.NoError(t, err)
	require.Contains(t, out, "ID")
	require.Contains(t, out, "PD-1")
	require.Contains(t, out, "Checkout latency")
}

func TestListEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakes(t, &fakePagerDuty{listResp: nil}, nil)

	out, err := executeCommand(t, "list")
	require.NoError(t, err)
	require.Contains(t, out, "No incidents")
}

func TestResolveCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	timer.NewMTTRTimer().Start("PD-7")
	fakePD := &fakePagerDuty{}
	useFakes(t, fakePD, &fakeSlack{})

	out, err := executeCommand(t, "resolve", "--id", "PD-7", "--resolution", "rolled back deploy")
	require.NoError(t, err)
	require.Contains(t, out, "Incident PD-7 resolved.")
	require.Contains(t, out, "MTTR:")
	require.Equal(t, "PD-7", fakePD.resolvedID)
}

func TestResolveRequiresID(t *testing.T) {
	_, err := executeCommand(t, "resolve")
	require.Error(t, err)
	require.Contains(t, err.Error(), "id")
}

func TestInvestigateCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := executeCommand(t,
		"investigate",
		"--service", "checkout",
		"--at", "2026-01-15T12:00:00Z",
		"--window", "2h",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
		"--top", "3",
	)
	require.NoError(t, err)
	require.Contains(t, out, "Timeline for checkout incident")
	require.Contains(t, out, "Most likely causes:")
	require.Contains(t, out, "checkout v4821")
}

func TestInvestigateNoSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := executeCommand(t, "investigate", "--service", "checkout")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no event sources")
}

func TestInvestigateBadAt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := executeCommand(t,
		"investigate",
		"--service", "checkout",
		"--at", "not-a-timestamp",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --at")
}

func TestPostmortemToFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	created := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	useFakes(t, &fakePagerDuty{getResp: &pagerduty.Incident{
		ID:         "PD-9",
		Title:      "Checkout outage",
		Severity:   "P1",
		CreatedAt:  created,
		ResolvedAt: created.Add(35 * time.Minute),
	}}, nil)

	outFile := filepath.Join(home, "postmortem.md")
	_, err := executeCommand(t, "postmortem", "--id", "PD-9", "--output", outFile)
	require.NoError(t, err)

	b, err := os.ReadFile(outFile)
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "# Post-Mortem: Checkout outage")
	require.Contains(t, content, "**Incident ID:** PD-9")
	require.Contains(t, content, "**Severity:** P1")
}

func TestPostmortemToStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakes(t, &fakePagerDuty{getResp: &pagerduty.Incident{
		ID: "PD-10", Title: "Search degraded", Severity: "P3",
	}}, nil)

	out, err := executeCommand(t, "postmortem", "--id", "PD-10")
	require.NoError(t, err)
	require.Contains(t, out, "# Post-Mortem: Search degraded")
}

func TestPostmortemRequiresID(t *testing.T) {
	_, err := executeCommand(t, "postmortem")
	require.Error(t, err)
	require.Contains(t, err.Error(), "id")
}
