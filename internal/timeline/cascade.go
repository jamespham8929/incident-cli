package timeline

import (
	"fmt"
	"strings"
	"time"
)

// DependencyLister is what the cascade pass needs from a graph: the services
// one service calls directly. DependencyGraph implements it.
type DependencyLister interface {
	DependsOn(service string) []string
}

// A failure spreads up the call chain. When inventory slows down, checkout
// slows down because it waits on inventory, and frontend slows down because
// it waits on checkout. All three show a metric anomaly, in the same minute.
// Ranked on their own, the paged service's symptoms win, because they are on
// the paged service, and the real cause loses, because it is furthest away.
//
// The cascade pass reads the anomalies against the dependency graph. It
// starts at the paged service and follows its dependencies, as long as each
// one is broken too. A broken service whose own dependencies are fine, or
// broke later, is the frontier, where the failure starts. Then:
//
//   - Events on a frontier service get full service relevance. The chain of
//     broken services between it and the page shows it matters.
//   - Events on a broken service that a dependency explains are scaled down.
//     That includes the paged service's own symptoms and its own deploys.
//   - A frontier anomaly that a change on the same service explains, because
//     the change came first, is scaled down, so the change ranks above it.
//   - A change that came after its service's anomalies started is scaled
//     down. It did not cause them.
//   - A load anomaly, such as a traffic drop, is scaled down. It describes
//     traffic, not a fault, and it never marks a service as broken.
//
// The pass needs a graph and a health anomaly on the paged service. Without
// either, the ranking is unchanged. See ADR 0006.
type cascade struct {
	onset       map[string]time.Time
	reached     map[string]bool
	parent      map[string]string
	frontier    map[string]bool
	explainedBy map[string]string
	page        string
}

func isHealthAnomaly(e Event) bool {
	return e.Source == SourceMetricAnomaly && e.Metadata["kind"] != AnomalyKindLoad
}

func isLoadAnomaly(e Event) bool {
	return e.Source == SourceMetricAnomaly && e.Metadata["kind"] == AnomalyKindLoad
}

// isChange reports whether an event changed the system, as opposed to
// observing it.
func isChange(e Event) bool {
	switch e.Source {
	case SourceDeploy, SourceConfigChange, SourceFeatureFlag, SourceScaling, SourceInfra, SourceChaos:
		return true
	}
	return false
}

// findCascade maps which broken services connect to the page. It returns nil
// when the paged service shows no health anomaly.
func findCascade(inc Incident, events []Event, deps DependencyLister, slack time.Duration) *cascade {
	cs := &cascade{
		onset: map[string]time.Time{}, reached: map[string]bool{}, parent: map[string]string{},
		frontier: map[string]bool{}, explainedBy: map[string]string{}, page: inc.Service,
	}
	for _, e := range events {
		if !isHealthAnomaly(e) || e.Service == "" || e.Timestamp.After(inc.DetectedAt) {
			continue
		}
		if t, ok := cs.onset[e.Service]; !ok || e.Timestamp.Before(t) {
			cs.onset[e.Service] = e.Timestamp
		}
	}
	if _, ok := cs.onset[inc.Service]; !ok {
		return nil
	}

	// Walk from the page through dependencies that broke too, and no later
	// than the service that calls them, give or take the slack. A dependency
	// that broke later cannot explain its caller. It may even be a victim of
	// its caller, such as a retry storm.
	cs.reached[inc.Service] = true
	order := []string{inc.Service}
	for i := 0; i < len(order); i++ {
		for _, dep := range deps.DependsOn(order[i]) {
			t, broken := cs.onset[dep]
			if broken && !cs.reached[dep] && !t.After(cs.onset[order[i]].Add(slack)) {
				cs.reached[dep] = true
				cs.parent[dep] = order[i]
				order = append(order, dep)
			}
		}
	}

	// A reached dependency explains the service that calls it. Anomalies come
	// in steps, so a cascade can look a step late, which is what the slack
	// allows for.
	for _, s := range order {
		for _, dep := range deps.DependsOn(s) {
			if cs.reached[dep] && cs.parent[dep] == s {
				cs.explainedBy[s] = dep
				break
			}
		}
		if _, ok := cs.explainedBy[s]; !ok {
			cs.frontier[s] = true
		}
	}
	return cs
}

// path names the broken services from the page to s.
func (cs *cascade) path(s string) []string {
	var rev []string
	for cur := s; ; cur = cs.parent[cur] {
		rev = append(rev, cur)
		if cur == cs.page {
			break
		}
	}
	out := make([]string, len(rev))
	for i, v := range rev {
		out[len(rev)-1-i] = v
	}
	return out
}

func (cs *cascade) relation(s string) string {
	p := cs.path(s)
	if len(p) == 1 {
		return fmt.Sprintf("%s broke, and no dependency of it did", s)
	}
	return fmt.Sprintf("anomalies run from %s to %s, where the failure starts", strings.Join(p[:len(p)-1], " through "), s)
}

// apply rescores the candidates. factor is what an explained event's score is
// multiplied by.
func (cs *cascade) apply(candidates []Candidate, factor float64, wService, wType, wMag float64) {
	for i := range candidates {
		cand := &candidates[i]
		e := cand.Event
		comp := &cand.Components
		var reasons []string

		if isLoadAnomaly(e) {
			comp.Cascade *= factor
			reasons = append(reasons, "a traffic shift describes load, not a fault")
		}
		if cs.frontier[e.Service] && cs.reached[e.Service] {
			comp.Service = 1
			comp.ServiceRelation = cs.relation(e.Service)
			comp.Relevance = wService*comp.Service + wType*comp.TypePrior + wMag*comp.Magnitude
			if isHealthAnomaly(e) {
				if change, ok := latestChangeBefore(candidates, e.Service, e.Timestamp); ok {
					comp.Cascade *= factor
					reasons = append(reasons, fmt.Sprintf("explained by %s, which came first", change.ID))
				}
			}
		} else if by, ok := cs.explainedBy[e.Service]; ok {
			comp.Cascade *= factor
			reasons = append(reasons, fmt.Sprintf("explained by anomalies on %s, which %s calls", by, e.Service))
		}
		if isChange(e) && cs.reached[e.Service] && e.Timestamp.After(cs.onset[e.Service]) {
			comp.Cascade *= factor
			reasons = append(reasons, fmt.Sprintf("came after the anomalies on %s started", e.Service))
		}
		comp.CascadeReason = strings.Join(reasons, ", and ")
		cand.Score = comp.Temporal * comp.Relevance * comp.Cascade
	}
}

func latestChangeBefore(candidates []Candidate, service string, t time.Time) (Event, bool) {
	var best Event
	found := false
	for _, c := range candidates {
		e := c.Event
		if e.Service != service || !isChange(e) || e.Timestamp.After(t) {
			continue
		}
		if !found || e.Timestamp.After(best.Timestamp) {
			best, found = e, true
		}
	}
	return best, found
}
