package pagerduty

import (
	"fmt"
	"time"

	pd "github.com/PagerDuty/go-pagerduty"
)

type Client struct {
	apiKey string
	client *pd.Client
}

type CreateIncidentInput struct {
	Title       string
	Severity    string
	Description string
	ServiceID   string
}

type Incident struct {
	ID         string
	Title      string
	Severity   string
	Service    string
	URL        string
	CreatedAt  time.Time
	ResolvedAt time.Time
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		client: pd.NewClient(apiKey),
	}
}

func (c *Client) CreateIncident(input CreateIncidentInput) (*Incident, error) {
	urgency := severityToUrgency(input.Severity)
	resp, err := c.client.CreateIncident("incident-cli", &pd.CreateIncidentOptions{
		Title: input.Title,
		Service: &pd.APIReference{
			ID:   input.ServiceID,
			Type: "service_reference",
		},
		Urgency: urgency,
		Body: &pd.APIDetails{
			Type:    "incident_body",
			Details: input.Description,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("PagerDuty API error: %w", err)
	}

	return &Incident{
		ID:       resp.ID,
		Title:    resp.Title,
		Severity: input.Severity,
		URL:      resp.HTMLURL,
	}, nil
}

func (c *Client) ResolveIncident(id, resolution string) error {
	_, err := c.client.ManageIncidents("incident-cli", []pd.ManageIncidentsOptions{
		{
			ID:     id,
			Status: "resolved",
		},
	})
	return err
}

func (c *Client) GetIncident(id string) (*Incident, error) {
	resp, err := c.client.GetIncident(id)
	if err != nil {
		return nil, fmt.Errorf("PagerDuty API error: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, resp.CreatedAt)
	var resolvedAt time.Time
	if resp.LastStatusChangeAt != "" {
		resolvedAt, _ = time.Parse(time.RFC3339, resp.LastStatusChangeAt)
	}

	return &Incident{
		ID:         resp.ID,
		Title:      resp.Title,
		URL:        resp.HTMLURL,
		CreatedAt:  createdAt,
		ResolvedAt: resolvedAt,
	}, nil
}

// ListRecentIncidents returns incidents created at or after `since`, newest
// first. The timeline package turns these into candidate events, since a burst
// of related alerts just before a page is often the first visible symptom.
func (c *Client) ListRecentIncidents(since time.Time) ([]Incident, error) {
	resp, err := c.client.ListIncidents(pd.ListIncidentsOptions{
		Since:    since.UTC().Format(time.RFC3339),
		SortBy:   "created_at:desc",
		Limit:    100,
		Statuses: []string{"triggered", "acknowledged", "resolved"},
	})
	if err != nil {
		return nil, fmt.Errorf("PagerDuty API error: %w", err)
	}

	out := make([]Incident, 0, len(resp.Incidents))
	for _, inc := range resp.Incidents {
		createdAt, _ := time.Parse(time.RFC3339, inc.CreatedAt)
		out = append(out, Incident{
			ID:        inc.ID,
			Title:     inc.Title,
			Service:   inc.Service.Summary,
			URL:       inc.HTMLURL,
			CreatedAt: createdAt,
		})
	}
	return out, nil
}

func severityToUrgency(severity string) string {
	switch severity {
	case "P1", "P2":
		return "high"
	default:
		return "low"
	}
}
