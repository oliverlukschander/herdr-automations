package host

import (
	"errors"
	"github.com/DnzzL/herdr-automations/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseTabRequiresIdleAgentAndOriginalUnsharedTab(t *testing.T) {
	for _, tc := range []struct {
		status    string
		moved     bool
		wantClose bool
	}{
		{"idle", false, true}, {"done", false, true}, {"working", false, false}, {"blocked", false, false}, {"unknown", false, false}, {"idle", true, false},
	} {
		t.Run(tc.status+map[bool]string{true: " moved", false: ""}[tc.moved], func(t *testing.T) {
			closed := false
			ops := &fakeOps{agentStatus: func(string) (string, error) { return tc.status, nil },
				runTab: func(p, w, tab string) (string, error) {
					if p != "p" || w != "w" || tab != "original" {
						t.Fatal("wrong target")
					}
					if tc.moved {
						return "", errors.New("moved/shared")
					}
					return tab, nil
				}, tabClose: func(tab string) error { closed = true; return nil }}
			h := &live{ops: ops}
			err := h.CloseTab(Session{WorkspaceID: "w", PaneID: "p", TabID: "original"}, config.Automation{Workspace: config.WorkspaceExisting})
			if closed != tc.wantClose || (err == nil) != tc.wantClose {
				t.Fatalf("closed=%t error=%v", closed, err)
			}
		})
	}
}

func TestAwaitWaitsForReportAfterTransientIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	waits := 0
	ops := &fakeOps{agentStatus: func(string) (string, error) { return "idle", nil }, agentWait: func(string, time.Duration) error {
		waits++
		if waits == 3 {
			if err := os.WriteFile(path, []byte(`{"user_action_required":false,"summary":"done"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}}
	w := agentWorkWith(ops, config.Automation{CloseWhenNoAction: true, ReportPath: path})
	if err := w.await(Session{PaneID: "p"}, time.Second); err != nil {
		t.Fatal(err)
	}
	if waits != 3 {
		t.Fatalf("stopped after %d waits, before report", waits)
	}
}

func TestExitedAgentRequiresReportAndAvailableShell(t *testing.T) {
	for _, needed := range []bool{false, true} {
		for _, hasReport := range []bool{false, true} {
			for _, shell := range []bool{false, true} {
				path := filepath.Join(t.TempDir(), "result.json")
				if hasReport {
					value := "false"
					if needed {
						value = "true"
					}
					os.WriteFile(path, []byte(`{"user_action_required":`+value+`,"summary":"finished"}`), 0600)
				}
				closed := false
				ops := &fakeOps{
					agentWait:   func(string, time.Duration) error { return apiErr("agent wait", "agent_not_found") },
					agentStatus: func(string) (string, error) { return "", apiErr("agent get", "agent_not_found") },
					paneIsShell: func(string) bool { return shell },
					runTab:      func(string, string, string) (string, error) { return "tab", nil },
					tabClose:    func(string) error { closed = true; return nil },
				}
				a := config.Automation{CloseWhenNoAction: true, ReportPath: path, Workspace: config.WorkspaceExisting}
				s := Session{WorkspaceID: "w", PaneID: "p", TabID: "tab"}
				err := agentWorkWith(ops, a).await(s, time.Second)
				if (err == nil) != (hasReport && shell) {
					t.Fatalf("report=%t shell=%t error=%v", hasReport, shell, err)
				}
				if err == nil && !needed {
					if err := (&live{ops: ops}).CloseTab(s, a); err != nil {
						t.Fatal(err)
					}
				}
				if closed != (hasReport && shell && !needed) {
					t.Fatal("wrong closure")
				}
			}
		}
	}
}
