package timeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var baseTime = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func incidentOn(service string) Incident {
	return Incident{ID: "INC-1", Service: service, DetectedAt: baseTime, Severity: "P1"}
}

// evt builds an event `beforeMin` minutes before the incident. A negative value
// places it after the incident.
func evt(id string, src EventSource, service string, beforeMin int, mag float64) Event {
	return Event{
		ID:        id,
		Source:    src,
		Service:   service,
		Title:     id,
		Timestamp: baseTime.Add(-time.Duration(beforeMin) * time.Minute),
		Magnitude: mag,
	}
}

func topID(c []Candidate) string {
	if len(c) == 0 {
		return ""
	}
	return c[0].Event.ID
}

func TestRankRecentSameServiceDeployWins(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	events := []Event{
		evt("old-unrelated", SourceDeploy, "search", 90, 0.9),
		evt("recent-cause", SourceDeploy, "checkout", 2, 0.5),
		evt("ambient-alert", SourceAlert, "billing", 40, 0.4),
	}
	ranked := cor.Rank(inc, events)
	require.NotEmpty(t, ranked)
	assert.Equal(t, "recent-cause", topID(ranked))
}

func TestRankExcludesEventsAfterIncident(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	events := []Event{
		evt("after", SourceDeploy, "checkout", -5, 0.9), // 5 min after detection
		evt("before", SourceDeploy, "checkout", 5, 0.5),
	}
	ranked := cor.Rank(inc, events)
	require.Len(t, ranked, 1)
	assert.Equal(t, "before", ranked[0].Event.ID)
}

func TestRankExcludesEventsBeyondLookback(t *testing.T) {
	cor := NewCorrelator(DefaultConfig()) // 2h lookback
	inc := incidentOn("checkout")
	events := []Event{
		evt("too-old", SourceDeploy, "checkout", 200, 0.9), // 3h20m before
		evt("in-scope", SourceConfigChange, "checkout", 10, 0.5),
	}
	ranked := cor.Rank(inc, events)
	require.Len(t, ranked, 1)
	assert.Equal(t, "in-scope", ranked[0].Event.ID)
}

func TestRankNearerInTimeWins(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	events := []Event{
		evt("far", SourceDeploy, "checkout", 40, 0.5),
		evt("near", SourceDeploy, "checkout", 3, 0.5),
	}
	ranked := cor.Rank(inc, events)
	assert.Equal(t, "near", topID(ranked))
}

func TestRankSameServiceBeatsUnrelated(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	events := []Event{
		evt("unrelated", SourceDeploy, "search", 5, 0.5),
		evt("same-svc", SourceDeploy, "checkout", 5, 0.5),
	}
	ranked := cor.Rank(inc, events)
	assert.Equal(t, "same-svc", topID(ranked))
}

func TestRankDeployBeatsAlertAllElseEqual(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	events := []Event{
		evt("alert", SourceAlert, "checkout", 5, 0.5),
		evt("deploy", SourceDeploy, "checkout", 5, 0.5),
	}
	ranked := cor.Rank(inc, events)
	assert.Equal(t, "deploy", topID(ranked))
}

func TestRankMagnitudeBreaksTies(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	events := []Event{
		evt("small", SourceDeploy, "checkout", 5, 0.1),
		evt("large", SourceDeploy, "checkout", 5, 0.9),
	}
	ranked := cor.Rank(inc, events)
	assert.Equal(t, "large", topID(ranked))
}

func TestScoreIsTemporalTimesRelevance(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	ranked := cor.Rank(inc, []Event{evt("e", SourceDeploy, "checkout", 7, 0.6)})
	require.Len(t, ranked, 1)
	c := ranked[0]
	assert.InDelta(t, c.Components.Temporal*c.Components.Relevance, c.Score, 1e-9)
	for _, v := range []float64{c.Components.Temporal, c.Components.Service, c.Components.TypePrior, c.Components.Magnitude, c.Components.Relevance} {
		assert.GreaterOrEqual(t, v, 0.0)
		assert.LessOrEqual(t, v, 1.0)
	}
}

func TestTemporalHalfLife(t *testing.T) {
	cor := NewCorrelator(DefaultConfig()) // 15m half-life
	assert.InDelta(t, 1.0, cor.temporalScore(0), 1e-9)
	assert.InDelta(t, 0.5, cor.temporalScore(15*time.Minute), 1e-9)
	assert.InDelta(t, 0.25, cor.temporalScore(30*time.Minute), 1e-9)
}

func TestUnknownMagnitudeIsNeutral(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	inc := incidentOn("checkout")
	// Two identical deploys, one with unknown magnitude (0). Unknown is treated
	// as the neutral 0.5, so it must not be penalized below an explicit 0.5.
	ranked := cor.Rank(inc, []Event{
		evt("unknown", SourceDeploy, "checkout", 5, 0.0),
		evt("neutral", SourceDeploy, "checkout", 5, 0.5),
	})
	require.Len(t, ranked, 2)
	assert.InDelta(t, ranked[0].Score, ranked[1].Score, 1e-9)
}

func TestRankEmpty(t *testing.T) {
	cor := NewCorrelator(DefaultConfig())
	assert.Empty(t, cor.Rank(incidentOn("checkout"), nil))
}

func TestWeightsAreNormalized(t *testing.T) {
	// Unnormalized weights should rank identically to their normalized ratios.
	cfg := DefaultConfig()
	cfg.ServiceWeight, cfg.TypePriorWeight, cfg.MagnitudeWeight = 45, 30, 25
	cor := NewCorrelator(cfg)
	assert.InDelta(t, 0.45, cor.wService, 1e-9)
	assert.InDelta(t, 0.30, cor.wType, 1e-9)
	assert.InDelta(t, 0.25, cor.wMag, 1e-9)
}
