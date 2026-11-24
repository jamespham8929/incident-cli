package timeline

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamespham/incident-cli/internal/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var anomalyBase = time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)

// series builds one point a minute. Points before `shiftAt` minutes come from
// `normal`, the rest from `shifted`. A fixed seed keeps the noise repeatable.
func series(minutes, shiftAt int, normal, shifted func(r *rand.Rand) float64) []prometheus.Sample {
	r := rand.New(rand.NewSource(7))
	out := make([]prometheus.Sample, minutes)
	for i := range out {
		v := normal(r)
		if i >= shiftAt {
			v = shifted(r)
		}
		out[i] = prometheus.Sample{Time: anomalyBase.Add(time.Duration(i) * time.Minute), Value: v}
	}
	return out
}

func around(center, spread float64) func(r *rand.Rand) float64 {
	return func(r *rand.Rand) float64 { return center + spread*(r.Float64()-0.5) }
}

func TestDetectShiftFindsASustainedRise(t *testing.T) {
	// 6h of baseline near 20ms, then latency jumps to 80ms at minute 400.
	samples := series(480, 400, around(0.020, 0.004), around(0.080, 0.004))
	windowStart := anomalyBase.Add(360 * time.Minute)

	shift, ok := DetectShift(samples, windowStart, DirectionUp, DefaultAnomalyConfig())
	require.True(t, ok)
	assert.True(t, shift.Onset.Equal(anomalyBase.Add(400*time.Minute)), "onset %s", shift.Onset)
	assert.InDelta(t, 0.020, shift.BaselineMedian, 0.002)
	assert.Greater(t, shift.Z, 4.0)
}

func TestDetectShiftIgnoresAOneMinuteSpike(t *testing.T) {
	samples := series(480, 480, around(0.020, 0.004), around(0.020, 0.004))
	samples[420].Value = 5.0 // one bad scrape
	_, ok := DetectShift(samples, anomalyBase.Add(360*time.Minute), DirectionUp, DefaultAnomalyConfig())
	assert.False(t, ok)
}

func TestDetectShiftIsNotFooledByASpikeInTheBaseline(t *testing.T) {
	// A big spike an hour into the baseline would inflate a standard deviation
	// and hide the later shift. The median and MAD shrug it off.
	samples := series(480, 400, around(0.020, 0.004), around(0.060, 0.004))
	for i := 60; i < 70; i++ {
		samples[i].Value = 10
	}
	_, ok := DetectShift(samples, anomalyBase.Add(360*time.Minute), DirectionUp, DefaultAnomalyConfig())
	assert.True(t, ok)
}

func TestDetectShiftRespectsDirection(t *testing.T) {
	// Throughput collapses. An "up" query does not care, a "down" one does.
	samples := series(480, 400, around(1000, 50), around(100, 50))
	windowStart := anomalyBase.Add(360 * time.Minute)

	_, up := DetectShift(samples, windowStart, DirectionUp, DefaultAnomalyConfig())
	shift, down := DetectShift(samples, windowStart, DirectionDown, DefaultAnomalyConfig())
	_, both := DetectShift(samples, windowStart, DirectionBoth, DefaultAnomalyConfig())

	assert.False(t, up)
	assert.True(t, down)
	assert.True(t, both)
	assert.Less(t, shift.Z, 0.0)
}

func TestDetectShiftOnAFlatZeroBaseline(t *testing.T) {
	// Errors where there were none is a shift, even though the MAD is zero.
	samples := series(480, 450, func(*rand.Rand) float64 { return 0 }, func(*rand.Rand) float64 { return 3 })
	shift, ok := DetectShift(samples, anomalyBase.Add(360*time.Minute), DirectionUp, DefaultAnomalyConfig())
	require.True(t, ok)
	assert.True(t, shift.Onset.Equal(anomalyBase.Add(450*time.Minute)))
}

func TestDetectShiftNeedsHistory(t *testing.T) {
	samples := series(40, 20, around(1, 0.1), around(9, 0.1))
	_, ok := DetectShift(samples, anomalyBase.Add(20*time.Minute), DirectionUp, DefaultAnomalyConfig())
	assert.False(t, ok, "20 baseline points is under the 30 needed")
}

func TestDetectShiftRunCutShortAtTheEnd(t *testing.T) {
	// Only the last two points are high, one short of the three required.
	samples := series(480, 478, around(0.020, 0.004), around(0.080, 0.004))
	_, ok := DetectShift(samples, anomalyBase.Add(360*time.Minute), DirectionUp, DefaultAnomalyConfig())
	assert.False(t, ok)
}

type fakeRange struct {
	series map[string][]prometheus.Series
	err    error
	start  time.Time
}

func (f *fakeRange) QueryRange(query string, start, end time.Time, step time.Duration) ([]prometheus.Series, error) {
	f.start = start
	return f.series[query], f.err
}

func TestAnomalySourceBuildsEvents(t *testing.T) {
	before := anomalyBase.Add(480 * time.Minute)
	fake := &fakeRange{series: map[string][]prometheus.Series{
		"pool_wait": {
			{Labels: map[string]string{"app": "payments-db"}, Samples: series(480, 400, around(0.01, 0.002), around(0.5, 0.01))},
			{Labels: map[string]string{"app": "orders-db"}, Samples: series(480, 480, around(0.01, 0.002), around(0.01, 0.002))},
		},
	}}
	src := NewPrometheusAnomalySource(fake, []AnomalyQuery{
		{Name: "db_pool_wait", Query: "pool_wait", ServiceLabel: "app"},
	}, DefaultAnomalyConfig())

	events, err := src.Fetch(2*time.Hour, before)
	require.NoError(t, err)

	// The query reaches back over the window and the baseline.
	assert.True(t, fake.start.Equal(before.Add(-2*time.Hour-6*time.Hour)))
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, SourceMetricAnomaly, e.Source)
	assert.Equal(t, "payments-db", e.Service)
	assert.Contains(t, e.Title, "db_pool_wait rose to")
	assert.Greater(t, e.Magnitude, 0.3)
	assert.Equal(t, "db_pool_wait", e.Metadata["query"])
}

func TestAnomalySourceSurfacesQueryErrors(t *testing.T) {
	src := NewPrometheusAnomalySource(&fakeRange{err: errors.New("timeout")},
		[]AnomalyQuery{{Name: "x", Query: "x"}}, DefaultAnomalyConfig())
	_, err := src.Fetch(time.Hour, anomalyBase)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "x: timeout")
}

func TestLoadAnomalyQueries(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "q.json")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}

	qs, err := LoadAnomalyQueries(write(`[{"name":"latency","query":"x","direction":"both"}]`))
	require.NoError(t, err)
	assert.Equal(t, DirectionBoth, qs[0].Direction)

	_, err = LoadAnomalyQueries(write(`[{"name":"latency"}]`))
	assert.Error(t, err)
	_, err = LoadAnomalyQueries(write(`[{"name":"a","query":"x","direction":"sideways"}]`))
	assert.Error(t, err)
	_, err = LoadAnomalyQueries(write(`not json`))
	assert.Error(t, err)
	_, err = LoadAnomalyQueries(filepath.Join(dir, "missing.json"))
	assert.Error(t, err)
}

func TestAnUpstreamAnomalyOutranksAnUnrelatedDeploy(t *testing.T) {
	// The point of the source: with the dependency graph, a database shift
	// right before checkout paged should beat a deploy of an unrelated service.
	graph := NewDependencyGraph(map[string][]string{"checkout": {"payments-db"}}, DefaultGraphConfig())
	cfg := DefaultConfig()
	cfg.Graph = graph
	inc := Incident{Service: "checkout", DetectedAt: anomalyBase.Add(time.Hour)}
	events := []Event{
		{ID: "a", Source: SourceMetricAnomaly, Service: "payments-db", Title: "pool wait rose",
			Timestamp: inc.DetectedAt.Add(-6 * time.Minute), Magnitude: 0.9},
		{ID: "d", Source: SourceDeploy, Service: "search", Title: "search v12",
			Timestamp: inc.DetectedAt.Add(-5 * time.Minute)},
	}
	ranked := NewCorrelator(cfg).Rank(inc, events)
	require.Len(t, ranked, 2)
	assert.Equal(t, "a", ranked[0].Event.ID)
}
