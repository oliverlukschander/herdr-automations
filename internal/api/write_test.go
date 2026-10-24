package api

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
)

const kept = `# by hand
automations:
  - name: "[THO] Storno"
    # 09:30 on purpose
    cron: "CRON_TZ=Europe/Vienna 30 9 * * 1"
    repo: /home/agent/storno
    workspace: existing
    workspace_id: w3
    workflow: storno-2026
    timeout_minutes: 60

  - name: other
    cron: "@daily"
    repo: /x
    prompt: p
`

// fakeMachine answers the checks for a machine where only /ok exists.
func fakeMachine(t *testing.T) {
	t.Helper()
	saved := machineChecks
	t.Cleanup(func() { machineChecks = saved })
	machineChecks = checker{
		find:      func(bin string) (string, error) { return "/bin/" + bin, nil },
		isGit:     func(string) bool { return true },
		workflow:  func(string, string) error { return nil },
		workspace: func(id string) (bool, error) { return id == "w9", nil },
		zone:      func() string { return "Europe/Vienna" },
	}
}

func fileNow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func code(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return "not a Failure: " + err.Error()
}

func TestAJSONEditRewritesOnlyWhatChanged(t *testing.T) {
	sandbox(t, kept)
	res, err := Apply([]byte(`{"name":"[THO] Storno","cron":"CRON_TZ=Europe/Vienna 45 9 * * 1","repo":"/home/agent/storno",
		"workspace":"existing","workspace_id":"w3","workflow":"storno-2026","timeout_minutes":60}`), ApplyOptions{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(kept, "30 9 * * 1", "45 9 * * 1", 1)
	if res.Action != "replaced" || fileNow(t) != want {
		t.Errorf("action %s, file:\n%s", res.Action, fileNow(t))
	}
	if len(res.NextRuns) != 5 || res.Checks == nil || res.Runnable != nil {
		t.Errorf("next_runs %d, checks %v, runnable %v", len(res.NextRuns), res.Checks, res.Runnable)
	}
}

// The move: the source's text, comments and all, patched for this machine,
// written only if this machine can run it.
func TestTheMoveWritesThePatchedTextOnlyWhenRunnable(t *testing.T) {
	sandbox(t, "automations:\n  - name: other\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n")
	fakeMachine(t)
	src := "name: \"[THO] Storno\"\n# 09:30 on purpose\ncron: \"CRON_TZ=Europe/Vienna 30 9 * * 1\"\nrepo: /home/agent/storno\n" +
		"workspace: existing\nworkspace_id: w3\nworkflow: storno-2026\n"
	before := fileNow(t)

	_, err := Apply([]byte(src), ApplyOptions{New: true, RequireRunnable: true}, time.Now())
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeChecksFailed || len(f.Checks) == 0 || fileNow(t) != before {
		t.Fatalf("unrunnable move: %v (file changed: %v)", err, fileNow(t) != before)
	}

	dir := t.TempDir()
	patch := `{"disabled":true,"repo":"` + dir + `","workspace_id":"w9"}`
	dry, err := Apply([]byte(src), ApplyOptions{New: true, DryRun: true, Patch: patch}, time.Now())
	if err != nil || !*dry.Runnable || dry.Action != "created" || fileNow(t) != before {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	for _, c := range dry.Checks {
		if !c.OK && c.Level == "error" {
			t.Errorf("check failed: %+v", c)
		}
	}

	if _, err := Apply([]byte(src), ApplyOptions{New: true, RequireRunnable: true, Patch: patch}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := fileNow(t)
	want := before + "  - name: \"[THO] Storno\"\n    # 09:30 on purpose\n    cron: \"CRON_TZ=Europe/Vienna 30 9 * * 1\"\n" +
		"    repo: " + dir + "\n    workspace: existing\n    workspace_id: w9\n    workflow: storno-2026\n    disabled: true\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	if _, err := Apply([]byte(src), ApplyOptions{New: true, Patch: patch}, time.Now()); code(err) != CodeExists {
		t.Errorf("second --new: %v", err)
	}
}

func TestARenameKeepsTheEntryAndRefusesATakenName(t *testing.T) {
	sandbox(t, kept)
	body := []byte("name: renamed\ncron: \"@daily\"\nrepo: /x\nprompt: p\n")
	if _, err := Apply(body, ApplyOptions{RenameFrom: "[THO] Storno"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f, _ := config.Read()
	if f.Find("[THO] Storno") != nil || f.Find("renamed") == nil || f.Find("other") == nil {
		t.Errorf("after rename: %+v", f.Automations)
	}
	taken := []byte("name: other\ncron: \"@daily\"\nrepo: /x\nprompt: p\n")
	if _, err := Apply(taken, ApplyOptions{RenameFrom: "renamed"}, time.Now()); code(err) != CodeExists {
		t.Errorf("rename onto other: %v", err)
	}
}

func TestABodyThatWouldNotLoadIsRefusedWithItsOwnLine(t *testing.T) {
	sandbox(t, kept)
	_, err := Apply([]byte("name: x\n# why\ncron: \"0 99 * * *\"\nrepo: /x\nprompt: p\n"), ApplyOptions{}, time.Now())
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeInvalid || len(f.Diagnostics) != 1 || f.Diagnostics[0].Line != 1 {
		t.Fatalf("err = %+v", err)
	}
	if fileNow(t) != kept {
		t.Error("written anyway")
	}
	for _, bad := range []string{`{"name":"x","cron":"@daily","repo":"/x","prompt":"p","timout":1}`, "- name: x\n", "{name: x}", ""} {
		if _, err := Apply([]byte(bad), ApplyOptions{}, time.Now()); code(err) != CodeInvalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// A broken entry can be shown, fixed through apply, and deleted; pausing it
// says why not; a stale --if-match says what the file hashes to now.
func TestBrokenEntriesAndConflicts(t *testing.T) {
	broken := kept + "  - name: broken\n    cron: \"0 99 * * *\"\n    repo: /x\n    prompt: p\n"
	sandbox(t, broken)

	show, err := Show("broken", time.Now())
	if err != nil || show.Automation != nil || show.Diagnostic == nil || !strings.HasPrefix(show.YAML, "name: broken\n") {
		t.Fatalf("show: %+v %v", show, err)
	}
	if _, err := SetPaused("broken", true, "", time.Now()); code(err) != CodeInvalid {
		t.Errorf("pause broken: %v", err)
	}
	_, err = Apply([]byte("name: broken\ncron: \"@daily\"\nrepo: /x\nprompt: p\n"), ApplyOptions{IfMatch: config.Hash([]byte("stale"))}, time.Now())
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeConflict || f.CurrentSHA256 == nil || *f.CurrentSHA256 != config.Hash([]byte(broken)) {
		t.Fatalf("conflict: %+v", err)
	}
	res, err := Apply([]byte(strings.Replace(show.YAML, "0 99 * * *", "0 9 * * *", 1)), ApplyOptions{IfMatch: show.Config.SHA256}, time.Now())
	if err != nil || res.Automation.Name != "broken" || !res.Automation.Enabled {
		t.Fatalf("fixing it: %+v %v", res, err)
	}
	if _, err := Delete("broken", res.Config.SHA256); err != nil {
		t.Fatal(err)
	}
	if fileNow(t) != kept {
		t.Errorf("after fix and delete:\n%s", fileNow(t))
	}
}

// The hash a dry run reports is the one the real apply must match.
func TestADryRunReportsTheFileAsItIs(t *testing.T) {
	sandbox(t, kept)
	body := []byte("name: other\ncron: \"@hourly\"\nrepo: /x\nprompt: p\n")
	dry, err := Apply(body, ApplyOptions{DryRun: true}, time.Now())
	if err != nil || dry.Config.SHA256 != config.Hash([]byte(kept)) {
		t.Fatalf("dry run: %v %+v", err, dry.Config)
	}
	if _, err := Apply(body, ApplyOptions{IfMatch: dry.Config.SHA256}, time.Now()); err != nil {
		t.Errorf("apply after the dry run: %v", err)
	}
}

// Review finding 8: the checks take seconds (shells, hwf, Herdr) and must not
// hold the file lock while they run: a pause from the board or the app gets
// through in the meantime.
func TestChecksRunWithoutTheLock(t *testing.T) {
	sandbox(t, kept)
	fakeMachine(t)
	var paused error = errors.New("never tried")
	machineChecks.workspace = func(string) (bool, error) {
		done := make(chan error, 1)
		go func() { _, _, _, err := config.SetDisabled("other", true, ""); done <- err }()
		select {
		case paused = <-done:
		case <-time.After(3 * time.Second):
			paused = errors.New("blocked behind the checks")
		}
		return true, nil
	}
	body := []byte("name: \"[THO] Storno\"\ncron: \"@daily\"\nrepo: " + t.TempDir() + "\nworkspace: existing\nworkspace_id: w9\nworkflow: w\n")
	if _, err := Apply(body, ApplyOptions{DryRun: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if paused != nil {
		t.Errorf("pause during the checks: %v", paused)
	}
}

// ...and a write with --require-runnable writes exactly what was checked: if
// the file changed while the checks ran, it is a conflict, not a blind write.
// Review finding 11: such a write reports the checks it ran.
func TestRequireRunnableWritesWhatWasChecked(t *testing.T) {
	sandbox(t, kept)
	fakeMachine(t)
	dir := t.TempDir()
	body := []byte("name: new\ncron: \"@daily\"\nrepo: " + dir + "\nworkflow: w\n")

	res, err := Apply(body, ApplyOptions{RequireRunnable: true}, time.Now())
	if err != nil || res.Runnable == nil || !*res.Runnable || len(res.Checks) == 0 || res.Action != "created" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	machineChecks.workspace = func(string) (bool, error) { return true, nil }
	machineChecks.isGit = func(string) bool {
		config.SetDisabled("other", true, "") // someone else writes meanwhile
		return true
	}
	body = []byte("name: newer\ncron: \"@daily\"\nrepo: " + dir + "\nworkspace: worktree\nprompt: p\n")
	if _, err := Apply(body, ApplyOptions{RequireRunnable: true}, time.Now()); code(err) != CodeConflict {
		t.Errorf("write after a change during the checks: %v", err)
	}
	if f, _ := config.Read(); f.Item("newer") != nil {
		t.Error("written anyway")
	}
}

// Review round 2, finding 2: the second of two entries named x is listed
// invalid with its own text; applying that text by name replaced the first,
// good one. Now nothing by that name is edited (finding 4: nor deleted or
// renamed), and the refusal names both lines.
func TestFixingADuplicateByNameIsRefused(t *testing.T) {
	raw := "automations:\n  - name: x\n    cron: \"@daily\"\n    repo: /good\n    prompt: keep me\n" +
		"  - name: x\n    cron: \"@daily\"\n    repo: /dup\n    prompt: p\n"
	sandbox(t, raw)
	list, err := List(time.Now())
	if err != nil || len(list.Invalid) != 1 {
		t.Fatal(err)
	}
	attempts := map[string]error{}
	_, attempts["apply its own yaml"] = Apply([]byte(list.Invalid[0].YAML), ApplyOptions{}, time.Now())
	_, attempts["rename"] = Apply([]byte("name: y\ncron: \"@daily\"\nrepo: /y\nprompt: p\n"), ApplyOptions{RenameFrom: "x"}, time.Now())
	_, attempts["delete"] = Delete("x", "")
	_, attempts["pause"] = SetPaused("x", true, "", time.Now())
	for what, err := range attempts {
		var f *Failure
		if !errors.As(err, &f) || f.Code != CodeInvalid || len(f.Diagnostics) != 2 ||
			f.Diagnostics[0].Line != 2 || f.Diagnostics[1].Line != 6 || !strings.Contains(f.Message, "lines 2, 6") {
			t.Errorf("%s: %+v", what, err)
		}
	}
	if fileNow(t) != raw {
		t.Errorf("written anyway:\n%s", fileNow(t))
	}
}

// Review round 3, finding 1: --new on a name declared twice is "exists", like
// on a name declared once, not the duplicate refusal.
func TestNewOnADuplicatedNameIsExists(t *testing.T) {
	sandbox(t, "automations:\n  - {name: x, cron: \"@daily\", repo: /a, prompt: p}\n  - {name: x, cron: \"@daily\", repo: /b, prompt: p}\n")
	if _, err := Apply([]byte("name: x\ncron: \"@daily\"\nrepo: /c\nprompt: p\n"), ApplyOptions{New: true}, time.Now()); code(err) != CodeExists {
		t.Errorf("err = %v", err)
	}
}

// Review round 3, finding 2: next_runs never holds a null, and a cron that
// never comes due is refused with the reason (it would not load).
func TestNextRunsHoldOnlyRealTimes(t *testing.T) {
	sandbox(t, kept)
	_, err := Apply([]byte("name: feb30\ncron: \"0 0 30 2 *\"\nrepo: /x\nprompt: p\n"), ApplyOptions{DryRun: true}, time.Now())
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeInvalid || !strings.Contains(f.Message, "never comes due") {
		t.Errorf("never-due cron: %v", err)
	}
	res, err := Apply([]byte("name: rare\ncron: \"0 0 29 2 1\"\nrepo: /x\nprompt: p\n"), ApplyOptions{DryRun: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.NextRuns {
		if n.IsZero() {
			t.Errorf("a null among next_runs %v", res.NextRuns)
		}
	}
}
