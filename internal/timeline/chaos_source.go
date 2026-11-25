package timeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// SourceChaos is a chaos-operator run: a fault someone injected on purpose.
const SourceChaos EventSource = "chaos"

// chaosResource is the full name of the chaos-operator custom resource.
const chaosResource = "chaosexperiments.chaos.jamespham.io"

// ChaosLister returns a ChaosExperimentList as JSON, the same document
// `kubectl get chaosexperiments -A -o json` prints.
type ChaosLister interface {
	ListExperiments() ([]byte, error)
}

// ChaosFile reads a saved list from disk. It backs offline investigation and
// tests.
type ChaosFile struct{ Path string }

func (f ChaosFile) ListExperiments() ([]byte, error) { return os.ReadFile(f.Path) }

// ChaosKubectl asks the cluster through kubectl. kubectl already knows the
// responder's contexts and auth plugins, which a hand-built API client would
// have to reimplement.
type ChaosKubectl struct {
	// Context is the kubeconfig context. Empty means the current one.
	Context string
	// Run executes a command and returns its stdout. Nil means exec.
	Run func(name string, args ...string) ([]byte, error)
}

func (k ChaosKubectl) ListExperiments() ([]byte, error) {
	args := []string{"get", chaosResource, "--all-namespaces", "--output", "json"}
	if k.Context != "" {
		args = append(args, "--context", k.Context)
	}
	run := k.Run
	if run == nil {
		run = func(name string, args ...string) ([]byte, error) {
			out, err := exec.Command(name, args...).Output()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
				return nil, fmt.Errorf("%w: %s", err, exitErr.Stderr)
			}
			return out, err
		}
	}
	return run("kubectl", args...)
}

// ChaosSource turns the run records chaos-operator keeps in status.runs into
// candidate events. See chaos-operator's ADR 0004 for the record format.
type ChaosSource struct {
	lister ChaosLister
}

func NewChaosSource(lister ChaosLister) *ChaosSource { return &ChaosSource{lister: lister} }

func (c *ChaosSource) Name() string { return "chaos-runs" }

type chaosList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Status struct {
			Runs []chaosRun `json:"runs"`
		} `json:"status"`
	} `json:"items"`
}

type chaosRun struct {
	ID           string    `json:"id"`
	Trigger      string    `json:"trigger"`
	Type         string    `json:"type"`
	Service      string    `json:"service"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	Outcome      string    `json:"outcome"`
	Injected     *bool     `json:"injected"`
	PodsAffected int       `json:"podsAffected"`
}

// notInjected mirrors chaos-operator's list of outcomes where it stopped
// before touching a pod. It is only used for records written before the
// operator added the injected field.
var notInjected = map[string]bool{
	"no_targets": true, "skipped": true, "refused": true, "unsupported_for_safe_run": true,
}

func (r chaosRun) injected() bool {
	if r.Injected != nil {
		return *r.Injected
	}
	return !notInjected[r.Outcome]
}

// chaosMagnitude scores how hard a run hit. A run that tripped a guard or left
// the system unhealthy measurably hurt something. A run that held its steady
// state did less, but it still injected a fault.
func chaosMagnitude(outcome string) float64 {
	switch outcome {
	case "aborted", "did_not_recover":
		return 1.0
	case "succeeded", "completed":
		return 0.6
	default:
		return 0 // unknown, scored as neutral
	}
}

func (c *ChaosSource) Fetch(window time.Duration, before time.Time) ([]Event, error) {
	raw, err := c.lister.ListExperiments()
	if err != nil {
		return nil, fmt.Errorf("listing chaos experiments: %w", err)
	}
	var list chaosList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing chaos experiments: %w", err)
	}

	cutoff := before.Add(-window)
	var events []Event
	for _, item := range list.Items {
		for _, run := range item.Status.Runs {
			// A run that never touched a pod cannot have caused the page.
			if !run.injected() {
				continue
			}
			if run.StartedAt.Before(cutoff) || run.StartedAt.After(before) {
				continue
			}
			events = append(events, Event{
				ID:        run.ID,
				Source:    SourceChaos,
				Service:   run.Service,
				Title:     chaosTitle(item.Metadata.Name, run),
				Timestamp: run.StartedAt,
				Magnitude: chaosMagnitude(run.Outcome),
				Metadata: map[string]string{
					"experiment":    item.Metadata.Name,
					"namespace":     item.Metadata.Namespace,
					"type":          run.Type,
					"outcome":       run.Outcome,
					"trigger":       run.Trigger,
					"finished_at":   run.FinishedAt.UTC().Format(time.RFC3339),
					"pods_affected": fmt.Sprint(run.PodsAffected),
				},
			})
		}
	}
	return events, nil
}

func chaosTitle(experiment string, run chaosRun) string {
	target := run.Service
	if target == "" {
		target = "an unnamed service"
	}
	return fmt.Sprintf("chaos experiment %s: %s on %s, %s", experiment, run.Type, target, run.Outcome)
}
