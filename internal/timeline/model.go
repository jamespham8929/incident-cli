// Package timeline reconstructs an incident timeline from multiple event sources
// and ranks the events most likely to have caused the incident.
//
// The hard part of incident response is rarely "what broke" once you know where
// to look. It is finding where to look: out of dozens of deploys, config
// changes, scaling events, and related alerts in the last hour, which one
// actually triggered the page. This package scores each candidate by how well it
// explains the incident, combining timing, service relevance, the base rate of
// each event type causing incidents, and the event's own signal strength.
package timeline

import "time"

// EventSource identifies what kind of thing an event is. The kind matters
// because some kinds (a deploy, a config change) cause incidents far more often
// than others (an unrelated external event).
type EventSource string

const (
	SourceDeploy       EventSource = "deploy"
	SourceConfigChange EventSource = "config_change"
	SourceFeatureFlag  EventSource = "feature_flag"
	SourceScaling      EventSource = "scaling"
	SourceInfra        EventSource = "infra"
	SourceAlert        EventSource = "alert"
	SourceExternal     EventSource = "external"
)

// Event is a single thing that happened that might explain an incident.
type Event struct {
	ID        string
	Source    EventSource
	Service   string
	Title     string
	Timestamp time.Time
	// Magnitude is a source-specific signal strength normalized to [0,1]. For a
	// deploy it might scale with the number of files changed, for an alert with
	// its severity, for a scaling event with the size of the change. A zero value
	// is treated as "unknown" and contributes a neutral amount.
	Magnitude float64
	Metadata  map[string]string
}

// Incident is what we are trying to explain.
type Incident struct {
	ID         string
	Service    string
	DetectedAt time.Time
	Severity   string
}

// defaultTypePriors is the base probability that an event of a given source is
// the cause of an incident, before considering timing or service. These are the
// rough field intuitions every on-call engineer carries: suspect the last deploy
// first, then config and flags, and only then the ambient noise.
var defaultTypePriors = map[EventSource]float64{
	SourceDeploy:       0.90,
	SourceConfigChange: 0.85,
	SourceFeatureFlag:  0.80,
	SourceInfra:        0.70,
	SourceScaling:      0.60,
	SourceAlert:        0.50,
	SourceExternal:     0.30,
}
