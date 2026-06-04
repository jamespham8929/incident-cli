package timeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSortsAndMarksOffsets(t *testing.T) {
	inc := incidentOn("checkout")
	events := []Event{
		evt("after", SourceAlert, "checkout", -5, 0.5),        // +5m
		evt("earliest", SourceDeploy, "checkout", 10, 0.5),    // -10m
		evt("middle", SourceConfigChange, "checkout", 2, 0.5), // -2m
	}
	entries := Build(inc, events)
	require.Len(t, entries, 3)

	// Sorted ascending by timestamp.
	assert.Equal(t, "earliest", entries[0].Event.ID)
	assert.Equal(t, "middle", entries[1].Event.ID)
	assert.Equal(t, "after", entries[2].Event.ID)

	// Before/after flags and offset signs.
	assert.True(t, entries[0].BeforeIncident)
	assert.True(t, entries[1].BeforeIncident)
	assert.False(t, entries[2].BeforeIncident)
	assert.Less(t, entries[0].Offset, time.Duration(0))
	assert.Greater(t, entries[2].Offset, time.Duration(0))
}

func TestFormatOffset(t *testing.T) {
	assert.Equal(t, "at detection", FormatOffset(0))
	assert.Equal(t, "2m00s before", FormatOffset(-2*time.Minute))
	assert.Equal(t, "5m00s after", FormatOffset(5*time.Minute))
	assert.Equal(t, "1h05m before", FormatOffset(-(time.Hour + 5*time.Minute)))
}

func TestCompactDuration(t *testing.T) {
	assert.Equal(t, "45s", compactDuration(45*time.Second))
	assert.Equal(t, "2m30s", compactDuration(2*time.Minute+30*time.Second))
	assert.Equal(t, "1h00m", compactDuration(time.Hour))
}
