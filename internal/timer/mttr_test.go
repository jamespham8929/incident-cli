package timer

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMTTRTimer(t *testing.T) {
	tmpDir := t.TempDir()

	newTimer := func() *MTTRTimer {
		return &MTTRTimer{
			stateDir: tmpDir,
			states:   make(map[string]*timerState),
		}
	}

	t.Run("start and stop measures elapsed time", func(t *testing.T) {
		timer := newTimer()
		timer.Start("INC-001")
		time.Sleep(10 * time.Millisecond)
		elapsed := timer.Stop("INC-001")
		assert.Greater(t, elapsed, time.Duration(0))
	})

	t.Run("elapsed returns running time before stop", func(t *testing.T) {
		timer := newTimer()
		timer.Start("INC-002")
		time.Sleep(5 * time.Millisecond)
		elapsed := timer.Elapsed("INC-002")
		assert.Greater(t, elapsed, time.Duration(0))
	})

	t.Run("persists state across instances", func(t *testing.T) {
		timer1 := newTimer()
		timer1.Start("INC-003")

		timer2 := newTimer()
		timer2.Load("INC-003")
		elapsed := timer2.Elapsed("INC-003")
		assert.Greater(t, elapsed, time.Duration(0))
	})

	t.Run("elapsed after stop returns fixed duration", func(t *testing.T) {
		timer := newTimer()
		timer.Start("INC-004")
		time.Sleep(10 * time.Millisecond)
		stopped := timer.Stop("INC-004")

		time.Sleep(10 * time.Millisecond)
		elapsed := timer.Elapsed("INC-004")
		assert.Equal(t, stopped, elapsed, "elapsed after stop should be constant")
	})

	t.Run("returns zero for unknown incident", func(t *testing.T) {
		timer := newTimer()
		elapsed := timer.Elapsed("INC-UNKNOWN")
		assert.Equal(t, time.Duration(0), elapsed)
	})
}

func TestStatePersistenceFile(t *testing.T) {
	tmpDir := t.TempDir()
	timer := &MTTRTimer{
		stateDir: tmpDir,
		states:   make(map[string]*timerState),
	}

	timer.Start("INC-005")

	stateFile := timer.statePath("INC-005")
	_, err := os.Stat(stateFile)
	require.NoError(t, err, "state file should exist after Start()")
}
