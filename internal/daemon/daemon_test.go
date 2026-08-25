package daemon

import (
	"testing"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
)

func invalidRecords(t *testing.T) int {
	t.Helper()
	runs, err := history.Runs("", 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range runs {
		if r.Status == history.StatusInvalid {
			n++
		}
	}
	return n
}

func TestReportInvalidComplainsOncePerBrokenEntry(t *testing.T) {
	// evaluate runs every 30 seconds. Re-recording an unfixed typo would bury
	// the log and the board under the same line all day.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	cfg := &config.Config{Invalid: []config.Diagnostic{
		{Name: "broken", Line: 3, Message: "broken: invalid cron"},
	}}
	state := &scheduleState{Invalid: map[string]bool{}}

	if !reportInvalid(cfg, state) {
		t.Error("the first sighting is a change worth persisting")
	}
	if reportInvalid(cfg, state) {
		t.Error("the second tick has nothing new to say")
	}
	if got := invalidRecords(t); got != 1 {
		t.Fatalf("recorded %d invalid entries, want 1", got)
	}
}

func TestReportInvalidSpeaksUpAgainAfterAFixAndABreak(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	broken := &config.Config{Invalid: []config.Diagnostic{
		{Name: "broken", Line: 3, Message: "broken: invalid cron"},
	}}
	fixed := &config.Config{}
	state := &scheduleState{Invalid: map[string]bool{}}

	reportInvalid(broken, state)
	if !reportInvalid(fixed, state) {
		t.Error("forgetting a fixed entry is a change worth persisting")
	}
	if len(state.Invalid) != 0 {
		t.Fatalf("state still remembers %v", state.Invalid)
	}
	reportInvalid(broken, state)
	if got := invalidRecords(t); got != 2 {
		t.Fatalf("recorded %d invalid entries, want 2: breaking it again is news", got)
	}
}

func TestReportInvalidNamesAnEntryTooBrokenToHaveAName(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	cfg := &config.Config{Invalid: []config.Diagnostic{{Line: 7, Message: "cannot decode"}}}

	reportInvalid(cfg, &scheduleState{Invalid: map[string]bool{}})

	runs, err := history.Runs("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Automation != "(unnamed)" {
		t.Fatalf("records = %+v, want one attributed to (unnamed)", runs)
	}
}
