package timer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type timerState struct {
	IncidentID string    `json:"incident_id"`
	StartedAt  time.Time `json:"started_at"`
	StoppedAt  time.Time `json:"stopped_at,omitempty"`
	Elapsed    string    `json:"elapsed,omitempty"`
}

// MTTRTimer persists timer state to disk so it survives between CLI invocations.
type MTTRTimer struct {
	stateDir string
	states   map[string]*timerState
}

func NewMTTRTimer() *MTTRTimer {
	home, _ := os.UserHomeDir()
	stateDir := filepath.Join(home, ".incident", "timers")
	_ = os.MkdirAll(stateDir, 0700)
	return &MTTRTimer{
		stateDir: stateDir,
		states:   make(map[string]*timerState),
	}
}

func (t *MTTRTimer) Start(incidentID string) {
	state := &timerState{
		IncidentID: incidentID,
		StartedAt:  time.Now().UTC(),
	}
	t.states[incidentID] = state
	t.persist(state)
}

func (t *MTTRTimer) Stop(incidentID string) time.Duration {
	t.Load(incidentID)
	state, ok := t.states[incidentID]
	if !ok {
		return 0
	}
	state.StoppedAt = time.Now().UTC()
	elapsed := state.StoppedAt.Sub(state.StartedAt)
	state.Elapsed = elapsed.String()
	t.persist(state)
	return elapsed
}

func (t *MTTRTimer) Elapsed(incidentID string) time.Duration {
	t.Load(incidentID)
	state, ok := t.states[incidentID]
	if !ok {
		return 0
	}
	if !state.StoppedAt.IsZero() {
		return state.StoppedAt.Sub(state.StartedAt)
	}
	return time.Since(state.StartedAt)
}

func (t *MTTRTimer) StartedAt() time.Time {
	for _, state := range t.states {
		return state.StartedAt
	}
	return time.Time{}
}

func (t *MTTRTimer) Load(incidentID string) {
	path := t.statePath(incidentID)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state timerState
	if err := json.Unmarshal(b, &state); err != nil {
		return
	}
	t.states[incidentID] = &state
}

func (t *MTTRTimer) persist(state *timerState) {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(t.statePath(state.IncidentID), b, 0600)
}

func (t *MTTRTimer) statePath(incidentID string) string {
	return filepath.Join(t.stateDir, fmt.Sprintf("%s.json", incidentID))
}
