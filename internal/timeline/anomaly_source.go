package timeline

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/jamespham/incident-cli/internal/prometheus"
)

// SourceMetricAnomaly is a sustained shift in a metric, found by
// PrometheusAnomalySource. It is often a cause seen from the side: a saturated
// connection pool, an upstream that got slow. See ADR 0004.
const SourceMetricAnomaly EventSource = "metric_anomaly"

// RangeQuerier is the slice of the Prometheus client this source uses.
type RangeQuerier interface {
	QueryRange(query string, start, end time.Time, step time.Duration) ([]prometheus.Series, error)
}

// Direction says which way a metric has to move to count.
type Direction string

const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
	DirectionBoth Direction = "both"
)

// AnomalyQuery is one metric to watch. Point these at saturation and
// dependency signals. A shift in the SLI that paged is the incident itself,
// not its cause. A query file shared by many services will still watch the
// paged service's SLI, so the correlator's cascade pass treats an anomaly that
// a broken dependency explains as a symptom. See ADR 0006.
type AnomalyQuery struct {
	Name         string    `json:"name"`
	Query        string    `json:"query"`
	ServiceLabel string    `json:"service_label,omitempty"`
	Direction    Direction `json:"direction,omitempty"`
	// Kind is "health" (the default) for errors, latency or capacity, and
	// "load" for how much traffic arrives. A load shift does not mean the
	// service is broken. A failure upstream of it can cut its traffic too.
	Kind string `json:"kind,omitempty"`
}

// The kinds of anomaly query.
const (
	AnomalyKindHealth = "health"
	AnomalyKindLoad   = "load"
)

// AnomalyConfig tunes detection. The defaults suit one-minute resolution.
type AnomalyConfig struct {
	// Baseline is how much history before the lookback window counts as normal.
	Baseline time.Duration
	Step     time.Duration
	// Threshold is the robust z-score a point must pass.
	Threshold float64
	// Sustain is how many points in a row must pass. A single bad scrape is
	// noise. A shift that holds is a change.
	Sustain int
	// MinBaselinePoints guards against judging a series with almost no history.
	MinBaselinePoints int
}

func DefaultAnomalyConfig() AnomalyConfig {
	return AnomalyConfig{
		Baseline:          6 * time.Hour,
		Step:              time.Minute,
		Threshold:         4,
		Sustain:           3,
		MinBaselinePoints: 30,
	}
}

// LoadAnomalyQueries reads a JSON array of AnomalyQuery.
func LoadAnomalyQueries(path string) ([]AnomalyQuery, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var queries []AnomalyQuery
	if err := json.Unmarshal(b, &queries); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for i, q := range queries {
		if q.Name == "" || q.Query == "" {
			return nil, fmt.Errorf("%s: query %d needs a name and a query", path, i)
		}
		switch q.Direction {
		case "", DirectionUp, DirectionDown, DirectionBoth:
		default:
			return nil, fmt.Errorf("%s: query %q has direction %q, want up, down, or both", path, q.Name, q.Direction)
		}
		switch q.Kind {
		case "", AnomalyKindHealth, AnomalyKindLoad:
		default:
			return nil, fmt.Errorf("%s: query %q has kind %q, want health or load", path, q.Name, q.Kind)
		}
	}
	return queries, nil
}

// PrometheusAnomalySource turns sustained metric shifts into candidate events.
type PrometheusAnomalySource struct {
	client  RangeQuerier
	queries []AnomalyQuery
	cfg     AnomalyConfig
}

func NewPrometheusAnomalySource(client RangeQuerier, queries []AnomalyQuery, cfg AnomalyConfig) *PrometheusAnomalySource {
	return &PrometheusAnomalySource{client: client, queries: queries, cfg: cfg}
}

func (p *PrometheusAnomalySource) Name() string { return "prometheus-anomalies" }

func (p *PrometheusAnomalySource) Fetch(window time.Duration, before time.Time) ([]Event, error) {
	windowStart := before.Add(-window)
	start := windowStart.Add(-p.cfg.Baseline)

	var events []Event
	for _, q := range p.queries {
		series, err := p.client.QueryRange(q.Query, start, before, p.cfg.Step)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.Name, err)
		}
		label := q.ServiceLabel
		if label == "" {
			label = "service"
		}
		direction := q.Direction
		if direction == "" {
			direction = DirectionUp
		}
		for _, s := range series {
			shift, ok := DetectShift(s.Samples, windowStart, direction, p.cfg)
			if !ok {
				continue
			}
			events = append(events, anomalyEvent(q, s.Labels[label], shift))
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	return events, nil
}

// Shift is a sustained departure from the baseline.
type Shift struct {
	Onset          time.Time
	BaselineMedian float64
	Value          float64 // the value at the strongest point of the shift
	Z              float64 // the robust z-score at that point, signed
}

// DetectShift finds the first run of cfg.Sustain points after windowStart
// that all sit beyond cfg.Threshold robust z-scores from the baseline.
//
// The baseline is every point before windowStart. It uses the median and the
// median absolute deviation, not the mean and standard deviation, so an
// earlier spike in the baseline does not hide a real shift in the window.
func DetectShift(samples []prometheus.Sample, windowStart time.Time, dir Direction, cfg AnomalyConfig) (Shift, bool) {
	var baseline, recent []prometheus.Sample
	for _, s := range samples {
		if s.Time.Before(windowStart) {
			baseline = append(baseline, s)
		} else {
			recent = append(recent, s)
		}
	}
	if len(baseline) < cfg.MinBaselinePoints || len(recent) < cfg.Sustain {
		return Shift{}, false
	}

	values := make([]float64, len(baseline))
	for i, s := range baseline {
		values[i] = s.Value
	}
	med := median(values)
	deviations := make([]float64, len(values))
	for i, v := range values {
		deviations[i] = math.Abs(v - med)
	}
	// 1.4826 makes the MAD match a standard deviation on normal data. A flat
	// baseline has a MAD of zero, so a small floor keeps the score finite and
	// lets "errors where there were none" register as a large shift.
	scale := math.Max(1.4826*median(deviations), math.Max(0.01*math.Abs(med), 1e-9))

	run := 0
	var best Shift
	for i, s := range recent {
		z := (s.Value - med) / scale
		if !beyond(z, dir, cfg.Threshold) {
			if run >= cfg.Sustain {
				return best, true
			}
			run = 0
			continue
		}
		if run == 0 {
			best = Shift{Onset: s.Time, BaselineMedian: med}
		}
		run++
		if math.Abs(z) > math.Abs(best.Z) {
			best.Z, best.Value = z, s.Value
		}
		if run >= cfg.Sustain && i == len(recent)-1 {
			return best, true
		}
	}
	return Shift{}, false
}

func beyond(z float64, dir Direction, threshold float64) bool {
	switch dir {
	case DirectionDown:
		return z <= -threshold
	case DirectionBoth:
		return math.Abs(z) >= threshold
	default:
		return z >= threshold
	}
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func anomalyEvent(q AnomalyQuery, service string, s Shift) Event {
	verb := "rose"
	if s.Z < 0 {
		verb = "fell"
	}
	// A shift right at the threshold is weak evidence, one three times past
	// it is as strong as this signal gets.
	magnitude := clamp01(math.Abs(s.Z) / (3 * DefaultAnomalyConfig().Threshold))
	return Event{
		ID:        fmt.Sprintf("anomaly:%s:%s:%d", q.Name, service, s.Onset.Unix()),
		Source:    SourceMetricAnomaly,
		Service:   service,
		Title:     fmt.Sprintf("%s %s to %s from a baseline of %s", q.Name, verb, short(s.Value), short(s.BaselineMedian)),
		Timestamp: s.Onset,
		Magnitude: magnitude,
		Metadata: map[string]string{
			"query":           q.Name,
			"kind":            anomalyKind(q),
			"baseline_median": short(s.BaselineMedian),
			"value":           short(s.Value),
			"robust_z":        strconv.FormatFloat(s.Z, 'f', 1, 64),
		},
	}
}

func anomalyKind(q AnomalyQuery) string {
	if q.Kind == AnomalyKindLoad {
		return AnomalyKindLoad
	}
	return AnomalyKindHealth
}

func short(v float64) string { return strconv.FormatFloat(v, 'g', 3, 64) }
