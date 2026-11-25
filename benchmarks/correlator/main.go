// Command correlator measures the cause-ranking quality of the timeline engine
// on synthetic incidents whose true cause is known by construction.
//
// Each trial plants one true cause and surrounds it with noise events. Some
// causes are a change to the affected service itself; the rest are a change to
// a service it depends on, which is the case exact-service matching cannot rank
// and the dependency graph exists to handle. Every trial is scored twice, once
// with each model, so the graph's effect is measured rather than asserted.
//
//	precision@1  fraction of incidents where the true cause ranked first
//	MRR          mean reciprocal rank of the true cause (1.0 is perfect)
//
// Run:
//
//	go run ./benchmarks/correlator -trials 2000 -noise 12 -upstream 0.35
//
// With -export DIR it also writes the trials as scenario files, which
// platform-mcp's offline eval replays against an investigator. See export.go.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/jamespham/incident-cli/internal/timeline"
)

// estate is the synthetic service graph. It is deliberately shaped like a real
// one: a couple of entry points, a fan-out through mid-tier services, and shared
// leaf dependencies that several services call.
var estate = map[string][]string{
	"web":       {"checkout", "search", "catalog"},
	"mobile":    {"checkout", "catalog"},
	"checkout":  {"auth", "payments", "inventory"},
	"payments":  {"ledger", "auth"},
	"billing":   {"ledger"},
	"search":    {"catalog"},
	"catalog":   {"inventory"},
	"inventory": {"warehouse"},
}

type trial struct {
	incident timeline.Incident
	events   []timeline.Event
	upstream bool // whether the true cause is on a service the incident service depends on
}

type result struct {
	hits int
	mrr  float64
	n    int
}

func (r *result) record(rank int) {
	r.n++
	if rank == 1 {
		r.hits++
	}
	if rank > 0 {
		r.mrr += 1.0 / float64(rank)
	}
}

func (r result) precision() float64 {
	if r.n == 0 {
		return 0
	}
	return float64(r.hits) / float64(r.n)
}

func (r result) meanReciprocalRank() float64 {
	if r.n == 0 {
		return 0
	}
	return r.mrr / float64(r.n)
}

func main() {
	trials := flag.Int("trials", 2000, "number of synthetic incidents")
	noise := flag.Int("noise", 12, "noise events per incident")
	upstreamShare := flag.Float64("upstream", 0.35, "fraction of incidents whose true cause is on an upstream dependency")
	seed := flag.Int64("seed", 7, "random seed for reproducibility")
	upstreamDecay := flag.Float64("upstream-decay", timeline.DefaultGraphConfig().UpstreamDecay,
		"relevance multiplier per hop toward a dependency")
	downstreamDecay := flag.Float64("downstream-decay", timeline.DefaultGraphConfig().DownstreamDecay,
		"relevance multiplier per hop toward a consumer")
	exportDir := flag.String("export", "", "also write the trials as scenario files for platform-mcp's eval, into this directory")
	flag.Parse()

	rng := rand.New(rand.NewSource(*seed))
	services := allServices()

	exact := timeline.NewCorrelator(timeline.DefaultConfig())

	graphCfg := timeline.DefaultConfig()
	decay := timeline.DefaultGraphConfig()
	decay.UpstreamDecay = *upstreamDecay
	decay.DownstreamDecay = *downstreamDecay
	graphCfg.Graph = timeline.NewDependencyGraph(estate, decay)
	graphed := timeline.NewCorrelator(graphCfg)

	var (
		exactAll, graphAll           result
		exactSame, graphSame         result
		exactUpstream, graphUpstream result
	)

	var built []trial
	for i := 0; i < *trials; i++ {
		tr := buildTrial(rng, services, *noise, *upstreamShare)
		built = append(built, tr)

		exactRank := rankOfTrue(exact.Rank(tr.incident, tr.events))
		graphRank := rankOfTrue(graphed.Rank(tr.incident, tr.events))

		exactAll.record(exactRank)
		graphAll.record(graphRank)
		if tr.upstream {
			exactUpstream.record(exactRank)
			graphUpstream.record(graphRank)
		} else {
			exactSame.record(exactRank)
			graphSame.record(graphRank)
		}
	}

	if *exportDir != "" {
		if err := exportScenarios(*exportDir, built, *seed, *noise, *upstreamShare, exact, graphed); err != nil {
			fmt.Fprintln(os.Stderr, "export failed:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %d scenarios to %s\n", len(built), *exportDir)
	}

	fmt.Printf("\nsynthetic incidents: %d, noise events each: %d, upstream causes: %.0f%%, seed: %d\n",
		*trials, *noise, *upstreamShare*100, *seed)
	fmt.Printf("graph decay: %.2f per hop upstream, %.2f per hop downstream\n\n",
		*upstreamDecay, *downstreamDecay)
	fmt.Printf("%-24s %12s %8s\n", "", "precision@1", "MRR")
	fmt.Printf("%-24s %12.3f %8.3f\n", "exact-service match", exactAll.precision(), exactAll.meanReciprocalRank())
	fmt.Printf("%-24s %12.3f %8.3f\n", "dependency graph", graphAll.precision(), graphAll.meanReciprocalRank())

	fmt.Printf("\nprecision@1 split by where the true cause was:\n")
	fmt.Printf("%-24s %8s %12s %10s\n", "", "trials", "exact-match", "graph")
	fmt.Printf("%-24s %8d %12.3f %10.3f\n", "same service",
		exactSame.n, exactSame.precision(), graphSame.precision())
	fmt.Printf("%-24s %8d %12.3f %10.3f\n", "upstream dependency",
		exactUpstream.n, exactUpstream.precision(), graphUpstream.precision())
}

// buildTrial plants one true cause and surrounds it with noise. The ground truth
// is derived here with a local traversal rather than by asking the graph under
// test, so the benchmark cannot agree with a bug in it.
func buildTrial(rng *rand.Rand, services []string, noise int, upstreamShare float64) trial {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	incidentService := services[rng.Intn(len(services))]
	inc := timeline.Incident{ID: "INC", Service: incidentService, DetectedAt: base, Severity: "P1"}

	causeService := incidentService
	upstream := false
	if rng.Float64() < upstreamShare {
		if candidates := dependenciesWithin(incidentService, 2); len(candidates) > 0 {
			causeService = candidates[rng.Intn(len(candidates))]
			upstream = true
		}
	}

	causeSource := timeline.SourceDeploy
	if rng.Float64() < 0.3 {
		causeSource = timeline.SourceConfigChange
	}
	events := []timeline.Event{{
		ID:        "TRUE",
		Source:    causeSource,
		Service:   causeService,
		Title:     "true cause",
		Timestamp: base.Add(-time.Duration(1+rng.Intn(20)) * time.Minute),
		// The conversion stops arm64 from fusing this into one multiply-add.
		// A fused result can differ in the last bit. Then the exported
		// scenarios would differ between a Mac and CI.
		Magnitude: 0.4 + float64(rng.Float64()*0.6),
	}}

	noiseSources := []timeline.EventSource{
		timeline.SourceDeploy, timeline.SourceConfigChange, timeline.SourceFeatureFlag,
		timeline.SourceScaling, timeline.SourceInfra, timeline.SourceAlert, timeline.SourceExternal,
	}
	for i := 0; i < noise; i++ {
		events = append(events, timeline.Event{
			ID:        fmt.Sprintf("noise-%d", i),
			Source:    noiseSources[rng.Intn(len(noiseSources))],
			Service:   services[rng.Intn(len(services))],
			Title:     "noise",
			Timestamp: base.Add(-time.Duration(1+rng.Intn(115)) * time.Minute),
			Magnitude: rng.Float64(),
		})
	}

	return trial{incident: inc, events: events, upstream: upstream}
}

// dependenciesWithin returns the services reachable from start by following
// dependency edges, up to maxHops.
func dependenciesWithin(start string, maxHops int) []string {
	seen := map[string]bool{start: true}
	frontier := []string{start}
	var out []string
	for hop := 0; hop < maxHops; hop++ {
		var next []string
		for _, service := range frontier {
			for _, dep := range estate[service] {
				if seen[dep] {
					continue
				}
				seen[dep] = true
				out = append(out, dep)
				next = append(next, dep)
			}
		}
		frontier = next
	}
	sort.Strings(out)
	return out
}

func allServices() []string {
	seen := map[string]bool{}
	for service, deps := range estate {
		seen[service] = true
		for _, dep := range deps {
			seen[dep] = true
		}
	}
	out := make([]string, 0, len(seen))
	for service := range seen {
		out = append(out, service)
	}
	sort.Strings(out)
	return out
}

// rankOfTrue returns the 1-based position of the planted true cause, or 0 if it
// was excluded (which should not happen for an in-window cause).
func rankOfTrue(ranked []timeline.Candidate) int {
	for i, c := range ranked {
		if c.Event.ID == "TRUE" {
			return i + 1
		}
	}
	return 0
}
