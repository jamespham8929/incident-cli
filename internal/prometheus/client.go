// Package prometheus runs range queries against the Prometheus HTTP API.
//
// Like the GitHub client, it is small and hand-rolled. The investigator needs
// one endpoint, and the official client library brings far more than that.
package prometheus

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sample is one point of a series.
type Sample struct {
	Time  time.Time
	Value float64
}

// Series is one labelled time series from a range query.
type Series struct {
	Labels  map[string]string
	Samples []Sample
}

// Client talks to one Prometheus server.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a client for the server at baseURL, for example
// http://prometheus-operated.monitoring:9090.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type rangeResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// QueryRange runs query from start to end at the given step.
func (c *Client) QueryRange(query string, start, end time.Time, step time.Duration) ([]Series, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))

	resp, err := c.http.PostForm(c.baseURL+"/api/v1/query_range", params)
	if err != nil {
		return nil, fmt.Errorf("query_range: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading query_range response: %w", err)
	}

	var parsed rangeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("query_range returned %s: %s", resp.Status, truncate(string(body), 200))
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("query_range failed (%s): %s", parsed.ErrorType, parsed.Error)
	}
	if parsed.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("query_range returned %q, want a matrix", parsed.Data.ResultType)
	}

	series := make([]Series, 0, len(parsed.Data.Result))
	for _, r := range parsed.Data.Result {
		s := Series{Labels: r.Metric, Samples: make([]Sample, 0, len(r.Values))}
		for _, pair := range r.Values {
			sample, ok := parseSample(pair)
			if !ok {
				continue
			}
			s.Samples = append(s.Samples, sample)
		}
		series = append(series, s)
	}
	return series, nil
}

// parseSample reads a [unix_seconds, "value"] pair. NaN and infinite values
// are dropped, since no statistic can use them.
func parseSample(pair [2]any) (Sample, bool) {
	ts, ok := pair[0].(float64)
	if !ok {
		return Sample{}, false
	}
	raw, ok := pair[1].(string)
	if !ok {
		return Sample{}, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return Sample{}, false
	}
	sec, frac := math.Modf(ts)
	return Sample{Time: time.Unix(int64(sec), int64(frac*1e9)).UTC(), Value: v}, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
