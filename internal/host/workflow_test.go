package host

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func hwfWorkWith(ops ops, name string) hwfWork {
	return hwfWork{ops: ops, knobs: fast(), name: name}
}

func TestWorkflowRefusesToStartWithoutHwf(t *testing.T) {
	// The failure this exists for: launching the command successfully was
	// indistinguishable from the workflow succeeding, so a `workflow:`
	// automation reported done even with hwf not installed at all.
	ran := false
	ops := &fakeOps{
		lookPath: func(string) error { return errors.New("executable file not found in $PATH") },
		paneRun:  func(string, ...string) error { ran = true; return nil },
	}

	err := hwfWorkWith(ops, "bump").do(Session{PaneID: "p"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "hwf is not on PATH") {
		t.Fatalf("got %v, want it to name the missing binary", err)
	}
	if ran {
		t.Error("nothing should have been run in the pane")
	}
}

func TestWorkflowSucceedsOnAZeroExitPrintedBehindTheMarker(t *testing.T) {
	var command []string
	ops := &fakeOps{
		paneRun: func(_ string, c ...string) error { command = c; return nil },
	}
	// Echo back whatever marker the command asked for, with a zero status.
	ops.paneRead = func(string, int) (string, error) {
		marker := markerIn(command)
		return "work happened\n" + marker + ":0\n", nil
	}

	if err := hwfWorkWith(ops, "bump").do(Session{PaneID: "p"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(command) < 3 || command[0] != "sh" || command[1] != "-c" {
		t.Fatalf("command = %v, want it run through sh -c", command)
	}
	if !strings.Contains(command[2], "hwf run 'bump'") {
		t.Errorf("command = %q, want the workflow name quoted", command[2])
	}
}

func TestWorkflowFailsOnANonZeroExit(t *testing.T) {
	var command []string
	ops := &fakeOps{paneRun: func(_ string, c ...string) error { command = c; return nil }}
	ops.paneRead = func(string, int) (string, error) { return markerIn(command) + ":2\n", nil }

	err := hwfWorkWith(ops, "bump").do(Session{PaneID: "p"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "exited 2") {
		t.Fatalf("got %v, want the exit status reported", err)
	}
}

func TestWorkflowTimesOutWhileTheScreenStaysQuiet(t *testing.T) {
	ops := &fakeOps{paneRead: func(string, int) (string, error) { return "still working…\n", nil }}

	err := hwfWorkWith(ops, "bump").do(Session{PaneID: "p"}, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("got %v, want a timeout", err)
	}
	if ops.reads < 2 {
		t.Errorf("read the screen %d times, want it polled", ops.reads)
	}
}

func TestWorkflowKeepsPollingThroughAnUnreadableScreen(t *testing.T) {
	// A pane that cannot be read yet is not a verdict either way.
	var command []string
	ops := &fakeOps{paneRun: func(_ string, c ...string) error { command = c; return nil }}
	reads := 0
	ops.paneRead = func(string, int) (string, error) {
		reads++
		if reads < 3 {
			return "", errors.New("pane not ready")
		}
		return markerIn(command) + ":0\n", nil
	}

	if err := hwfWorkWith(ops, "bump").do(Session{PaneID: "p"}, time.Minute); err != nil {
		t.Fatalf("got %v, want it to have waited out the unreadable screen", err)
	}
}

// markerIn recovers the marker the command was built with, so a test can echo
// back the shape the real shell would print.
func markerIn(command []string) string {
	if len(command) < 3 {
		return "HWF-none"
	}
	_, rest, _ := strings.Cut(command[2], "printf '\\n")
	marker, _, _ := strings.Cut(rest, ":")
	return marker
}

func TestExitCodeIgnoresTheEchoedCommand(t *testing.T) {
	marker := "HWF-123"
	// The shell echoes the command before running it, so the literal printf
	// format is on screen alongside the real status.
	screen := "$ sh -c 'hwf run x; printf \"\\nHWF-123:%d\\n\" $?'\n" +
		"running workflow x…\n" +
		"HWF-123:0\n"
	code, done := exitCode(screen, marker)
	if !done || code != 0 {
		t.Fatalf("got (%d, %v), want (0, true)", code, done)
	}
}

func TestExitCodeWaitsWhileNothingIsPrinted(t *testing.T) {
	if _, done := exitCode("still working…\n", "HWF-123"); done {
		t.Fatal("a workflow still running must not be read as finished")
	}
}

func TestExitCodeTakesTheLastRun(t *testing.T) {
	code, done := exitCode("HWF-9:0\nsecond attempt\nHWF-9:2\n", "HWF-9")
	if !done || code != 2 {
		t.Fatalf("got (%d, %v), want (2, true)", code, done)
	}
}

func TestShellQuoteSurvivesAnApostrophe(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}
