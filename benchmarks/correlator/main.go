// Command correlator measures the cause-ranking quality of the timeline engine
// on synthetic incidents whose true cause is known by construction.
//
// Each trial plants one true cause (a recent deploy or config change to the
// affected service) and surrounds it with noise events (random sources,
// services, and times within the lookback window). The benchmark then reports:
//
//	precision@1  fraction of incidents where the true cause ranked first
//	MRR          mean reciprocal rank of the true cause (1.0 is perfect)
//
// Run:
//
//	go run ./benchmarks/correlator -trials 2000 -noise 12
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"time"

	"github.com/jamespham/incident-cli/internal/timeline"
)

func main() {
	trials := flag.Int("trials", 2000, "number of synthetic incidents")
	noise := flag.Int("noise", 12, "noise events per incident")
	seed := flag.Int64("seed", 7, "random seed for reproducibility")
	flag.Parse()

	rng := rand.New(rand.NewSource(*seed))
	cor := timeline.NewCorrelator(timeline.DefaultConfig())

	services := []string{"checkout", "search", "billing", "auth", "catalog"}
	noiseSources := []timeline.EventSource{
		timeline.SourceDeploy, timeline.SourceConfigChange, timeline.SourceFeatureFlag,
		timeline.SourceScaling, timeline.SourceInfra, timeline.SourceAlert, timeline.SourceExternal,
	}

	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	hits := 0
	mrr := 0.0

	for t := 0; t < *trials; t++ {
		incService := services[rng.Intn(len(services))]
		inc := timeline.Incident{ID: "INC", Service: incService, DetectedAt: base, Severity: "P1"}

		// True cause: a recent deploy (or sometimes a config change) to the
		// affected service.
		causeSrc := timeline.SourceDeploy
		if rng.Float64() < 0.3 {
			causeSrc = timeline.SourceConfigChange
		}
		causeLead := time.Duration(1+rng.Intn(20)) * time.Minute
		events := []timeline.Event{{
			ID:        "TRUE",
			Source:    causeSrc,
			Service:   incService,
			Title:     "true cause",
			Timestamp: base.Add(-causeLead),
			Magnitude: 0.4 + rng.Float64()*0.6,
		}}

		for i := 0; i < *noise; i++ {
			lead := time.Duration(1+rng.Intn(115)) * time.Minute // within the 2h window
			events = append(events, timeline.Event{
				ID:        fmt.Sprintf("noise-%d", i),
				Source:    noiseSources[rng.Intn(len(noiseSources))],
				Service:   services[rng.Intn(len(services))],
				Title:     "noise",
				Timestamp: base.Add(-lead),
				Magnitude: rng.Float64(),
			})
		}

		rank := rankOfTrue(cor.Rank(inc, events))
		if rank == 1 {
			hits++
		}
		if rank > 0 {
			mrr += 1.0 / float64(rank)
		}
	}

	fmt.Printf("\nsynthetic incidents: %d, noise events each: %d, seed: %d\n", *trials, *noise, *seed)
	fmt.Printf("precision@1: %.3f\n", float64(hits)/float64(*trials))
	fmt.Printf("MRR:         %.3f\n", mrr/float64(*trials))
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
