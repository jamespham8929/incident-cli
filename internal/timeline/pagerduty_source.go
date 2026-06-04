package timeline

import (
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
)

// PagerDutySource turns recent PagerDuty incidents into candidate alert events.
type PagerDutySource struct {
	client *pagerduty.Client
}

func NewPagerDutySource(client *pagerduty.Client) *PagerDutySource {
	return &PagerDutySource{client: client}
}

func (p *PagerDutySource) Name() string { return "pagerduty" }

func (p *PagerDutySource) Fetch(window time.Duration, before time.Time) ([]Event, error) {
	incidents, err := p.client.ListRecentIncidents(before.Add(-window))
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(incidents))
	for _, inc := range incidents {
		if inc.CreatedAt.After(before) {
			continue
		}
		events = append(events, Event{
			ID:        inc.ID,
			Source:    SourceAlert,
			Service:   inc.Service,
			Title:     inc.Title,
			Timestamp: inc.CreatedAt,
			Magnitude: 0.5,
		})
	}
	return events, nil
}
