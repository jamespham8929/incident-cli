package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/jamespham/incident-cli/internal/timeline"
)

// The export turns benchmark trials into scenario files for platform-mcp's
// offline eval. The correlator never reads an event's ID or title, so the
// benchmark can call the planted cause "TRUE". A language model reads both, so
// the export gives every event a random ID and a title drawn the same way for
// the cause and the noise. It also shuffles the events, so the cause is not
// always first.

type exportEvent struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Service   string    `json:"service"`
	Title     string    `json:"title"`
	Timestamp time.Time `json:"timestamp"`
	Magnitude float64   `json:"magnitude"`
}

type exportTruth struct {
	EventID  string `json:"event_id"`
	Service  string `json:"service"`
	Upstream bool   `json:"upstream"`
	// CorrelatorRank is where incident-cli ranked the cause on exactly these
	// events. An eval that runs the correlator through platform-mcp must get
	// the same ranks, or the pipe in between is losing something.
	CorrelatorRank struct {
		Exact int `json:"exact"`
		Graph int `json:"graph"`
	} `json:"correlator_rank"`
}

type exportScenario struct {
	ID            string        `json:"id"`
	Service       string        `json:"service"`
	DetectedAt    time.Time     `json:"detected_at"`
	WindowSeconds int64         `json:"window_seconds"`
	Events        []exportEvent `json:"events"`
	// Truth is for the grader. An investigator must never be shown it.
	Truth exportTruth `json:"truth"`
}

type exportManifest struct {
	GeneratedBy   string   `json:"generated_by"`
	Seed          int64    `json:"seed"`
	Trials        int      `json:"trials"`
	Noise         int      `json:"noise"`
	UpstreamShare float64  `json:"upstream_share"`
	Graph         string   `json:"graph"`
	Scenarios     []string `json:"scenarios"`
}

var (
	deployChanges = []string{
		"refactor retry logic", "bump client library", "add request tracing",
		"tune cache TTL", "rewrite pagination", "drop legacy endpoint",
		"raise worker count", "update TLS settings",
	}
	configChanges = []string{
		"raise DB pool to 200", "lower request timeout to 2s", "change cache size to 4GiB",
		"enable connection reuse", "point at the new upstream endpoint",
	}
	flagNames = []string{
		"new-ranking", "async-writes", "batch-checkout", "fast-path-auth", "v2-pricing",
	}
	infraChanges = []string{
		"node pool upgrade started", "disk pressure on one node",
		"load balancer target replaced", "zone maintenance window opened",
	}
	alertTitles = []string{
		"latency p99 above 800ms", "error rate above 2%", "CPU saturation above 90%",
		"queue depth above 10k",
	}
	externalTitles = []string{
		"cloud provider reports degraded networking", "CDN vendor opened an incident",
		"DNS provider status is investigating",
	}
)

func pick(r *rand.Rand, list []string) string { return list[r.Intn(len(list))] }

// disguiseTitle writes a plausible title from the event's source and service
// only. It never looks at whether the event is the planted cause.
func disguiseTitle(r *rand.Rand, e timeline.Event) string {
	switch e.Source {
	case timeline.SourceDeploy:
		return fmt.Sprintf("%s v%d: %s", e.Service, 1000+r.Intn(9000), pick(r, deployChanges))
	case timeline.SourceConfigChange:
		return fmt.Sprintf("%s: %s", e.Service, pick(r, configChanges))
	case timeline.SourceFeatureFlag:
		return fmt.Sprintf("%s: flag %s turned on", e.Service, pick(r, flagNames))
	case timeline.SourceScaling:
		from := 2 + r.Intn(8)
		return fmt.Sprintf("%s scaled %d to %d replicas", e.Service, from, from+1+r.Intn(6))
	case timeline.SourceInfra:
		return fmt.Sprintf("%s: %s", e.Service, pick(r, infraChanges))
	case timeline.SourceAlert:
		return fmt.Sprintf("%s %s", e.Service, pick(r, alertTitles))
	default:
		return fmt.Sprintf("%s: %s", e.Service, pick(r, externalTitles))
	}
}

// disguise returns the trial's events with new IDs and titles, shuffled, and
// the new ID of the planted cause.
func disguise(r *rand.Rand, tr trial) ([]timeline.Event, string) {
	events := make([]timeline.Event, len(tr.events))
	used := map[string]bool{}
	trueID := ""
	for i, e := range tr.events {
		id := ""
		for id == "" || used[id] {
			id = fmt.Sprintf("evt-%06x", r.Intn(1<<24))
		}
		used[id] = true
		if e.ID == "TRUE" {
			trueID = id
		}
		e.ID = id
		e.Title = disguiseTitle(r, e)
		events[i] = e
	}
	r.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	return events, trueID
}

func rankOf(ranked []timeline.Candidate, id string) int {
	for i, c := range ranked {
		if c.Event.ID == id {
			return i + 1
		}
	}
	return 0
}

func buildScenario(
	index int, tr trial, r *rand.Rand, exact, graphed *timeline.Correlator, window time.Duration,
) exportScenario {
	events, trueID := disguise(r, tr)

	sc := exportScenario{
		ID:            fmt.Sprintf("synthetic-%04d", index),
		Service:       tr.incident.Service,
		DetectedAt:    tr.incident.DetectedAt,
		WindowSeconds: int64(window.Seconds()),
		Events:        make([]exportEvent, 0, len(events)),
	}
	for _, e := range events {
		sc.Events = append(sc.Events, exportEvent{
			ID: e.ID, Source: string(e.Source), Service: e.Service,
			Title: e.Title, Timestamp: e.Timestamp, Magnitude: e.Magnitude,
		})
	}
	sc.Truth.EventID = trueID
	sc.Truth.Upstream = tr.upstream
	for _, e := range events {
		if e.ID == trueID {
			sc.Truth.Service = e.Service
		}
	}
	sc.Truth.CorrelatorRank.Exact = rankOf(exact.Rank(tr.incident, events), trueID)
	sc.Truth.CorrelatorRank.Graph = rankOf(graphed.Rank(tr.incident, events), trueID)
	return sc
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// exportScenarios writes dir/manifest.json, dir/graph.json and one file per
// trial under dir/scenarios. The trials come from the same seed and the same
// generator as the printed benchmark.
func exportScenarios(
	dir string, trials []trial, seed int64, noise int, upstreamShare float64,
	exact, graphed *timeline.Correlator,
) error {
	if err := os.MkdirAll(filepath.Join(dir, "scenarios"), 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "graph.json"), estate); err != nil {
		return err
	}
	// A separate stream for the disguise, so exporting does not change which
	// trials a seed produces.
	r := rand.New(rand.NewSource(seed + 1))
	manifest := exportManifest{
		GeneratedBy:   "incident-cli benchmarks/correlator -export",
		Seed:          seed,
		Trials:        len(trials),
		Noise:         noise,
		UpstreamShare: upstreamShare,
		Graph:         "graph.json",
	}
	window := timeline.DefaultConfig().LookbackWindow
	for i, tr := range trials {
		sc := buildScenario(i+1, tr, r, exact, graphed, window)
		name := filepath.Join("scenarios", sc.ID+".json")
		if err := writeJSON(filepath.Join(dir, name), sc); err != nil {
			return err
		}
		manifest.Scenarios = append(manifest.Scenarios, name)
	}
	return writeJSON(filepath.Join(dir, "manifest.json"), manifest)
}
