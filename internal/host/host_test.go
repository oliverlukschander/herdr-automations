package host

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/herdr"
)

// fakeOps scripts Herdr's answers. A nil field means "this call is fine and
// says nothing", which keeps each test to the calls it actually cares about.
type fakeOps struct {
	runTab          func(string, string, string) (string, error)
	tabClose        func(string) error
	paneIsShell     func(string) bool
	tabCreate       func(workspaceID, cwd, label string) (string, error)
	worktreeCreate  func(repo, branch, label string) (string, string, error)
	workspaceCreate func(cwd, label string) (string, string, error)
	agentStart      func(name, kind, paneID string, args []string) error
	agentRename     func(target, name string) error
	agentSubmit     func(target, text string) error
	submitPending   func(paneID string) error
	agentStatus     func(target string) (string, error)
	agentWait       func(target string, d time.Duration) error
	paneRun         func(paneID string, command ...string) error
	paneRead        func(paneID string, lines int) (string, error)
	lookPath        func(file string) error
	agentInfo       func(target string) (herdr.AgentInfo, error)

	starts  int
	submits int
	pending int
	waits   int
	reads   int
	// renames is every AgentRename, as "target name".
	renames []string
}

func (f *fakeOps) WorktreeCreate(repo, branch, label string) (string, string, error) {
	if f.worktreeCreate == nil {
		return "w1", "w1:p1", nil
	}
	return f.worktreeCreate(repo, branch, label)
}

func (f *fakeOps) WorkspaceCreate(cwd, label string) (string, string, error) {
	if f.workspaceCreate == nil {
		return "w2", "w2:p1", nil
	}
	return f.workspaceCreate(cwd, label)
}

func (f *fakeOps) TabCreate(workspaceID, cwd, label string) (string, error) {
	if f.tabCreate == nil {
		return workspaceID + ":p1", nil
	}
	return f.tabCreate(workspaceID, cwd, label)
}

func (f *fakeOps) AgentStart(name, kind, paneID string, args []string) error {
	f.starts++
	if f.agentStart == nil {
		return nil
	}
	return f.agentStart(name, kind, paneID, args)
}

func (f *fakeOps) AgentRename(target, name string) error {
	f.renames = append(f.renames, target+" "+name)
	if f.agentRename == nil {
		return nil
	}
	return f.agentRename(target, name)
}

func (f *fakeOps) AgentSubmit(target, text string) error {
	f.submits++
	if f.agentSubmit == nil {
		return nil
	}
	return f.agentSubmit(target, text)
}

func (f *fakeOps) AgentSubmitPending(paneID string) error {
	f.pending++
	if f.submitPending == nil {
		return nil
	}
	return f.submitPending(paneID)
}

func (f *fakeOps) AgentStatus(target string) (string, error) {
	if f.agentStatus == nil {
		return "working", nil
	}
	return f.agentStatus(target)
}

func (f *fakeOps) AgentWait(target string, d time.Duration) error {
	f.waits++
	if f.agentWait == nil {
		return nil
	}
	return f.agentWait(target, d)
}

func (f *fakeOps) AgentInfo(target string) (herdr.AgentInfo, error) {
	if f.agentInfo == nil {
		return herdr.AgentInfo{}, nil
	}
	return f.agentInfo(target)
}

func (f *fakeOps) PaneRun(paneID string, command ...string) error {
	if f.paneRun == nil {
		return nil
	}
	return f.paneRun(paneID, command...)
}

func (f *fakeOps) PaneRead(paneID string, lines int) (string, error) {
	f.reads++
	if f.paneRead == nil {
		return "", nil
	}
	return f.paneRead(paneID, lines)
}

func (f *fakeOps) LookPath(file string) error {
	if f.lookPath == nil {
		return nil
	}
	return f.lookPath(file)
}

// HasCode is the real thing: the fakes return real *herdr.APIError values, so
// the code-recovery path under test is the one that ships.
func (f *fakeOps) HasCode(err error, code string) bool { return herdr.HasCode(err, code) }

// fast is defaultKnobs with the sleeps taken out.
func fast() knobs {
	return knobs{
		paneReady:        time.Minute,
		paneReadyPoll:    time.Millisecond,
		waitSlice:        30 * time.Second,
		statusPoll:       time.Millisecond,
		promptSettle:     5 * time.Millisecond,
		promptSettleLast: 5 * time.Millisecond,
		workflowPoll:     time.Millisecond,
	}
}

func apiErr(command, code string) error {
	return &herdr.APIError{Command: command, Code: code, Message: code}
}

func TestProvisionOpensAWorktreeOnABranchOfItsOwn(t *testing.T) {
	var gotBranch, gotLabel string
	ops := &fakeOps{worktreeCreate: func(repo, branch, label string) (string, string, error) {
		gotBranch, gotLabel = branch, label
		return "w7", "w7:p1", nil
	}}
	h := &live{ops: ops, knobs: fast()}

	s, err := h.Provision(config.Automation{
		Name: "Weekly sprint planning", Repo: "/x", Workspace: config.WorkspaceWorktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.WorkspaceID != "w7" || s.PaneID != "w7:p1" {
		t.Fatalf("session = %+v", s)
	}
	// git check-ref-format rejects spaces, so the name has to be slugged before
	// it can become a branch.
	if want := "auto/weekly-sprint-planning-"; len(gotBranch) <= len(want) || gotBranch[:len(want)] != want {
		t.Errorf("branch = %q, want it to start with %q", gotBranch, want)
	}
	if gotLabel != "auto: Weekly sprint planning" {
		t.Errorf("label = %q", gotLabel)
	}
}

func TestProvisionRootModeOpensTheRepoItself(t *testing.T) {
	called := false
	ops := &fakeOps{workspaceCreate: func(cwd, label string) (string, string, error) {
		called = true
		if cwd != "/repo" {
			t.Errorf("cwd = %q", cwd)
		}
		return "w9", "w9:p1", nil
	}}
	h := &live{ops: ops, knobs: fast()}

	if _, err := h.Provision(config.Automation{
		Name: "n", Repo: "/repo", Workspace: config.WorkspaceRoot,
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("root mode must not create a worktree")
	}
}

func TestProvisionRefusesAWorkspaceModeItDoesNotKnow(t *testing.T) {
	// Load validates this, so reaching here means something bypassed it. Saying
	// so beats provisioning nothing and reporting success.
	h := &live{ops: &fakeOps{}, knobs: fast()}
	if _, err := h.Provision(config.Automation{Name: "n", Workspace: "sandbox"}); err == nil {
		t.Fatal("want an error for an unknown workspace mode")
	}
}

func TestDoPicksTheWorkflowAdapterWhenOneIsNamed(t *testing.T) {
	ops := &fakeOps{
		paneRead: func(string, int) (string, error) { return "", errors.New("no screen") },
		lookPath: func(string) error { return errors.New("not found") },
	}
	h := &live{ops: ops, knobs: fast()}

	err := h.Do(Session{PaneID: "p"}, config.Automation{Workflow: "bump"}, time.Minute, nil)
	if err == nil || ops.starts != 0 {
		t.Fatalf("want the hwf adapter chosen, got err=%v starts=%d", err, ops.starts)
	}
}

func TestDoPicksTheAgentAdapterWhenThereIsAPrompt(t *testing.T) {
	ops := &fakeOps{}
	h := &live{ops: ops, knobs: fast()}

	if err := h.Do(Session{PaneID: "p"}, config.Automation{
		Agent: "claude", Prompt: "go", Name: "n",
	}, time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if ops.starts != 1 || ops.submits != 1 || ops.waits != 1 {
		t.Fatalf("starts=%d submits=%d waits=%d, want one of each", ops.starts, ops.submits, ops.waits)
	}
}

func TestSlugProducesValidBranchNames(t *testing.T) {
	cases := map[string]string{
		"Weekly sprint planning": "weekly-sprint-planning",
		"issue-triage":           "issue-triage",
		"Deps  bump!!":           "deps-bump",
		"  ~weird/name~  ":       "weird-name",
		"???":                    "automation",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentNameFitsHerdrsLimit(t *testing.T) {
	long := "a-very-long-automation-name-that-herdr-will-not-accept"
	got := agentName(long)
	if len(got) > 32 {
		t.Fatalf("agentName(%q) = %q, %d chars", long, got, len(got))
	}
	if got[len(got)-1] == '-' {
		t.Errorf("agentName(%q) = %q, want no trailing dash", long, got)
	}
}

func TestProvisionExistingUsesFreshTabsInTheSameWorkspace(t *testing.T) {
	calls := 0
	ops := &fakeOps{
		workspaceCreate: func(string, string) (string, string, error) {
			t.Fatal("created a workspace")
			return "", "", nil
		},
		worktreeCreate: func(string, string, string) (string, string, error) {
			t.Fatal("created a worktree")
			return "", "", nil
		},
		tabCreate: func(workspaceID, cwd, label string) (string, error) {
			if workspaceID != "w7" || cwd != "/repo" || !strings.HasPrefix(label, "auto: inbox ") {
				t.Fatalf("unexpected tab: %q %q %q", workspaceID, cwd, label)
			}
			calls++
			return fmt.Sprintf("w7:p%d", calls), nil
		},
	}
	h := &live{ops: ops, knobs: fast()}
	a := config.Automation{Name: "inbox", Repo: "/repo", Workspace: config.WorkspaceExisting, WorkspaceID: "w7"}
	first, err := h.Provision(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Provision(a)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceID != second.WorkspaceID || first.PaneID == second.PaneID || calls != 2 {
		t.Fatalf("runs must share a workspace with distinct panes: %+v %+v", first, second)
	}
	ops.tabCreate = func(string, string, string) (string, error) { return "", errors.New("workspace gone") }
	if _, err := h.Provision(a); err == nil {
		t.Fatal("missing workspace must fail, never create a replacement")
	}
}

func TestExistingRunsHaveDistinctLiveAgentNames(t *testing.T) {
	seen := map[string]bool{}
	ops := &fakeOps{agentStart: func(name, kind, paneID string, args []string) error {
		if seen[name] || len(name) > 32 {
			t.Fatalf("invalid or reused name %q", name)
		}
		seen[name] = true
		return nil
	}}
	w := agentWork{ops: ops, knobs: fast(), a: config.Automation{Name: strings.Repeat("long-name", 8), Workspace: config.WorkspaceExisting}}
	// Herdr's pane IDs are case-sensitive; agent names are lowercase only. w1D
	// and w1d are two different live tabs and must not fold onto one name.
	for _, pane := range []string{"w7:p1", "w7:p2", "w1D:p1", "w1d:p1", "w17:pB", "w17:pb"} {
		if err := w.start(Session{PaneID: pane}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentNameIsTheAutomationsOutsideExistingMode(t *testing.T) {
	// The pane ID only earns its place where earlier runs stay open. Elsewhere
	// the sidebar should keep reading "inbox", not "w7-p1-inbox".
	for _, mode := range []config.Workspace{config.WorkspaceWorktree, config.WorkspaceRoot} {
		a := config.Automation{Name: "inbox", Workspace: mode}
		if got := agentNameFor(a, "w7:p1"); got != "inbox" {
			t.Errorf("agentNameFor(%s) = %q, want %q", mode, got, "inbox")
		}
	}
}

// runStart is a run's start as history reads it back: local time.
var runStart = time.Date(2026, 10, 25, 17, 5, 42, 0, time.Local)

// A kept run's agent gives up the plain name for one with the run's start in
// it, so the next run of the automation can start under the plain name.
func TestReleaseNameRenamesTheAgentToTheRunsStart(t *testing.T) {
	for _, mode := range []config.Workspace{config.WorkspaceWorktree, config.WorkspaceRoot} {
		ops := &fakeOps{}
		a := config.Automation{Name: "[SOC] Grow X Account", Workspace: mode, Agent: "grok", Prompt: "go"}
		if err := (&live{ops: ops, knobs: fast()}).ReleaseName(Session{PaneID: "w2:p1"}, a, runStart); err != nil {
			t.Fatal(err)
		}
		if want := []string{"w2:p1 soc-grow-x-account-1025-1705"}; !reflect.DeepEqual(ops.renames, want) {
			t.Errorf("%s: renames = %q, want %q", mode, ops.renames, want)
		}
	}
}

func TestKeptAgentNameTrimsTheNameNeverTheStart(t *testing.T) {
	// slug[:22] ends on the dash before "vwxyz", which must not double up
	// with the suffix's own.
	for _, name := range []string{"abcdefghijklmnopqrstu vwxyz", strings.Repeat("long-name", 8)} {
		got := keptAgentName(name, runStart)
		if len(got) > 32 || !strings.HasSuffix(got, "-1025-1705") || strings.Contains(got, "--") {
			t.Errorf("keptAgentName(%q) = %q", name, got)
		}
	}
}

func TestReleaseNameAnswersForWhatHerdrSays(t *testing.T) {
	gone := apiErr("agent rename", herdr.CodeAgentGone)
	notFound := apiErr("agent rename", "agent_not_found")
	taken := apiErr("agent rename", herdr.CodeNameTaken)
	down := apiErr("agent rename", "server_unreachable")
	for _, tc := range []struct {
		name    string
		answers []error // to each rename, in order
		renames []string
		wantErr error
	}{
		{"taken falls back to no name", []error{taken, nil}, []string{"p soc-1025-1705", "p "}, nil},
		{"agent exited", []error{gone}, []string{"p soc-1025-1705"}, nil},
		{"agent not found", []error{notFound}, []string{"p soc-1025-1705"}, nil},
		{"agent exited before the clear", []error{taken, gone}, []string{"p soc-1025-1705", "p "}, nil},
		{"anything else is the caller's to log", []error{down}, []string{"p soc-1025-1705"}, down},
		{"so is a refused clear", []error{taken, down}, []string{"p soc-1025-1705", "p "}, down},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := tc.answers
			ops := &fakeOps{agentRename: func(string, string) error {
				err := answers[0]
				answers = answers[1:]
				return err
			}}
			a := config.Automation{Name: "soc", Workspace: config.WorkspaceRoot}
			err := (&live{ops: ops, knobs: fast()}).ReleaseName(Session{PaneID: "p"}, a, runStart)
			if err != tc.wantErr || !reflect.DeepEqual(ops.renames, tc.renames) {
				t.Fatalf("err = %v, renames = %q", err, ops.renames)
			}
		})
	}
}

// Existing mode never held the plain name, and a workflow run started no
// agent of the plugin's to rename.
func TestReleaseNameLeavesExistingModeAndWorkflowsAlone(t *testing.T) {
	for _, a := range []config.Automation{
		{Name: "inbox", Workspace: config.WorkspaceExisting, WorkspaceID: "w7"},
		{Name: "storno", Workspace: config.WorkspaceRoot, Workflow: "storno-2026"},
	} {
		ops := &fakeOps{}
		if err := (&live{ops: ops, knobs: fast()}).ReleaseName(Session{PaneID: "w7:p3"}, a, runStart); err != nil || len(ops.renames) != 0 {
			t.Errorf("%s: err = %v, renames = %q", a.Name, err, ops.renames)
		}
	}
}

func (f *fakeOps) RunTab(pane, workspace, expected string) (string, error) {
	if f.runTab != nil {
		return f.runTab(pane, workspace, expected)
	}
	return "w1:t1", nil
}
func (f *fakeOps) TabClose(tab string) error {
	if f.tabClose != nil {
		return f.tabClose(tab)
	}
	return nil
}

func (f *fakeOps) PaneIsShell(paneID string) bool {
	return f.paneIsShell != nil && f.paneIsShell(paneID)
}

// The run is told which session its agent is in once the prompt is being
// worked on, and again when the agent settles: a /clear starts a new one.
func TestDoReportsTheAgentSessionWhenWorkingAndWhenSettled(t *testing.T) {
	sessions := []string{"s1", "s2"}
	ops := &fakeOps{agentInfo: func(string) (herdr.AgentInfo, error) {
		id := sessions[0]
		sessions = sessions[1:]
		return herdr.AgentInfo{Kind: "claude", SessionID: id, SessionSource: "herdr:claude", CWD: "/repo"}, nil
	}}
	h := &live{ops: ops, knobs: fast()}
	var seen []Agent
	err := h.Do(Session{PaneID: "p"}, config.Automation{Agent: "claude", Prompt: "go", Name: "n"}, time.Minute,
		func(a Agent) { seen = append(seen, a) })
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{{"s1", "herdr:claude", "/repo"}, {"s2", "herdr:claude", "/repo"}}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("seen = %+v", seen)
	}
}

// An agent herdr cannot describe is not a failed run.
func TestDoSaysNothingAboutAnAgentItCannotRead(t *testing.T) {
	ops := &fakeOps{agentInfo: func(string) (herdr.AgentInfo, error) { return herdr.AgentInfo{}, errors.New("gone") }}
	h := &live{ops: ops, knobs: fast()}
	called := false
	if err := h.Do(Session{PaneID: "p"}, config.Automation{Agent: "claude", Prompt: "go", Name: "n"}, time.Minute,
		func(Agent) { called = true }); err != nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestCaptureReadsTheRunsPane(t *testing.T) {
	var asked int
	ops := &fakeOps{paneRead: func(pane string, lines int) (string, error) { asked = lines; return "screen of " + pane, nil }}
	got, err := (&live{ops: ops, knobs: fast()}).Capture(Session{PaneID: "w1:p1"})
	if err != nil || got != "screen of w1:p1" || asked != CaptureLines {
		t.Fatalf("got %q, %v, %d lines", got, err, asked)
	}
}
