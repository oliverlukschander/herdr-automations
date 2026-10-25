package herdr

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewAPIErrorPrefersHerdrsOwnCode(t *testing.T) {
	body := []byte(`{"error":{"code":"agent_pane_busy","message":"pane w1T:p1 is not an available shell"},"id":"cli:agent:start"}`)
	err := newAPIError([]string{"agent", "start", "triage"}, body, "", errors.New("exit status 1"))

	if !HasCode(err, CodePaneBusy) {
		t.Fatalf("code not recovered from %v", err)
	}
	want := "agent start: agent_pane_busy: pane w1T:p1 is not an available shell"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestNewAPIErrorFallsBackToTheExecError(t *testing.T) {
	// The failure that made a run undiagnosable: herdr exits non-zero and
	// prints nothing, so the only thing left to report is why the process died.
	err := newAPIError([]string{"worktree", "create"}, nil, "", errors.New("exit status 1"))

	want := "worktree create: exit status 1"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestNewAPIErrorKeepsStderrOverTheExecError(t *testing.T) {
	// "exit status 1" says less than whatever herdr wrote, so it loses.
	err := newAPIError([]string{"worktree", "create"}, nil,
		"  fatal: not a git repository\n", errors.New("exit status 128"))

	want := "worktree create: fatal: not a git repository"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestNewAPIErrorSurvivesANilExecError(t *testing.T) {
	if err := newAPIError([]string{"pane", "read"}, nil, "", nil); err == nil {
		t.Fatal("want an error even with nothing to say")
	}
}

func TestNotificationArgsOmitsAnEmptyBody(t *testing.T) {
	got := notificationArgs("triage failed", "", SoundRequest)
	for _, a := range got {
		if a == "--body" {
			t.Fatalf("args = %v, want no --body flag for an empty body", got)
		}
	}
}

func TestNotificationArgsPassesTheTitleAsAPositionalArgument(t *testing.T) {
	got := notificationArgs("triage failed", "boom", SoundRequest)
	want := []string{"notification", "show", "triage failed",
		"--body", "boom", "--position", notificationPosition, "--sound", "request"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestTabCreate(t *testing.T) {
	for _, tc := range []struct {
		name, response, pane string
		wantError            bool
	}{
		{"created", `{"result":{"type":"tab_created","root_pane":{"pane_id":"w7:p2"}}}`, "w7:p2", false},
		{"missing pane", `{"result":{"type":"tab_created"}}`, "", true},
		{"invalid response", `not json`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "herdr")
			argsFile := filepath.Join(dir, "args")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HERDR_TEST_ARGS\"\nprintf '%s\\n' \"$HERDR_TEST_RESPONSE\"\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_BIN_PATH", bin)
			t.Setenv("HERDR_TEST_ARGS", argsFile)
			t.Setenv("HERDR_TEST_RESPONSE", tc.response)
			pane, err := (Client{}).TabCreate("w7", "/a repo", "auto: daily summary")
			if (err != nil) != tc.wantError || pane != tc.pane {
				t.Fatalf("pane=%q err=%v", pane, err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			want := []string{"tab", "create", "--workspace", "w7", "--cwd", "/a repo", "--label", "auto: daily summary", "--no-focus"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("args=%q, want %q", got, want)
			}
		})
	}
}

// argsOf is the argv call hands the herdr binary.
func argsOf(t *testing.T, call func() error) []string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	argsFile := filepath.Join(dir, "args")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HERDR_TEST_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("HERDR_TEST_ARGS", argsFile)
	if err := call(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// An empty name is a clear, not a rename to "".
func TestAgentRename(t *testing.T) {
	for name, want := range map[string][]string{
		"soc-1025-1705": {"agent", "rename", "w2:p1", "soc-1025-1705"},
		"":              {"agent", "rename", "w2:p1", "--clear"},
	} {
		if got := argsOf(t, func() error { return (Client{}).AgentRename("w2:p1", name) }); !reflect.DeepEqual(got, want) {
			t.Errorf("AgentRename(%q): args=%q, want %q", name, got, want)
		}
	}
}

// One workspace, never its group.
func TestWorkspaceClose(t *testing.T) {
	want := []string{"workspace", "close", "wMA"}
	if got := argsOf(t, func() error { return (Client{}).WorkspaceClose("wMA") }); !reflect.DeepEqual(got, want) {
		t.Errorf("args=%q, want %q", got, want)
	}
}

func TestPaneRunPreservesShellArguments(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "herdr")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nshift 3\nexec sh -c \"$*\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", fake)
	output := filepath.Join(dir, "output")
	t.Setenv("HERDR_TEST_OUT", output)
	if err := (Client{}).PaneRun("w1:p1", "sh", "-c", `printf '%s' "two words and an apostrophe: '" > "$HERDR_TEST_OUT"`); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two words and an apostrophe: '" {
		t.Fatalf("got %q", got)
	}
}

func TestAPIErrorDecodesStderrEnvelope(t *testing.T) {
	err := newAPIError([]string{"agent", "get"}, nil, `{"error":{"code":"agent_not_running","message":"gone"}}`, errors.New("exit 1"))
	if !HasCode(err, CodeAgentGone) {
		t.Fatalf("lost code: %v", err)
	}
}

func TestTabCloseVerifiesRemoval(t *testing.T) {
	for _, disappears := range []bool{true, false} {
		dir := t.TempDir()
		fake := filepath.Join(dir, "herdr")
		get := `echo '{"result":{"type":"tab_info"}}'`
		if disappears {
			get = `echo '{"error":{"code":"tab_not_found","message":"gone"}}' >&2; exit 1`
		}
		script := "#!/bin/sh\nif [ \"$2\" = close ]; then echo '{\"result\":{\"type\":\"ok\"}}'; else " + get + "; fi\n"
		if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HERDR_BIN_PATH", fake)
		if err := (Client{}).TabClose("tab"); (err == nil) != disappears {
			t.Fatalf("disappears=%t error=%v", disappears, err)
		}
	}
}

// A closed workspace is an answer, not an error; anything else herdr says is.
func TestWorkspaceExists(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\ncase \"$3\" in\n" +
		"  w1) echo '{\"result\":{\"type\":\"workspace_info\"}}' ;;\n" +
		"  w2) echo '{\"error\":{\"code\":\"workspace_not_found\",\"message\":\"gone\"}}' >&2; exit 1 ;;\n" +
		"  *) echo '{\"error\":{\"code\":\"server_unreachable\",\"message\":\"no server\"}}' >&2; exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", fake)
	for id, want := range map[string]bool{"w1": true, "w2": false} {
		if open, err := (Client{}).WorkspaceExists(id); open != want || err != nil {
			t.Errorf("%s: open=%v err=%v", id, open, err)
		}
	}
	if open, err := (Client{}).WorkspaceExists("w3"); open || !HasCode(err, "server_unreachable") {
		t.Errorf("unreachable: open=%v err=%v", open, err)
	}
}
