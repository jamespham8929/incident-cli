package main

import (
	"math/rand"
	"testing"

	"github.com/jamespham/incident-cli/internal/timeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The benchmark defines the ground truth the model is measured against. A bug in
// this file makes every number it prints meaningless, and it would not be
// obvious from the output, so the traversal and the trial construction are
// tested like anything else.

func TestDependenciesWithin(t *testing.T) {
	cases := []struct {
		service string
		hops    int
		want    []string
	}{
		{"checkout", 1, []string{"auth", "inventory", "payments"}},
		{"checkout", 2, []string{"auth", "inventory", "ledger", "payments", "warehouse"}},
		{"warehouse", 2, nil},
		{"unknown-service", 2, nil},
	}

	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			assert.Equal(t, tc.want, dependenciesWithin(tc.service, tc.hops))
		})
	}
}

// The estate has a service reachable by two different paths. Visiting it twice
// would let it be picked as the true cause more often than its neighbours.
func TestDependenciesWithinDoesNotRepeat(t *testing.T) {
	deps := dependenciesWithin("web", 3)
	seen := map[string]bool{}
	for _, dep := range deps {
		require.False(t, seen[dep], "%s appears twice", dep)
		seen[dep] = true
	}
	assert.NotContains(t, deps, "web", "the starting service is not its own dependency")
}

func TestAllServicesIncludesLeaves(t *testing.T) {
	services := allServices()
	assert.Contains(t, services, "web", "a service that only appears as a key")
	assert.Contains(t, services, "warehouse", "a service that only appears as a dependency")
	assert.IsIncreasing(t, services, "the order has to be stable for a seeded run to reproduce")
}

func TestBuildTrialPlantsASameServiceCause(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tr := buildTrial(rng, allServices(), 5, 0)

	require.Len(t, tr.events, 6, "one true cause plus the noise")
	assert.False(t, tr.upstream)
	assert.Equal(t, "TRUE", tr.events[0].ID)
	assert.Equal(t, tr.incident.Service, tr.events[0].Service)
	assert.Contains(t,
		[]timeline.EventSource{timeline.SourceDeploy, timeline.SourceConfigChange},
		tr.events[0].Source)
}

func TestBuildTrialPlantsAnUpstreamCause(t *testing.T) {
	rng := rand.New(rand.NewSource(2))

	upstreamTrials := 0
	for i := 0; i < 200; i++ {
		tr := buildTrial(rng, allServices(), 5, 1.0)
		if !tr.upstream {
			// A leaf service has nothing upstream, so the cause falls back to
			// the service itself.
			assert.Empty(t, dependenciesWithin(tr.incident.Service, 2))
			assert.Equal(t, tr.incident.Service, tr.events[0].Service)
			continue
		}
		upstreamTrials++
		assert.NotEqual(t, tr.incident.Service, tr.events[0].Service)
		assert.Contains(t, dependenciesWithin(tr.incident.Service, 2), tr.events[0].Service)
	}
	assert.Positive(t, upstreamTrials)
}

// The true cause is always inside the lookback window, so a rank of 0 means the
// correlator dropped it and the benchmark is measuring the wrong thing.
func TestPlantedCauseIsAlwaysRanked(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	cor := timeline.NewCorrelator(timeline.DefaultConfig())

	for i := 0; i < 100; i++ {
		tr := buildTrial(rng, allServices(), 8, 0.5)
		assert.Positive(t, rankOfTrue(cor.Rank(tr.incident, tr.events)))
	}
}

func TestRankOfTrueMissing(t *testing.T) {
	ranked := []timeline.Candidate{{Event: timeline.Event{ID: "noise-0"}}}
	assert.Zero(t, rankOfTrue(ranked))
}

func TestResultMetrics(t *testing.T) {
	var r result
	assert.Zero(t, r.precision(), "an empty result reports zero rather than dividing by zero")
	assert.Zero(t, r.meanReciprocalRank())

	r.record(1)
	r.record(2)
	r.record(4)
	r.record(0) // excluded from the ranking entirely

	assert.Equal(t, 4, r.n)
	assert.InDelta(t, 0.25, r.precision(), 1e-9)
	assert.InDelta(t, (1.0+0.5+0.25)/4, r.meanReciprocalRank(), 1e-9)
}
