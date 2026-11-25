package cmd

import (
	"encoding/json"
	"io"
	"math"
	"time"

	"github.com/jamespham/incident-cli/internal/timeline"
)

// InvestigationSchema names the shape of `investigate --output json`. Other
// tools, platform-mcp first, parse this output. Add fields freely, but rename
// or remove one only together with a new version. See docs/investigate-json.md.
const InvestigationSchema = "incident-cli/investigate/v1"

type investigationJSON struct {
	Schema        string          `json:"schema"`
	Incident      incidentJSON    `json:"incident"`
	WindowSeconds int64           `json:"window_seconds"`
	Sources       []string        `json:"sources"`
	Events        []timelineEvent `json:"events"`
	Causes        []causeJSON     `json:"causes"`
	// TotalCandidates counts every ranked event, so a reader can tell when
	// --top cut the list short.
	TotalCandidates int `json:"total_candidates"`
}

type incidentJSON struct {
	Service    string    `json:"service"`
	DetectedAt time.Time `json:"detected_at"`
}

type timelineEvent struct {
	ID            string            `json:"id"`
	Source        string            `json:"source"`
	Service       string            `json:"service"`
	Title         string            `json:"title"`
	Timestamp     time.Time         `json:"timestamp"`
	OffsetSeconds float64           `json:"offset_seconds"`
	Magnitude     float64           `json:"magnitude"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type causeJSON struct {
	Rank            int            `json:"rank"`
	EventID         string         `json:"event_id"`
	Score           float64        `json:"score"`
	LeadTimeSeconds float64        `json:"lead_time_seconds"`
	Components      componentsJSON `json:"components"`
}

type componentsJSON struct {
	Temporal        float64 `json:"temporal"`
	Service         float64 `json:"service"`
	TypePrior       float64 `json:"type_prior"`
	Magnitude       float64 `json:"magnitude"`
	Relevance       float64 `json:"relevance"`
	ServiceRelation string  `json:"service_relation,omitempty"`
	// Cascade multiplies the score, and is 1 unless the cascade pass found
	// the event explained by something else. Added in v1 without a version
	// bump, because a reader that ignores it still reads the score right.
	Cascade       float64 `json:"cascade"`
	CascadeReason string  `json:"cascade_reason,omitempty"`
}

// round4 keeps scores readable and the golden file stable. Four places is
// finer than any decision the scores feed.
func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }

func writeInvestigationJSON(
	out io.Writer,
	inc timeline.Incident,
	window time.Duration,
	sources []timeline.Source,
	events []timeline.Event,
	ranked []timeline.Candidate,
	top int,
) error {
	doc := investigationJSON{
		Schema: InvestigationSchema,
		Incident: incidentJSON{
			Service:    inc.Service,
			DetectedAt: inc.DetectedAt.UTC(),
		},
		WindowSeconds:   int64(window.Seconds()),
		Sources:         make([]string, 0, len(sources)),
		Events:          make([]timelineEvent, 0, len(events)),
		Causes:          make([]causeJSON, 0, len(ranked)),
		TotalCandidates: len(ranked),
	}
	for _, s := range sources {
		doc.Sources = append(doc.Sources, s.Name())
	}
	for _, entry := range timeline.Build(inc, events) {
		e := entry.Event
		doc.Events = append(doc.Events, timelineEvent{
			ID:            e.ID,
			Source:        string(e.Source),
			Service:       e.Service,
			Title:         e.Title,
			Timestamp:     e.Timestamp.UTC(),
			OffsetSeconds: entry.Offset.Seconds(),
			Magnitude:     round4(e.Magnitude),
			Metadata:      e.Metadata,
		})
	}
	if top == 0 || top > len(ranked) {
		top = len(ranked)
	}
	for i, c := range ranked[:top] {
		doc.Causes = append(doc.Causes, causeJSON{
			Rank:            i + 1,
			EventID:         c.Event.ID,
			Score:           round4(c.Score),
			LeadTimeSeconds: c.LeadTime.Seconds(),
			Components: componentsJSON{
				Temporal:        round4(c.Components.Temporal),
				Service:         round4(c.Components.Service),
				TypePrior:       round4(c.Components.TypePrior),
				Magnitude:       round4(c.Components.Magnitude),
				Relevance:       round4(c.Components.Relevance),
				ServiceRelation: c.Components.ServiceRelation,
				Cascade:         round4(c.Components.Cascade),
				CascadeReason:   c.Components.CascadeReason,
			},
		})
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
