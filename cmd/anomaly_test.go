package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakePrometheus answers every range query with one series: flat until
// `shiftAt`, then high.
func fakePrometheus(t *testing.T, shiftAt time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		start, _ := strconv.ParseInt(r.Form.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(r.Form.Get("end"), 10, 64)
		var points []string
		for ts := start; ts <= end; ts += 60 {
			v := "0.01"
			if !time.Unix(ts, 0).Before(shiftAt) {
				v = "0.9"
			}
			points = append(points, fmt.Sprintf("[%d,%q]", ts, v))
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"service":"payments-db"},"values":[%s]}]}}`, strings.Join(points, ","))
	}))
}

func TestInvestigateReadsMetricAnomalies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	srv := fakePrometheus(t, at.Add(-12*time.Minute))
	defer srv.Close()

	queries := filepath.Join(t.TempDir(), "anomalies.json")
	require.NoError(t, os.WriteFile(queries,
		[]byte(`[{"name":"db_pool_wait","query":"pool_wait_seconds"}]`), 0o600))

	out, err := executeCommand(t,
		"investigate",
		"--service", "checkout",
		"--at", at.Format(time.RFC3339),
		"--window", "1h",
		"--prometheus-url", srv.URL,
		"--anomaly-queries", queries,
	)
	require.NoError(t, err)
	require.Contains(t, out, "metric_anomaly/payments-db")
	require.Contains(t, out, "db_pool_wait rose to 0.9 from a baseline of 0.01")
}

func TestInvestigateNeedsBothPrometheusFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := executeCommand(t, "investigate", "--service", "checkout",
		"--prometheus-url", "http://prometheus:9090")
	require.Error(t, err)
	require.Contains(t, err.Error(), "go together")
}

func TestInvestigateRejectsBadAnomalyQueries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	queries := filepath.Join(t.TempDir(), "anomalies.json")
	require.NoError(t, os.WriteFile(queries, []byte(`[{"name":"x"}]`), 0o600))
	_, err := executeCommand(t, "investigate", "--service", "checkout",
		"--prometheus-url", "http://prometheus:9090", "--anomaly-queries", queries)
	require.Error(t, err)
	require.Contains(t, err.Error(), "loading anomaly queries")
}
