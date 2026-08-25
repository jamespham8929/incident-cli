package timeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testGraph is a small estate with a cycle in it, because real dependency
// graphs have cycles and a naive traversal loops forever on them.
//
//	mobile -> web -> checkout -> auth
//	                 checkout -> payments -> ledger -> payments (cycle)
//	                             payments -> auth
func testGraph(t *testing.T) *DependencyGraph {
	t.Helper()
	return NewDependencyGraph(map[string][]string{
		"mobile":   {"web"},
		"web":      {"checkout"},
		"checkout": {"auth", "payments"},
		"payments": {"ledger", "auth"},
		"ledger":   {"payments"},
	}, DefaultGraphConfig())
}

func TestDependencyGraphRelevanceByDistanceAndDirection(t *testing.T) {
	g := testGraph(t)

	cases := []struct {
		name         string
		eventService string
		want         float64
		reason       string
	}{
		{"same service", "checkout", 1.0, "same service"},
		{"direct dependency", "auth", 0.60, "checkout depends on auth, 1 hop away"},
		{"dependency two hops out", "ledger", 0.36, "checkout depends on ledger, 2 hops away"},
		{"direct consumer", "web", 0.35, "web depends on checkout, 1 hop away"},
		{"consumer two hops out", "mobile", 0.1225, "mobile depends on checkout, 2 hops away"},
		{"unconnected service", "warehouse", 0.10, "no path in the graph"},
		{"no service on the event", "", 0.30, "no service on event"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, g.Relation(tc.eventService, "checkout"), 1e-9)
			assert.Equal(t, tc.reason, g.Explain(tc.eventService, "checkout"))
		})
	}
}

// A dependency outranks a consumer at the same distance. Auth breaking checkout
// is the common story; checkout breaking auth is the rarer one.
func TestDependencyOutranksConsumerAtEqualDistance(t *testing.T) {
	g := testGraph(t)
	assert.Greater(t, g.Relation("auth", "checkout"), g.Relation("web", "checkout"))
}

func TestDependencyGraphHandlesCycles(t *testing.T) {
	g := testGraph(t)
	// ledger depends on payments and payments depends on ledger. Traversal has to
	// terminate, and each service keeps its shortest distance.
	assert.InDelta(t, 0.60, g.Relation("payments", "checkout"), 1e-9)
	assert.InDelta(t, 0.36, g.Relation("ledger", "checkout"), 1e-9)
}

func TestDependencyGraphRespectsMaxHops(t *testing.T) {
	cfg := DefaultGraphConfig()
	cfg.MaxHops = 1
	g := NewDependencyGraph(map[string][]string{
		"checkout": {"payments"},
		"payments": {"ledger"},
	}, cfg)

	assert.InDelta(t, 0.60, g.Relation("payments", "checkout"), 1e-9)
	assert.InDelta(t, cfg.Unrelated, g.Relation("ledger", "checkout"), 1e-9,
		"two hops away with MaxHops=1 should be treated as unrelated")
}

func TestDependencyGraphIgnoresSelfEdges(t *testing.T) {
	g := NewDependencyGraph(map[string][]string{
		"checkout": {"checkout", "auth"},
	}, DefaultGraphConfig())

	assert.Equal(t, 1.0, g.Relation("checkout", "checkout"))
	assert.Equal(t, []string{"auth", "checkout"}, g.Services())
}

func TestDependencyGraphUnknownIncidentService(t *testing.T) {
	g := testGraph(t)
	// An incident on a service the graph has never heard of should not crash and
	// should treat everything else as unrelated rather than as relevant.
	assert.InDelta(t, 0.10, g.Relation("checkout", "warehouse"), 1e-9)
	assert.Equal(t, "no path in the graph", g.Explain("checkout", "warehouse"))
}

func TestLoadDependencyGraph(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deps.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
	  "checkout": ["auth", "payments"],
	  "payments": ["ledger"]
	}`), 0o644))

	g, err := LoadDependencyGraph(path, DefaultGraphConfig())
	require.NoError(t, err)
	assert.Equal(t, []string{"auth", "checkout", "ledger", "payments"}, g.Services())
	assert.InDelta(t, 0.60, g.Relation("auth", "checkout"), 1e-9)
}

func TestLoadDependencyGraphErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadDependencyGraph(filepath.Join(dir, "nope.json"), DefaultGraphConfig())
		assert.Error(t, err)
	})

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"checkout": "auth"}`), 0o644))
		_, err := LoadDependencyGraph(path, DefaultGraphConfig())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing")
	})

	t.Run("empty graph", func(t *testing.T) {
		path := filepath.Join(dir, "empty.json")
		require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o644))
		_, err := LoadDependencyGraph(path, DefaultGraphConfig())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})
}

// The point of the whole feature: with a graph loaded, a deploy to a service the
// affected one depends on outranks an unrelated deploy that happened at the same
// moment. Without the graph the two are indistinguishable.
func TestGraphLiftsUpstreamDeployAboveUnrelatedOne(t *testing.T) {
	inc := Incident{ID: "INC", Service: "checkout", DetectedAt: baseTime}
	events := []Event{
		{ID: "upstream", Source: SourceDeploy, Service: "auth", Title: "auth deploy",
			Timestamp: baseTime.Add(-10 * time.Minute), Magnitude: 0.5},
		{ID: "unrelated", Source: SourceDeploy, Service: "warehouse", Title: "warehouse deploy",
			Timestamp: baseTime.Add(-10 * time.Minute), Magnitude: 0.5},
	}

	withoutGraph := NewCorrelator(DefaultConfig()).Rank(inc, events)
	require.Len(t, withoutGraph, 2)
	assert.Equal(t, withoutGraph[0].Score, withoutGraph[1].Score,
		"without a graph the two deploys are indistinguishable")

	cfg := DefaultConfig()
	cfg.Graph = testGraph(t)
	withGraph := NewCorrelator(cfg).Rank(inc, events)
	require.Len(t, withGraph, 2)
	assert.Equal(t, "upstream", withGraph[0].Event.ID)
	assert.Greater(t, withGraph[0].Score, withGraph[1].Score)
	assert.Equal(t, "checkout depends on auth, 1 hop away", withGraph[0].Components.ServiceRelation)
}
