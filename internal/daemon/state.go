package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// scheduleState is schedule.State on disk, plus the diagnostics already
// reported. Restarts and sleeps must not replay or lose runs, which is the only
// reason any of it is persisted.
type scheduleState struct {
	LastOccurrence map[string]time.Time `json:"last_occurrence"`
	// Invalid is the set of diagnostics already reported, so an unfixed typo
	// is logged once rather than every tick.
	Invalid map[string]bool `json:"invalid,omitempty"`
}

func statePath() string {
	return filepath.Join(config.StateDir(), "schedule.json")
}

func loadState() *scheduleState {
	s := &scheduleState{LastOccurrence: map[string]time.Time{}, Invalid: map[string]bool{}}
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return s
	}
	if json.Unmarshal(raw, s) != nil || s.LastOccurrence == nil {
		s.LastOccurrence = map[string]time.Time{}
	}
	if s.Invalid == nil {
		s.Invalid = map[string]bool{}
	}
	return s
}

func (s *scheduleState) save() error {
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), raw, 0o644)
}
