package timeline

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testdata/chaos-experiments.json was written by chaos-operator's own
// history.run_record and history.append, wrapped the way kubectl prints a
// list. So these tests read the real record format, not a copy of it.
var chaosFixture = filepath.Join("..", "..", "testdata", "chaos-experiments.json")

var chaosBefore = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func TestChaosSourceReadsInjectedRunsInTheWindow(t *testing.T) {
	src := NewChaosSource(ChaosFile{Path: chaosFixture})

	events, err := src.Fetch(2*time.Hour, chaosBefore)
	require.NoError(t, err)

	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	// The 03:00 run is outside the window, and the 11:30 run found no pods.
	assert.ElementsMatch(t, []string{
		"identity/auth-latency/20260115T115000Z",
		"payments/checkout-nightly/20260115T114501Z",
	}, ids)
}

func TestChaosSourceEventFields(t *testing.T) {
	events, err := NewChaosSource(ChaosFile{Path: chaosFixture}).Fetch(2*time.Hour, chaosBefore)
	require.NoError(t, err)

	byID := map[string]Event{}
	for _, e := range events {
		byID[e.ID] = e
	}

	aborted := byID["identity/auth-latency/20260115T115000Z"]
	assert.Equal(t, SourceChaos, aborted.Source)
	assert.Equal(t, "auth", aborted.Service)
	assert.Equal(t, time.Date(2026, 1, 15, 11, 50, 0, 0, time.UTC), aborted.Timestamp)
	assert.Equal(t, 1.0, aborted.Magnitude, "a run that tripped a guard hit hard")
	assert.Equal(t, "chaos experiment auth-latency: network-latency on auth, aborted", aborted.Title)
	assert.Equal(t, "identity", aborted.Metadata["namespace"])
	assert.Equal(t, "2026-01-15T11:51:15Z", aborted.Metadata["finished_at"])

	// The service came from the app.kubernetes.io/name label, not spec.service.
	completed := byID["payments/checkout-nightly/20260115T114501Z"]
	assert.Equal(t, "checkout", completed.Service)
	assert.Equal(t, 0.6, completed.Magnitude)
	assert.Equal(t, "2", completed.Metadata["pods_affected"])
	assert.Equal(t, "schedule", completed.Metadata["trigger"])
}

func TestChaosRunRanksFirstForItsService(t *testing.T) {
	// A deploy to the same service at the same lead time still ranks below a
	// chaos run. The chaos run is the one to rule out first.
	events, err := NewChaosSource(ChaosFile{Path: chaosFixture}).Fetch(2*time.Hour, chaosBefore)
	require.NoError(t, err)
	events = append(events, Event{
		ID: "dep-checkout-1", Source: SourceDeploy, Service: "checkout",
		Title: "checkout v1", Timestamp: chaosBefore.Add(-15 * time.Minute), Magnitude: 0.6,
	})

	ranked := NewCorrelator(DefaultConfig()).Rank(
		Incident{Service: "checkout", DetectedAt: chaosBefore}, events)
	require.NotEmpty(t, ranked)
	assert.Equal(t, "payments/checkout-nightly/20260115T114501Z", ranked[0].Event.ID)
}

func TestChaosRunWithoutInjectedField(t *testing.T) {
	// Records written before the operator added `injected` still parse. The
	// outcome decides instead.
	assert.True(t, chaosRun{Outcome: "succeeded"}.injected())
	assert.False(t, chaosRun{Outcome: "refused"}.injected())
	no := false
	assert.False(t, chaosRun{Outcome: "succeeded", Injected: &no}.injected())
}

func TestChaosMagnitude(t *testing.T) {
	assert.Equal(t, 1.0, chaosMagnitude("did_not_recover"))
	assert.Equal(t, 0.6, chaosMagnitude("succeeded"))
	assert.Equal(t, 0.0, chaosMagnitude("something-new"), "unknown is neutral")
}

func TestChaosTitleWithoutService(t *testing.T) {
	got := chaosTitle("x", chaosRun{Type: "cpu-stress", Outcome: "completed"})
	assert.Equal(t, "chaos experiment x: cpu-stress on an unnamed service, completed", got)
}

func TestChaosSourceErrors(t *testing.T) {
	_, err := NewChaosSource(ChaosFile{Path: "does-not-exist.json"}).Fetch(time.Hour, chaosBefore)
	assert.ErrorContains(t, err, "listing chaos experiments")

	bad := fakeLister{out: []byte("not json")}
	_, err = NewChaosSource(bad).Fetch(time.Hour, chaosBefore)
	assert.ErrorContains(t, err, "parsing chaos experiments")
}

type fakeLister struct {
	out []byte
	err error
}

func (f fakeLister) ListExperiments() ([]byte, error) { return f.out, f.err }

func TestChaosKubectlArguments(t *testing.T) {
	var gotName string
	var gotArgs []string
	k := ChaosKubectl{
		Context: "kind-platform",
		Run: func(name string, args ...string) ([]byte, error) {
			gotName, gotArgs = name, args
			return []byte(`{"items":[]}`), nil
		},
	}
	out, err := k.ListExperiments()
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":[]}`, string(out))
	assert.Equal(t, "kubectl", gotName)
	assert.Equal(t, []string{
		"get", "chaosexperiments.chaos.jamespham.io", "--all-namespaces",
		"--output", "json", "--context", "kind-platform",
	}, gotArgs)
}

func TestChaosKubectlCurrentContext(t *testing.T) {
	var gotArgs []string
	k := ChaosKubectl{Run: func(name string, args ...string) ([]byte, error) {
		gotArgs = args
		return nil, errors.New("no cluster")
	}}
	_, err := NewChaosSource(k).Fetch(time.Hour, chaosBefore)
	assert.ErrorContains(t, err, "no cluster")
	assert.NotContains(t, gotArgs, "--context")
}
