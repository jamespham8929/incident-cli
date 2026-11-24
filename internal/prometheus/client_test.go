package prometheus

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryRangeParsesAMatrix(t *testing.T) {
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/query_range", r.URL.Path)
		require.NoError(t, r.ParseForm())
		form = map[string]string{
			"query": r.Form.Get("query"), "start": r.Form.Get("start"),
			"end": r.Form.Get("end"), "step": r.Form.Get("step"),
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"service":"db"},"values":[[1700000000,"1.5"],[1700000060.5,"NaN"],[1700000120,"2"]]}
		]}}`))
	}))
	defer srv.Close()

	start := time.Unix(1700000000, 0)
	series, err := NewClient(srv.URL+"/").QueryRange("up", start, start.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"query": "up", "start": "1700000000", "end": "1700000120", "step": "60"}, form)
	require.Len(t, series, 1)
	assert.Equal(t, "db", series[0].Labels["service"])
	// The NaN point is dropped.
	require.Len(t, series[0].Samples, 2)
	assert.Equal(t, 1.5, series[0].Samples[0].Value)
	assert.True(t, series[0].Samples[1].Time.Equal(time.Unix(1700000120, 0)))
}

func TestQueryRangeReportsPrometheusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error at char 3"}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).QueryRange("rate(", time.Now(), time.Now(), time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad_data")
	assert.Contains(t, err.Error(), "parse error")
}

func TestQueryRangeRejectsANonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).QueryRange("up", time.Now(), time.Now(), time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

func TestQueryRangeWantsAMatrix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).QueryRange("up", time.Now(), time.Now(), time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matrix")
}

func TestQueryRangeConnectionError(t *testing.T) {
	_, err := NewClient("http://127.0.0.1:1").QueryRange("up", time.Now(), time.Now(), time.Minute)
	require.Error(t, err)
}
