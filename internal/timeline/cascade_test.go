package timeline

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shop from platform-mcp's incident gym: frontend calls checkout, which
// calls inventory. recommendations stands alone.
func shopGraph() *DependencyGraph {
	return NewDependencyGraph(map[string][]string{
		"frontend": {"checkout"},
		"checkout": {"inventory"},
	}, DefaultGraphConfig())
}

var page = Incident{ID: "INC", Service: "frontend", DetectedAt: time.Date(2026, 11, 25, 12, 17, 0, 0, time.UTC)}

func anomaly(id, service, kind string, minutesBefore float64) Event {
	return Event{
		ID: id, Source: SourceMetricAnomaly, Service: service, Title: id,
		Timestamp: page.DetectedAt.Add(-time.Duration(minutesBefore * float64(time.Minute))),
		Magnitude: 1, Metadata: map[string]string{"kind": kind},
	}
}

func change(id string, source EventSource, service string, minutesBefore float64) Event {
	return Event{
		ID: id, Source: source, Service: service, Title: id,
		Timestamp: page.DetectedAt.Add(-time.Duration(minutesBefore * float64(time.Minute))),
		Magnitude: 0.6,
	}
}

func rankWith(t *testing.T, graph ServiceGraph, events []Event) []Candidate {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Graph = graph
	return NewCorrelator(cfg).Rank(page, events)
}

func ids(ranked []Candidate) []string {
	out := make([]string, len(ranked))
	for i, c := range ranked {
		out[i] = c.Event.ID
	}
	return out
}

func byID(ranked []Candidate, id string) Candidate {
	for _, c := range ranked {
		if c.Event.ID == id {
			return c
		}
	}
	return Candidate{}
}

// The inventory-latency scenario. inventory slows down, and the slowness
// shows on checkout and frontend in the same minute. An unrelated service
// deployed a few minutes before.
func latencyCascade() []Event {
	return []Event{
		anomaly("frontend-p99", "frontend", AnomalyKindHealth, 3),
		anomaly("frontend-rps", "frontend", AnomalyKindLoad, 3),
		anomaly("checkout-p99", "checkout", AnomalyKindHealth, 3),
		anomaly("inventory-upstream-p99", "inventory", AnomalyKindHealth, 3),
		anomaly("inventory-rps", "inventory", AnomalyKindLoad, 3),
		anomaly("recs-rps", "recommendations", AnomalyKindLoad, 3),
		change("recs-deploy", SourceDeploy, "recommendations", 6.6),
	}
}

func TestWithoutTheCascadeThePagedSymptomsWin(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Graph = shopGraph()
	cfg.ExplainedFactor = 0
	ranked := NewCorrelator(cfg).Rank(page, latencyCascade())
	assert.Equal(t, "frontend-p99", ranked[0].Event.ID, "this is the failure the gym found")
}

func TestTheFrontierOfACascadeRanksFirst(t *testing.T) {
	ranked := rankWith(t, shopGraph(), latencyCascade())
	require.Equal(t, "inventory-upstream-p99", ranked[0].Event.ID, "ranking: %v", ids(ranked))

	top := ranked[0].Components
	assert.Equal(t, 1.0, top.Service, "the chain of broken services shows inventory matters")
	assert.Equal(t, "anomalies run from frontend through checkout to inventory, where the failure starts", top.ServiceRelation)
	assert.Equal(t, 1.0, top.Cascade)
	assert.Empty(t, top.CascadeReason)

	front := byID(ranked, "frontend-p99").Components
	assert.Equal(t, 0.5, front.Cascade)
	assert.Equal(t, "explained by anomalies on checkout, which frontend calls", front.CascadeReason)
	assert.Equal(t, 0.5, byID(ranked, "checkout-p99").Components.Cascade)

	// A traffic drop is never the cause, even on the frontier.
	rps := byID(ranked, "inventory-rps").Components
	assert.Equal(t, 0.5, rps.Cascade)
	assert.Contains(t, rps.CascadeReason, "traffic shift describes load")
	assert.Equal(t, 0.5, byID(ranked, "recs-rps").Components.Cascade)

	// An unrelated deploy is left alone. Service relevance already handles it.
	recs := byID(ranked, "recs-deploy").Components
	assert.Equal(t, 1.0, recs.Cascade)
	assert.Less(t, byID(ranked, "recs-deploy").Score, ranked[0].Score)
}

func TestADeployAfterTheSymptomsStartedIsNotTheCause(t *testing.T) {
	events := append(latencyCascade(), change("checkout-deploy", SourceDeploy, "checkout", 2.6))
	ranked := rankWith(t, shopGraph(), events)
	assert.Equal(t, "inventory-upstream-p99", ranked[0].Event.ID, "ranking: %v", ids(ranked))
	c := byID(ranked, "checkout-deploy").Components
	assert.Equal(t, 0.25, c.Cascade, "explained by inventory, and after checkout broke")
	assert.Equal(t, "explained by anomalies on inventory, which checkout calls, and came after the anomalies on checkout started", c.CascadeReason)
}

func TestADeployOnThePagedServiceIsExplainedByABrokenDependency(t *testing.T) {
	// inventory crash loops. frontend deployed four minutes before.
	events := []Event{
		anomaly("frontend-errors", "frontend", AnomalyKindHealth, 6),
		anomaly("checkout-upstream-errors", "checkout", AnomalyKindHealth, 6),
		anomaly("inventory-upstream-errors", "inventory", AnomalyKindHealth, 6),
		anomaly("inventory-replicas", "inventory", AnomalyKindHealth, 6),
		change("frontend-deploy", SourceDeploy, "frontend", 10.8),
	}
	ranked := rankWith(t, shopGraph(), events)
	assert.Contains(t, []string{"inventory-upstream-errors", "inventory-replicas"}, ranked[0].Event.ID, "ranking: %v", ids(ranked))
	assert.Equal(t, 0.5, byID(ranked, "frontend-deploy").Components.Cascade)
}

func TestAChangeOnTheFrontierExplainsItsAnomalies(t *testing.T) {
	// A bad checkout deploy. inventory only sees less traffic, because
	// checkout fails orders before it calls inventory.
	events := []Event{
		anomaly("frontend-errors", "frontend", AnomalyKindHealth, 3),
		anomaly("checkout-errors", "checkout", AnomalyKindHealth, 3),
		anomaly("inventory-rps", "inventory", AnomalyKindLoad, 3),
		change("inventory-flag", SourceFeatureFlag, "inventory", 5.7),
		change("checkout-deploy", SourceDeploy, "checkout", 4.7),
	}
	ranked := rankWith(t, shopGraph(), events)
	require.Equal(t, "checkout-deploy", ranked[0].Event.ID, "ranking: %v", ids(ranked))
	assert.Equal(t, 1.0, ranked[0].Components.Service)
	errs := byID(ranked, "checkout-errors").Components
	assert.Equal(t, 0.5, errs.Cascade)
	assert.Equal(t, "explained by checkout-deploy, which came first", errs.CascadeReason)
	// inventory only lost traffic, so it is not broken, and its flag is not
	// boosted.
	assert.Equal(t, 0.36, byID(ranked, "inventory-flag").Components.Service)
}

func TestAChaosRunOnTheFrontierRanksFirst(t *testing.T) {
	events := append(latencyCascade(), change("gym/fault/1", SourceChaos, "inventory", 3.6))
	ranked := rankWith(t, shopGraph(), events)
	assert.Equal(t, "gym/fault/1", ranked[0].Event.ID, "ranking: %v", ids(ranked))
}

func TestADependencyThatBrokeLaterExplainsNothing(t *testing.T) {
	events := []Event{
		anomaly("frontend-p99", "frontend", AnomalyKindHealth, 10),
		anomaly("checkout-p99", "checkout", AnomalyKindHealth, 4),
	}
	ranked := rankWith(t, shopGraph(), events)
	assert.Equal(t, 1.0, byID(ranked, "frontend-p99").Components.Cascade, "checkout broke six minutes later")
	assert.Equal(t, "frontend broke, and no dependency of it did", byID(ranked, "frontend-p99").Components.ServiceRelation)
	// checkout is not part of the cascade, so it keeps its graph relevance.
	assert.Equal(t, 0.6, byID(ranked, "checkout-p99").Components.Service)
	assert.Equal(t, 1.0, byID(ranked, "checkout-p99").Components.Cascade)

	// One minute later is within the slack, because anomalies come in steps.
	events[1] = anomaly("checkout-p99", "checkout", AnomalyKindHealth, 9)
	ranked = rankWith(t, shopGraph(), events)
	assert.Equal(t, 0.5, byID(ranked, "frontend-p99").Components.Cascade)
}

func TestNoCascadeWithoutAGraphOrASymptomOnThePage(t *testing.T) {
	// Without a graph there are no edges to follow.
	ranked := rankWith(t, nil, latencyCascade())
	for _, c := range ranked {
		assert.Equal(t, 1.0, c.Components.Cascade, c.Event.ID)
	}
	// Without a health anomaly on the paged service there is no cascade to
	// trace. Only load anomalies here.
	ranked = rankWith(t, shopGraph(), []Event{
		anomaly("frontend-rps", "frontend", AnomalyKindLoad, 3),
		anomaly("checkout-p99", "checkout", AnomalyKindHealth, 3),
	})
	for _, c := range ranked {
		assert.Equal(t, 1.0, c.Components.Cascade, c.Event.ID)
	}
}

func TestAnAnomalyWithNoKindCountsAsHealth(t *testing.T) {
	e := anomaly("old", "frontend", "", 3)
	delete(e.Metadata, "kind")
	assert.True(t, isHealthAnomaly(e))
	assert.False(t, isLoadAnomaly(e))
	assert.False(t, isChange(e))
	assert.True(t, isChange(change("d", SourceDeploy, "x", 1)))
	assert.False(t, isChange(change("a", SourceAlert, "x", 1)))
}

func TestLoadAnomalyQueriesRejectAnUnknownKind(t *testing.T) {
	path := t.TempDir() + "/q.json"
	require.NoError(t, writeFile(path, `[{"name":"x","query":"up","kind":"vibes"}]`))
	_, err := LoadAnomalyQueries(path)
	require.ErrorContains(t, err, `kind "vibes", want health or load`)

	require.NoError(t, writeFile(path, `[{"name":"x","query":"up","kind":"load"}]`))
	qs, err := LoadAnomalyQueries(path)
	require.NoError(t, err)
	assert.Equal(t, AnomalyKindLoad, anomalyEvent(qs[0], "svc", Shift{Onset: page.DetectedAt, Z: -5}).Metadata["kind"])
	assert.Equal(t, AnomalyKindHealth, anomalyEvent(AnomalyQuery{Name: "y"}, "svc", Shift{Onset: page.DetectedAt, Z: 5}).Metadata["kind"])
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }
