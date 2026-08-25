package host

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// hwfWork hands the run to herdr-workflows and waits for its verdict.
//
// A pane has no exit code to return, so the command is asked to print one
// behind a marker and the screen is read back for it. Without that, launching
// the command successfully was indistinguishable from the workflow succeeding,
// and a `workflow:` automation reported done whatever happened — including
// when hwf was not installed at all.
type hwfWork struct {
	ops   ops
	knobs knobs
	name  string
}

func (w hwfWork) do(s Session, timeout time.Duration) error {
	if err := w.ops.LookPath("hwf"); err != nil {
		return fmt.Errorf("workflow %q needs herdr-workflows: hwf is not on PATH", w.name)
	}

	marker := fmt.Sprintf("HWF-%d", time.Now().UnixNano())
	// The shell echoes the command it was given, so the marker appears twice on
	// screen: once as this literal (with %d unexpanded) and once with the real
	// status. Only the latter matches a digit, which is what exitCode looks for.
	command := fmt.Sprintf("hwf run %s; printf '\\n%s:%%d\\n' $?", shellQuote(w.name), marker)
	if err := w.ops.PaneRun(s.PaneID, "sh", "-c", command); err != nil {
		return fmt.Errorf("running workflow %s: %w", w.name, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		screen, err := w.ops.PaneRead(s.PaneID, 200)
		if err == nil {
			if code, done := exitCode(screen, marker); done {
				if code != 0 {
					return fmt.Errorf("workflow %s exited %d", w.name, code)
				}
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workflow %s did not finish within %s", w.name, timeout)
		}
		time.Sleep(w.knobs.workflowPoll)
	}
}

// exitCode finds the status the marker was printed with. The last match wins:
// a pane may hold output from an earlier run of the same automation.
func exitCode(screen, marker string) (int, bool) {
	matches := regexp.MustCompile(regexp.QuoteMeta(marker)+`:(\d+)`).FindAllStringSubmatch(screen, -1)
	if len(matches) == 0 {
		return 0, false
	}
	code, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// shellQuote makes a workflow name safe to interpolate into the sh -c string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
