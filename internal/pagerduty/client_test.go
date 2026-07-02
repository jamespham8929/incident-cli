package pagerduty

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pd "github.com/PagerDuty/go-pagerduty"
	"github.com/stretchr/testify/require"
)

// serve starts an httptest server that answers every request with the given
// status and body, and returns a Client whose underlying SDK client points at
// it. This exercises the request and response handling without reaching the
// real PagerDuty API.
func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{apiKey: "test-token", client: pd.NewClient("test-token", pd.WithAPIEndpoint(srv.URL))}
}

func TestCreateIncident(t *testing.T) {
	c := serve(t, http.StatusCreated, `{"incident":{"id":"PDABC","title":"Checkout down","html_url":"https://acme.pagerduty.com/incidents/PDABC"}}`)

	inc, err := c.CreateIncident(CreateIncidentInput{Title: "Checkout down", Severity: "P1", ServiceID: "SVC1"})
	require.NoError(t, err)
	require.Equal(t, "PDABC", inc.ID)
	require.Equal(t, "Checkout down", inc.Title)
	require.Equal(t, "P1", inc.Severity)
	require.Equal(t, "https://acme.pagerduty.com/incidents/PDABC", inc.URL)
}

func TestCreateIncidentError(t *testing.T) {
	c := serve(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)

	_, err := c.CreateIncident(CreateIncidentInput{Title: "x", ServiceID: "SVC1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "PagerDuty API error")
}

func TestResolveIncident(t *testing.T) {
	c := serve(t, http.StatusOK, `{"incidents":[{"id":"PDABC","status":"resolved"}]}`)
	require.NoError(t, c.ResolveIncident("PDABC", "rolled back the deploy"))
}

func TestResolveIncidentError(t *testing.T) {
	c := serve(t, http.StatusInternalServerError, `{}`)
	require.Error(t, c.ResolveIncident("PDABC", ""))
}

func TestGetIncident(t *testing.T) {
	c := serve(t, http.StatusOK, `{"incident":{"id":"PDABC","title":"Checkout down","html_url":"https://acme/PDABC","created_at":"2026-01-15T12:00:00Z","last_status_change_at":"2026-01-15T12:35:00Z"}}`)

	inc, err := c.GetIncident("PDABC")
	require.NoError(t, err)
	require.Equal(t, "PDABC", inc.ID)
	require.Equal(t, "Checkout down", inc.Title)
	require.False(t, inc.CreatedAt.IsZero())
	require.False(t, inc.ResolvedAt.IsZero())
}

func TestGetIncidentError(t *testing.T) {
	c := serve(t, http.StatusNotFound, `{"error":{"message":"Not Found"}}`)

	_, err := c.GetIncident("nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "PagerDuty API error")
}

func TestListRecentIncidents(t *testing.T) {
	c := serve(t, http.StatusOK, `{"incidents":[{"id":"PD1","title":"Checkout latency","created_at":"2026-01-15T12:00:00Z","html_url":"https://acme/PD1","service":{"summary":"checkout"}}],"more":false}`)

	incs, err := c.ListRecentIncidents(time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, incs, 1)
	require.Equal(t, "PD1", incs[0].ID)
	require.Equal(t, "checkout", incs[0].Service)
	require.False(t, incs[0].CreatedAt.IsZero())
}

func TestListRecentIncidentsError(t *testing.T) {
	c := serve(t, http.StatusInternalServerError, `{}`)

	_, err := c.ListRecentIncidents(time.Now())
	require.Error(t, err)
	require.Contains(t, err.Error(), "PagerDuty API error")
}

func TestSeverityToUrgency(t *testing.T) {
	require.Equal(t, "high", severityToUrgency("P1"))
	require.Equal(t, "high", severityToUrgency("P2"))
	require.Equal(t, "low", severityToUrgency("P3"))
	require.Equal(t, "low", severityToUrgency("P4"))
	require.Equal(t, "low", severityToUrgency(""))
}
