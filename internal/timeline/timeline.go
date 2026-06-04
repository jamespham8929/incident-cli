package timeline

import (
	"fmt"
	"sort"
	"time"
)

// Entry is one row of a reconstructed timeline.
type Entry struct {
	Event          Event
	Offset         time.Duration // signed: negative before the incident, positive after
	BeforeIncident bool
}

// Build returns all events sorted by time, each annotated with its signed offset
// from the moment the incident was detected. The incident itself is not an entry;
// callers render it as the dividing line between negative and positive offsets.
func Build(inc Incident, events []Event) []Entry {
	entries := make([]Entry, 0, len(events))
	for _, e := range events {
		off := e.Timestamp.Sub(inc.DetectedAt)
		entries = append(entries, Entry{
			Event:          e,
			Offset:         off,
			BeforeIncident: off < 0,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Event.Timestamp.Before(entries[j].Event.Timestamp)
	})
	return entries
}

// FormatOffset renders a signed duration relative to the incident in a compact,
// human form: "12m00s before", "3m00s after", "at detection".
func FormatOffset(d time.Duration) string {
	if d == 0 {
		return "at detection"
	}
	if d < 0 {
		return compactDuration(-d) + " before"
	}
	return compactDuration(d) + " after"
}

func compactDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
