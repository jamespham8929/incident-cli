package timeline

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
)

// DependencyGraph scores service relevance by distance in a service dependency
// graph, so a deploy to a service the affected one calls is a plausible cause
// rather than noise.
//
// Direction is the whole point. If checkout calls auth, then an auth deploy can
// break checkout directly, and it is scored close to a checkout deploy. A deploy
// to something that calls checkout is a weaker signal but not nothing, because a
// consumer that changes its traffic pattern or starts retrying can take down the
// thing it depends on. Both directions decay per hop.
type DependencyGraph struct {
	dependsOn  map[string][]string
	dependedBy map[string][]string
	cfg        GraphConfig

	mu     sync.Mutex
	cached map[string]hopDistances
}

// hopDistances holds, for one incident service, how many hops away every other
// service is in each direction.
type hopDistances struct {
	upstream   map[string]int
	downstream map[string]int
}

// GraphConfig tunes how fast relevance falls off with distance.
type GraphConfig struct {
	// UpstreamDecay multiplies relevance per hop toward a dependency: a service
	// the incident service calls, which it calls, and so on. At 0.6 a direct
	// dependency scores 0.6 and a dependency-of-a-dependency scores 0.36.
	UpstreamDecay float64
	// DownstreamDecay does the same toward consumers. It is lower because a
	// consumer's deploy causing its provider's incident is real but rarer than
	// the other way round.
	DownstreamDecay float64
	// MaxHops bounds the search. Past three hops in a large estate almost
	// everything is connected to almost everything, and the score stops carrying
	// information.
	MaxHops int
	// UnknownService is the relevance of an event with no service attached. Mild
	// rather than zero, because "unknown" is not evidence of irrelevance.
	UnknownService float64
	// Unrelated is the floor for a service with no path to the incident service.
	Unrelated float64
}

// DefaultGraphConfig returns the decay settings the CLI uses.
func DefaultGraphConfig() GraphConfig {
	return GraphConfig{
		UpstreamDecay:   0.60,
		DownstreamDecay: 0.35,
		MaxHops:         3,
		UnknownService:  0.30,
		Unrelated:       0.10,
	}
}

// NewDependencyGraph builds a graph from a service-to-dependencies map. Keys are
// services, values are the services they call.
func NewDependencyGraph(dependsOn map[string][]string, cfg GraphConfig) *DependencyGraph {
	forward := make(map[string][]string, len(dependsOn))
	reverse := make(map[string][]string)
	for service, deps := range dependsOn {
		for _, dep := range deps {
			if dep == service {
				continue // a self-edge says nothing; the same-service case is already 1.0
			}
			forward[service] = append(forward[service], dep)
			reverse[dep] = append(reverse[dep], service)
		}
	}
	for _, edges := range forward {
		sort.Strings(edges)
	}
	for _, edges := range reverse {
		sort.Strings(edges)
	}
	return &DependencyGraph{
		dependsOn:  forward,
		dependedBy: reverse,
		cfg:        cfg,
		cached:     make(map[string]hopDistances),
	}
}

// LoadDependencyGraph reads a graph from a JSON file mapping each service to the
// services it calls:
//
//	{
//	  "checkout": ["auth", "payments"],
//	  "payments": ["ledger"]
//	}
//
// A service that only appears as a dependency does not need its own key.
func LoadDependencyGraph(path string, cfg GraphConfig) (*DependencyGraph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string][]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s: dependency graph is empty", path)
	}
	return NewDependencyGraph(raw, cfg), nil
}

// Services returns every service the graph knows about, sorted.
func (g *DependencyGraph) Services() []string {
	seen := make(map[string]struct{})
	for service, deps := range g.dependsOn {
		seen[service] = struct{}{}
		for _, dep := range deps {
			seen[dep] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for service := range seen {
		out = append(out, service)
	}
	sort.Strings(out)
	return out
}

// Relation implements ServiceGraph.
func (g *DependencyGraph) Relation(eventService, incidentService string) float64 {
	switch eventService {
	case "":
		return g.cfg.UnknownService
	case incidentService:
		return 1.0
	}

	distances := g.distancesFrom(incidentService)
	best := g.cfg.Unrelated
	if hops, ok := distances.upstream[eventService]; ok {
		best = max(best, pow(g.cfg.UpstreamDecay, hops))
	}
	if hops, ok := distances.downstream[eventService]; ok {
		best = max(best, pow(g.cfg.DownstreamDecay, hops))
	}
	return best
}

// Explain implements RelationExplainer, so the score breakdown can say why a
// service on a different name scored as high as it did.
func (g *DependencyGraph) Explain(eventService, incidentService string) string {
	switch eventService {
	case "":
		return "no service on event"
	case incidentService:
		return "same service"
	}

	distances := g.distancesFrom(incidentService)
	up, hasUp := distances.upstream[eventService]
	down, hasDown := distances.downstream[eventService]

	switch {
	case hasUp && (!hasDown || pow(g.cfg.UpstreamDecay, up) >= pow(g.cfg.DownstreamDecay, down)):
		return fmt.Sprintf("%s depends on %s, %s away", incidentService, eventService, hopWord(up))
	case hasDown:
		return fmt.Sprintf("%s depends on %s, %s away", eventService, incidentService, hopWord(down))
	default:
		return "no path in the graph"
	}
}

func hopWord(hops int) string {
	if hops == 1 {
		return "1 hop"
	}
	return fmt.Sprintf("%d hops", hops)
}

// distancesFrom runs the two breadth-first searches for one incident service and
// caches the result, since Rank calls Relation once per candidate against the
// same incident service.
func (g *DependencyGraph) distancesFrom(incidentService string) hopDistances {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cached, ok := g.cached[incidentService]; ok {
		return cached
	}
	distances := hopDistances{
		upstream:   bfs(g.dependsOn, incidentService, g.cfg.MaxHops),
		downstream: bfs(g.dependedBy, incidentService, g.cfg.MaxHops),
	}
	g.cached[incidentService] = distances
	return distances
}

// bfs returns hop counts from start over the given adjacency, excluding start
// itself and stopping at maxHops. Cycles are handled by the visited set, which
// matters because real dependency graphs are rarely acyclic.
func bfs(adjacency map[string][]string, start string, maxHops int) map[string]int {
	distances := make(map[string]int)
	if maxHops <= 0 {
		return distances
	}
	visited := map[string]bool{start: true}
	frontier := []string{start}
	for hop := 1; hop <= maxHops && len(frontier) > 0; hop++ {
		var next []string
		for _, node := range frontier {
			for _, neighbour := range adjacency[node] {
				if visited[neighbour] {
					continue
				}
				visited[neighbour] = true
				distances[neighbour] = hop
				next = append(next, neighbour)
			}
		}
		frontier = next
	}
	return distances
}

func pow(base float64, exponent int) float64 {
	result := 1.0
	for i := 0; i < exponent; i++ {
		result *= base
	}
	return result
}
