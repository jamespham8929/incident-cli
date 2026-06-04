package timeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileSourceFiltersWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.json")
	// baseTime is 2026-01-15T12:00:00Z. With a 30m window: -10m is in scope,
	// -90m is too old, +5m is after detection.
	content := `[
	  {"id":"in","source":"deploy","service":"checkout","title":"in","timestamp":"2026-01-15T11:50:00Z","magnitude":0.5},
	  {"id":"old","source":"deploy","service":"checkout","title":"old","timestamp":"2026-01-15T10:30:00Z","magnitude":0.5},
	  {"id":"after","source":"alert","service":"checkout","title":"after","timestamp":"2026-01-15T12:05:00Z","magnitude":0.5}
	]`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	events, err := NewFileSource(path).Fetch(30*time.Minute, baseTime)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "in", events[0].ID)
	assert.Equal(t, SourceDeploy, events[0].Source)
}

func TestGatherMergesSources(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}
	a := write("a.json", `[{"id":"a1","source":"deploy","service":"checkout","title":"a1","timestamp":"2026-01-15T11:55:00Z","magnitude":0.5}]`)
	b := write("b.json", `[{"id":"b1","source":"config_change","service":"checkout","title":"b1","timestamp":"2026-01-15T11:58:00Z","magnitude":0.5}]`)

	events, err := Gather(baseTime, time.Hour, NewFileSource(a), NewFileSource(b))
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestFileSourceMissingFile(t *testing.T) {
	_, err := NewFileSource("/no/such/file.json").Fetch(time.Hour, baseTime)
	assert.Error(t, err)
}
