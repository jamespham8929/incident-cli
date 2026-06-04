package timeline

import (
	"math"
	"sort"
	"time"
)

// ServiceGraph answers how relevant one service is to another. A real
// implementation can be backed by a dependency graph so an upstream service's
// deploy is scored as a plausible cause of a downstream incident. The default
// only knows about exact matches.
type ServiceGraph interface {
	// Relation returns a relevance in [0,1]: 1 for the same service, a smaller
	// value for a known dependency, near 0 for unrelated services.
	Relation(eventService, incidentService string) float64
}

type sameServiceGraph struct{}

func (sameServiceGraph) Relation(eventService, incidentService string) float64 {
	switch {
	case eventService == incidentService && eventService != "":
		return 1.0
	case eventService == "":
		return 0.3 // unknown service, mild relevance rather than zero
	default:
		return 0.1
	}
}

// Config holds the tunable parameters of the scoring model. The zero value is
// not useful; start from DefaultConfig and adjust.
type Config struct {
	// HalfLife is the lead time at which an event's temporal score halves. With a
	// 15-minute half-life, an event 15 minutes before the incident scores 0.5 on
	// timing, 30 minutes before scores 0.25, and so on.
	HalfLife time.Duration
	// LookbackWindow bounds how far back an event can be and still be considered.
	LookbackWindow time.Duration
	// The relevance blend weights. They are normalized to sum to 1, so only their
	// ratios matter.
	ServiceWeight   float64
	TypePriorWeight float64
	MagnitudeWeight float64
	// TypePriors overrides the built-in per-source priors when non-nil.
	TypePriors map[EventSource]float64
	// Graph resolves service relevance. Defaults to exact-match.
	Graph ServiceGraph
}

// DefaultConfig returns a sensible starting model. The weights lean on service
// relevance most, then the event-type prior, then magnitude, because "did this
// touch the thing that broke" is a stronger signal than "how big was it".
func DefaultConfig() Config {
	return Config{
		HalfLife:        15 * time.Minute,
		LookbackWindow:  2 * time.Hour,
		ServiceWeight:   0.45,
		TypePriorWeight: 0.30,
		MagnitudeWeight: 0.25,
	}
}

// ScoreComponents exposes how a candidate's score was built so a responder can
// see why it ranked where it did. A black-box ranking is hard to trust at 3am.
type ScoreComponents struct {
	Temporal  float64 // time-decay factor in [0,1]
	Service   float64 // service relevance in [0,1]
	TypePrior float64 // base rate for this event source in [0,1]
	Magnitude float64 // normalized signal strength in [0,1]
	Relevance float64 // weighted blend of service/type/magnitude before time gating
}

// Candidate is a scored possible cause.
type Candidate struct {
	Event      Event
	Score      float64
	LeadTime   time.Duration // how long before the incident the event happened
	Components ScoreComponents
}

// Correlator scores and ranks candidate events against an incident.
type Correlator struct {
	cfg      Config
	priors   map[EventSource]float64
	graph    ServiceGraph
	wService float64
	wType    float64
	wMag     float64
}

// NewCorrelator builds a correlator, filling defaults and normalizing weights.
func NewCorrelator(cfg Config) *Correlator {
	priors := cfg.TypePriors
	if priors == nil {
		priors = defaultTypePriors
	}
	graph := cfg.Graph
	if graph == nil {
		graph = sameServiceGraph{}
	}
	sum := cfg.ServiceWeight + cfg.TypePriorWeight + cfg.MagnitudeWeight
	if sum <= 0 {
		sum = 1
	}
	return &Correlator{
		cfg:      cfg,
		priors:   priors,
		graph:    graph,
		wService: cfg.ServiceWeight / sum,
		wType:    cfg.TypePriorWeight / sum,
		wMag:     cfg.MagnitudeWeight / sum,
	}
}

// Rank scores every candidate event against the incident and returns them sorted
// from most to least likely cause. Events after detection, or older than the
// lookback window, are dropped: a cause has to precede its effect and be in scope.
func (c *Correlator) Rank(inc Incident, events []Event) []Candidate {
	candidates := make([]Candidate, 0, len(events))
	for _, e := range events {
		lead := inc.DetectedAt.Sub(e.Timestamp)
		if lead < 0 {
			continue // happened after the incident; cannot be the cause
		}
		if c.cfg.LookbackWindow > 0 && lead > c.cfg.LookbackWindow {
			continue // too old to be in scope
		}
		comp := c.score(inc, e, lead)
		candidates = append(candidates, Candidate{
			Event:      e,
			Score:      comp.Temporal * comp.Relevance,
			LeadTime:   lead,
			Components: comp,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].LeadTime < candidates[j].LeadTime // tie-break: more recent
	})
	return candidates
}

func (c *Correlator) score(inc Incident, e Event, lead time.Duration) ScoreComponents {
	temporal := c.temporalScore(lead)
	service := c.graph.Relation(e.Service, inc.Service)
	prior := c.priors[e.Source]
	mag := magnitudeOrNeutral(e.Magnitude)
	relevance := c.wService*service + c.wType*prior + c.wMag*mag
	return ScoreComponents{
		Temporal:  temporal,
		Service:   service,
		TypePrior: prior,
		Magnitude: mag,
		Relevance: relevance,
	}
}

// temporalScore decays with lead time on a half-life curve:
// score = 0.5 ^ (lead / halfLife). It rewards recency without a hard cutoff, so
// the boundary of the lookback window is not a cliff.
func (c *Correlator) temporalScore(lead time.Duration) float64 {
	if c.cfg.HalfLife <= 0 {
		return 1.0
	}
	return math.Pow(0.5, lead.Seconds()/c.cfg.HalfLife.Seconds())
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// magnitudeOrNeutral treats a zero or negative magnitude as "unknown" and
// returns a neutral 0.5, so an event with no magnitude signal is neither
// rewarded nor penalized on that factor. An explicit positive value is clamped.
func magnitudeOrNeutral(m float64) float64 {
	if m <= 0 {
		return 0.5
	}
	return clamp01(m)
}
