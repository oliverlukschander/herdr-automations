package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/host"
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
