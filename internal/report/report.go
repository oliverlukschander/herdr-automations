// Package report is a run's own word on whether somebody needs to act: the
// agent (or command) writes it with `herdr-automations report`, the runner
// reads it once the work is over. Kept apart from the run's status, which
// says only whether the work ran.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// Env names the report's path in a command run's environment.
const Env = "HERDR_AUTOMATION_REPORT"

type Report struct {
	// Action is nil until the run says yes or no: a report without it is not
	// one, and must never close anything.
	Action  *bool  `json:"user_action_required"`
	Summary string `json:"summary"`
}

func (r Report) validate() error {
	if r.Action == nil || strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("a report needs yes or no, and a summary")
	}
	return nil
}

// NewPath reserves a fresh report path for one run.
func NewPath() (string, error) {
	dir := filepath.Join(config.StateDir(), "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "run-*.json")
	if err != nil {
		return "", err
	}
	return f.Name(), f.Close()
}

// Ask is what a prompt run is told on top of its prompt.
func Ask(path string) string {
	return fmt.Sprintf(`

When you are done, report whether the user needs to act by running:
%s report --file %s --action yes|no --summary '<what you found; if yes, what the user should do next>'
Use no only when the work is complete and nothing is left for a person to review, decide or fix; if unsure, use yes. Report even when there was nothing to do. A run reported with no is closed for you, so do not close it yourself.
`, quote(self()), quote(path))
}

// self is this binary, so the agent needs no herdr-automations on its PATH.
func self() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "herdr-automations"
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Read loads a run's report. An empty or half-written file is an error, the
// same as no file: only a whole report decides anything.
func Read(path string) (Report, error) {
	var r Report
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("unreadable report: %w", err)
	}
	return r, r.validate()
}

// Write saves a report whole or not at all: the runner may read it the moment
// the agent stops.
func Write(path string, r Report) error {
	if err := r.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
