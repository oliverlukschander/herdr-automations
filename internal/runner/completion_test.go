package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/host"
	"github.com/DnzzL/herdr-automations/internal/output"
	"github.com/DnzzL/herdr-automations/internal/report"
)

func TestActionReportControlsClosure(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		doErr      bool
		closed     int
		status     history.Status
	}{
		{"no action", `{"user_action_required":false,"summary":"All clear"}`, false, 1, history.StatusDone},
		{"action", `{"user_action_required":true,"summary":"Please renew login"}`, false, 0, history.StatusNeedsAction},
		{"missing", "", false, 0, history.StatusFailed},
		{"no verdict", `{"summary":"done"}`, false, 0, history.StatusFailed},
		{"invalid", `{"user_action_required":"no","summary":"done"}`, false, 0, history.StatusFailed},
		{"failed after report", `{"user_action_required":false,"summary":"done"}`, true, 0, history.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
			a := automation()
			a.Workspace = config.WorkspaceExisting
			a.CloseWhenNoAction = true
			h := &fakeHost{do: func(s host.Session, a config.Automation, d time.Duration) error {
				if !strings.Contains(a.Prompt, a.ReportPath) {
					t.Fatal("report path absent from prompt")
				}
				if tc.body != "" {
					if err := os.WriteFile(a.ReportPath, []byte(tc.body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.doErr {
					return errors.New("failed")
				}
				return nil
			}}
			err := New(h).Run(a, history.TriggerManual)
			if (err != nil) != (tc.status == history.StatusFailed) {
				t.Fatalf("error=%v", err)
			}
			got := runs(t)[0]
			if h.closed != tc.closed || got.Status != tc.status || got.UserActionRequired == nil || *got.UserActionRequired != (tc.closed == 0) {
				t.Fatalf("closed=%d, record=%+v", h.closed, got)
			}
			if tc.closed == 1 && (!got.TabClosed || got.Summary != "All clear") {
				t.Fatalf("report lost: %+v", got)
			}
		})
	}
}

// Every run whose tab stays open with an agent in it gives up the
// automation's agent name, or the next run's agent start is refused with
// agent_name_taken. A closed tab, an agent that never started and a closed
// workspace leave nothing holding the name.
func TestARunWhoseTabStaysOpenGivesUpItsAgentName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		report   string // empty: no completion report asked for
		body     string // what the agent reports; empty: nothing
		doErr    error
		closeErr error
		released bool
		status   history.Status
	}{
		{"done without a report", "", "", nil, nil, true, history.StatusDone},
		{"needs action", "yes", `{"user_action_required":true,"summary":"Please renew login"}`, nil, nil, true, history.StatusNeedsAction},
		{"no action, tab closed", "yes", `{"user_action_required":false,"summary":"All clear"}`, nil, nil, false, history.StatusDone},
		{"no action, close refused", "yes", `{"user_action_required":false,"summary":"All clear"}`, nil, errors.New("agent is working; tab kept open"), true, history.StatusFailed},
		{"report missing", "yes", "", nil, nil, true, history.StatusFailed},
		{"failed after the start", "", "", errors.New("waiting for the agent: timed out"), nil, true, history.StatusFailed},
		{"agent never started", "", "", fmt.Errorf("start grok agent: %w", host.ErrNoAgent), nil, false, history.StatusFailed},
		{"cancelled", "", "", fmt.Errorf("waiting for the agent: %w", host.ErrCancelled), nil, false, history.StatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
			a := automation()
			a.Workspace = config.WorkspaceRoot
			a.CloseWhenNoAction = tc.report != ""
			h := &fakeHost{
				do: func(_ host.Session, a config.Automation, _ time.Duration) error {
					if tc.body != "" {
						if err := os.WriteFile(a.ReportPath, []byte(tc.body), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return tc.doErr
				},
				closeTab: func(host.Session, config.Automation) error { return tc.closeErr },
			}
			r := New(h)
			busy := false
			h.release = func(host.Session, config.Automation) error {
				busy = r.Busy()
				return nil
			}
			r.Run(a, history.TriggerCron)

			got := runs(t)[0]
			if got.Status != tc.status || (len(h.released) == 1) != tc.released || len(h.released) > 1 {
				t.Fatalf("status = %s, released at %v", got.Status, h.released)
			}
			if !tc.released {
				return
			}
			if want, _ := history.StartedAt(got.RunID); !h.released[0].Equal(want) {
				t.Errorf("released with start %v, want the run's %v", h.released[0], want)
			}
			if !busy {
				t.Error("released after the in-flight slot was freed: the next run could race it")
			}
		})
	}
}

// Giving up the name is best effort: the run is already on record, and a name
// still held costs the next run, not this one.
func TestARefusedRenameLeavesTheOutcomeAlone(t *testing.T) {
	for _, report := range []bool{false, true} {
		t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
		a := automation()
		a.Workspace = config.WorkspaceRoot
		a.CloseWhenNoAction = report
		h := &fakeHost{
			do: func(_ host.Session, a config.Automation, _ time.Duration) error {
				if !report {
					return nil
				}
				return os.WriteFile(a.ReportPath, []byte(`{"user_action_required":true,"summary":"Please renew login"}`), 0600)
			},
			release: func(host.Session, config.Automation) error { return errors.New("agent rename: server_unreachable") },
		}
		want := map[bool]history.Status{false: history.StatusDone, true: history.StatusNeedsAction}[report]
		if err := New(h).Run(a, history.TriggerCron); err != nil || runs(t)[0].Status != want || len(h.released) != 1 {
			t.Errorf("report=%t: err = %v, status = %s, want %s", report, err, runs(t)[0].Status, want)
		}
	}
}

func TestNoCloseWithoutPreservedHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	a := automation()
	a.Workspace = config.WorkspaceExisting
	a.CloseWhenNoAction = true
	h := &fakeHost{do: func(s host.Session, a config.Automation, d time.Duration) error {
		no := false
		if err := report.Write(a.ReportPath, report.Report{UserActionRequired: &no, Summary: "Nothing left"}); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "history.jsonl")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		return os.Mkdir(p, 0700)
	}}
	if err := New(h).Run(a, history.TriggerManual); err == nil || h.closed != 0 {
		t.Fatalf("error=%v, closed=%d", err, h.closed)
	}
}

func TestReportsArePerRun(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	a := automation()
	a.Workspace = config.WorkspaceExisting
	a.CloseWhenNoAction = true
	var paths []string
	h := &fakeHost{do: func(s host.Session, a config.Automation, d time.Duration) error {
		paths = append(paths, a.ReportPath)
		if len(paths) > 1 {
			return nil
		}
		no := false
		return report.Write(a.ReportPath, report.Report{UserActionRequired: &no, Summary: "Done"})
	}}
	r := New(h)
	if err := r.Run(a, history.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(a, history.TriggerManual); err == nil {
		t.Fatal("stale verdict accepted")
	}
	if paths[0] == paths[1] || h.closed != 1 {
		t.Fatalf("paths=%v closed=%d", paths, h.closed)
	}
}

// A no-action run closes its tab, and the pane goes with it: the capture has
// to be taken first, or the run leaves nothing to read but its summary.
func TestThePaneIsKeptBeforeTheTabCloses(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	a := automation()
	a.Workspace = config.WorkspaceExisting
	a.CloseWhenNoAction = true
	var order []string
	h := &fakeHost{capture: "the agent's last answer", do: func(_ host.Session, a config.Automation, _ time.Duration) error {
		return os.WriteFile(a.ReportPath, []byte(`{"user_action_required":false,"summary":"All clear"}`), 0600)
	}}
	h.closeTab = func(host.Session, config.Automation) error {
		order = append(order, "close")
		return nil
	}
	if err := New(&capturingHost{h, &order}).Run(a, history.TriggerCron); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "capture,close" {
		t.Errorf("order = %v", order)
	}
	if c, ok := output.Read(runs(t)[0].RunID); !ok || c.Text != "the agent's last answer" {
		t.Errorf("capture = %+v %v", c, ok)
	}
}

// capturingHost notes when Capture is asked, in the same list as CloseTab.
type capturingHost struct {
	*fakeHost
	order *[]string
}

func (c *capturingHost) Capture(s host.Session) (string, error) {
	*c.order = append(*c.order, "capture")
	return c.fakeHost.Capture(s)
}
