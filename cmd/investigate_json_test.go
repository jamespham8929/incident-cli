package cmd

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func investigateJSON(t *testing.T, extra ...string) investigationJSON {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	args := append([]string{
		"investigate",
		"--service", "checkout",
		"--at", "2026-01-15T12:00:00Z",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
		"--output", "json",
	}, extra...)
	out, err := executeCommand(t, args...)
	require.NoError(t, err)
	var doc investigationJSON
	require.NoError(t, json.Unmarshal([]byte(out), &doc), "the output must be one JSON document")
	return doc
}

func TestInvestigateJSONShape(t *testing.T) {
	doc := investigateJSON(t, "--top", "2")

	assert.Equal(t, InvestigationSchema, doc.Schema)
	assert.Equal(t, "checkout", doc.Incident.Service)
	assert.Equal(t, int64(7200), doc.WindowSeconds)
	require.Len(t, doc.Causes, 2, "--top limits the causes")
	assert.Greater(t, doc.TotalCandidates, 2, "the total still counts every candidate")
	assert.Equal(t, 1, doc.Causes[0].Rank)
	assert.GreaterOrEqual(t, doc.Causes[0].Score, doc.Causes[1].Score)

	// Every cause points at an event in the same document. platform-mcp hands
	// these IDs to the investigator as evidence, so a dangling one is a bug.
	ids := map[string]bool{}
	for _, e := range doc.Events {
		ids[e.ID] = true
	}
	for _, c := range doc.Causes {
		assert.True(t, ids[c.EventID], "cause %s has no event", c.EventID)
	}
}

func TestInvestigateJSONTopZeroMeansAll(t *testing.T) {
	doc := investigateJSON(t, "--top", "0")
	assert.Len(t, doc.Causes, doc.TotalCandidates)
}

func TestInvestigateJSONReadsChaosRuns(t *testing.T) {
	doc := investigateJSON(t, "--chaos-runs", filepath.Join("..", "testdata", "chaos-experiments.json"))

	assert.Contains(t, doc.Sources, "chaos-runs")
	require.GreaterOrEqual(t, len(doc.Causes), 2)
	// The chaos run is a strong candidate, but timing still counts. The
	// checkout deploy landed 8 minutes before the page and the chaos run 15
	// minutes before, so the deploy stays first. A higher prior does not
	// let the chaos run jump the queue.
	assert.Equal(t, "dep-checkout-4821", doc.Causes[0].EventID)
	assert.Equal(t, "payments/checkout-nightly/20260115T114501Z", doc.Causes[1].EventID)
}

func TestInvestigateRejectsBadFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	events := filepath.Join("..", "testdata", "incident-example.json")
	cases := map[string][]string{
		"unknown output":   {"--output", "yaml"},
		"negative top":     {"--top", "-1"},
		"two chaos inputs": {"--chaos-runs", "x.json", "--chaos-from-cluster"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"investigate", "--service", "checkout", "--events", events}, extra...)
			_, err := executeCommand(t, args...)
			assert.Error(t, err)
		})
	}
}

func TestInvestigateTextTopZeroPrintsAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := executeCommand(t,
		"investigate", "--service", "checkout", "--at", "2026-01-15T12:00:00Z",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
		"--top", "0",
	)
	require.NoError(t, err)
	doc := investigateJSON(t, "--top", "0")
	for i := range doc.Causes {
		assert.Contains(t, out, "  "+strconv.Itoa(i+1)+". [")
	}
}
