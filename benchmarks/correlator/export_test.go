package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jamespham/incident-cli/internal/timeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func correlators() (*timeline.Correlator, *timeline.Correlator) {
	cfg := timeline.DefaultConfig()
	cfg.Graph = timeline.NewDependencyGraph(estate, timeline.DefaultGraphConfig())
	return timeline.NewCorrelator(timeline.DefaultConfig()), timeline.NewCorrelator(cfg)
}

func exportTrials(t *testing.T, n int) (string, []trial) {
	t.Helper()
	rng := rand.New(rand.NewSource(11))
	var trials []trial
	for i := 0; i < n; i++ {
		trials = append(trials, buildTrial(rng, allServices(), 10, 0.4))
	}
	dir := t.TempDir()
	exact, graphed := correlators()
	require.NoError(t, exportScenarios(dir, trials, 11, 10, 0.4, exact, graphed))
	return dir, trials
}

func readScenario(t *testing.T, path string) exportScenario {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var sc exportScenario
	require.NoError(t, json.Unmarshal(b, &sc))
	return sc
}

// An investigator that reads titles and IDs must not be able to spot the
// planted cause from them. Only the grader's truth block names it.
func TestExportDoesNotLeakTheAnswer(t *testing.T) {
	dir, _ := exportTrials(t, 40)
	files, err := filepath.Glob(filepath.Join(dir, "scenarios", "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 40)

	firstIsCause := 0
	for _, f := range files {
		sc := readScenario(t, f)
		for _, e := range sc.Events {
			assert.NotContains(t, e.ID, "TRUE")
			assert.NotContains(t, e.ID, "noise")
			for _, word := range []string{"true cause", "noise", "TRUE"} {
				assert.NotContains(t, e.Title, word)
			}
		}
		if sc.Events[0].ID == sc.Truth.EventID {
			firstIsCause++
		}
	}
	// With 11 events a shuffled cause comes first about 1 time in 11. Always
	// first would mean the shuffle is missing.
	assert.Less(t, firstIsCause, 20)
}

func TestExportTruthPointsAtTheCause(t *testing.T) {
	dir, trials := exportTrials(t, 25)
	for i, tr := range trials {
		sc := readScenario(t, filepath.Join(dir, "scenarios", sc4(i+1)+".json"))
		var cause *exportEvent
		for j := range sc.Events {
			if sc.Events[j].ID == sc.Truth.EventID {
				cause = &sc.Events[j]
			}
		}
		require.NotNil(t, cause, "truth names an event that is not in the scenario")
		assert.Equal(t, tr.events[0].Service, cause.Service)
		assert.Equal(t, string(tr.events[0].Source), cause.Source)
		assert.Equal(t, tr.events[0].Timestamp, cause.Timestamp)
		assert.Equal(t, tr.upstream, sc.Truth.Upstream)
		assert.Equal(t, tr.incident.Service, sc.Service)
		assert.Len(t, sc.Events, len(tr.events))
	}
}

// The recorded ranks are what the correlator gives on the exported events.
// That is the number an eval through platform-mcp has to reproduce.
func TestExportRanksMatchTheCorrelator(t *testing.T) {
	dir, _ := exportTrials(t, 30)
	exact, graphed := correlators()
	files, _ := filepath.Glob(filepath.Join(dir, "scenarios", "*.json"))
	for _, f := range files {
		sc := readScenario(t, f)
		events := make([]timeline.Event, 0, len(sc.Events))
		for _, e := range sc.Events {
			events = append(events, timeline.Event{
				ID: e.ID, Source: timeline.EventSource(e.Source), Service: e.Service,
				Title: e.Title, Timestamp: e.Timestamp, Magnitude: e.Magnitude,
			})
		}
		inc := timeline.Incident{Service: sc.Service, DetectedAt: sc.DetectedAt}
		assert.Equal(t, sc.Truth.CorrelatorRank.Exact, rankOf(exact.Rank(inc, events), sc.Truth.EventID))
		assert.Equal(t, sc.Truth.CorrelatorRank.Graph, rankOf(graphed.Rank(inc, events), sc.Truth.EventID))
		assert.Positive(t, sc.Truth.CorrelatorRank.Graph)
	}
}

func TestExportManifestAndGraph(t *testing.T) {
	dir, _ := exportTrials(t, 3)

	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	require.NoError(t, err)
	var m exportManifest
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, int64(11), m.Seed)
	assert.Equal(t, 3, m.Trials)
	assert.Equal(t, []string{
		filepath.Join("scenarios", "synthetic-0001.json"),
		filepath.Join("scenarios", "synthetic-0002.json"),
		filepath.Join("scenarios", "synthetic-0003.json"),
	}, m.Scenarios)

	// The graph file loads with the same loader incident-cli uses for --graph.
	_, err = timeline.LoadDependencyGraph(filepath.Join(dir, m.Graph), timeline.DefaultGraphConfig())
	require.NoError(t, err)
}

func TestDisguiseTitleCoversEverySource(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	sources := []timeline.EventSource{
		timeline.SourceDeploy, timeline.SourceConfigChange, timeline.SourceFeatureFlag,
		timeline.SourceScaling, timeline.SourceInfra, timeline.SourceAlert, timeline.SourceExternal,
	}
	for _, s := range sources {
		title := disguiseTitle(r, timeline.Event{Source: s, Service: "auth"})
		assert.True(t, strings.HasPrefix(title, "auth"), "%s: %q", s, title)
	}
}

func sc4(i int) string { return fmt.Sprintf("synthetic-%04d", i) }
