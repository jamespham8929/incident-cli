package cmd

import (
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/jamespham/incident-cli/internal/slack"
)

// pagerDutyAPI and slackAPI are the slices of the real clients that the
// commands actually use. Defining them as interfaces lets tests substitute
// fakes for the package-level constructors below instead of reaching the
// PagerDuty and Slack APIs.
type pagerDutyAPI interface {
	CreateIncident(pagerduty.CreateIncidentInput) (*pagerduty.Incident, error)
	ResolveIncident(id, resolution string) error
	GetIncident(id string) (*pagerduty.Incident, error)
	ListRecentIncidents(since time.Time) ([]pagerduty.Incident, error)
}

type slackAPI interface {
	CreateChannel(name string) (*slack.Channel, error)
	FindChannel(name string) (*slack.Channel, error)
	PostMessage(channelID, text string) error
	ArchiveChannel(channelID string) error
}

// newPagerDutyClient and newSlackClient are overridable seams. Production code
// gets the real clients; tests swap in fakes.
var newPagerDutyClient = func(apiKey string) pagerDutyAPI { return pagerduty.NewClient(apiKey) }

var newSlackClient = func(token string) slackAPI { return slack.NewClient(token) }
