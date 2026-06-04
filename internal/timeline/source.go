package timeline

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Source supplies candidate events that ended no later than `before` and no
// earlier than `before - window`.
type Source interface {
	Name() string
	Fetch(window time.Duration, before time.Time) ([]Event, error)
}

// Gather collects events from every source into one slice. A failing source
// stops the gather, since an investigation built on partial data is misleading.
func Gather(before time.Time, window time.Duration, sources ...Source) ([]Event, error) {
	var all []Event
	for _, s := range sources {
		evs, err := s.Fetch(window, before)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", s.Name(), err)
		}
		all = append(all, evs...)
	}
	return all, nil
}

// FileSource reads events from a JSON array on disk. It backs offline
// investigation, replaying a recorded incident, and the benchmark.
type FileSource struct {
	path string
}

func NewFileSource(path string) *FileSource { return &FileSource{path: path} }

func (f *FileSource) Name() string { return "file:" + f.path }

type eventJSON struct {
	ID        string            `json:"id"`
	Source    string            `json:"source"`
	Service   string            `json:"service"`
	Title     string            `json:"title"`
	Timestamp time.Time         `json:"timestamp"`
	Magnitude float64           `json:"magnitude"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func (f *FileSource) Fetch(window time.Duration, before time.Time) ([]Event, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	var raw []eventJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", f.path, err)
	}
	cutoff := before.Add(-window)
	events := make([]Event, 0, len(raw))
	for _, r := range raw {
		if r.Timestamp.Before(cutoff) || r.Timestamp.After(before) {
			continue
		}
		events = append(events, Event{
			ID:        r.ID,
			Source:    EventSource(r.Source),
			Service:   r.Service,
			Title:     r.Title,
			Timestamp: r.Timestamp,
			Magnitude: r.Magnitude,
			Metadata:  r.Metadata,
		})
	}
	return events, nil
}
