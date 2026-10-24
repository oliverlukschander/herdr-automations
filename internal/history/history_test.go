package history

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunsCollapsesToLatestState(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	base := time.Now()
	steps := []Record{
		{RunID: "r1", Automation: "triage", Status: StatusScheduled, At: base},
		{RunID: "r1", Automation: "triage", Status: StatusRunning, At: base.Add(time.Second)},
		{RunID: "r1", Automation: "triage", Status: StatusDone, At: base.Add(time.Minute)},
		{RunID: "r2", Automation: "other", Status: StatusFailed, At: base.Add(2 * time.Minute), Error: "boom"},
	}
	for _, r := range steps {
		if err := Append(r); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := Runs("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].RunID != "r2" || runs[0].Status != StatusFailed {
		t.Fatalf("newest first expected, got %+v", runs[0])
	}
	if runs[1].Status != StatusDone {
		t.Fatalf("r1 should collapse to done, got %s", runs[1].Status)
	}

	latest, err := Latest()
	if err != nil || latest["triage"].RunID != "r1" || latest["triage"].Status != StatusDone {
		t.Fatalf("Latest()[triage] = %+v, %v", latest["triage"], err)
	}
}

// appendAll writes records the way the runner does, failing the test on error.
func appendAll(t *testing.T, recs ...Record) {
	t.Helper()
	for _, r := range recs {
		if err := Append(r); err != nil {
			t.Fatal(err)
		}
	}
}

// idAt is a run ID as NewID makes it, for a run that began at t.
func idAt(name string, t time.Time) string { return fmt.Sprintf("%s-%d", name, t.UnixNano()) }

// A log far bigger than one read chunk, read backwards, must agree with the
// forward reading it replaced: every run once, at its latest state, newest
// run first — torn lines and a name full of punctuation included.
func TestRunsReadBackwardsAgreesWithTheWholeLog(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	base := time.Date(2026, 10, 24, 9, 0, 0, 0, time.Local)
	const runs = 600
	for i := range runs {
		name := "[KAN] Mails -> Kaneo"
		if i%3 == 0 {
			name = "[SOC] Grow X Account"
		}
		at := base.Add(time.Duration(i) * time.Minute)
		id := idAt(name, at)
		appendAll(t,
			Record{RunID: id, Automation: name, Status: StatusScheduled, At: at},
			Record{RunID: id, Automation: name, Status: StatusRunning, At: at.Add(time.Second), Summary: strings.Repeat("x", 80)},
			Record{RunID: id, Automation: name, Status: StatusDone, At: at.Add(30 * time.Second)},
		)
		if i == runs/2 {
			f, _ := os.OpenFile(filepath.Join(os.Getenv("HERDR_PLUGIN_STATE_DIR"), "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("{\"run_id\": \"torn\n")
			f.Close()
		}
	}

	all, err := Runs("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != runs {
		t.Fatalf("got %d runs, want %d", len(all), runs)
	}
	for i, r := range all {
		want := base.Add(time.Duration(runs-1-i) * time.Minute)
		if got, _ := StartedAt(r.RunID); !got.Equal(want) || r.Status != StatusDone {
			t.Fatalf("run %d: started %v status %s, want %v done", i, got, r.Status, want)
		}
	}

	kan, err := Runs("[KAN] Mails -> Kaneo", 2)
	if err != nil || len(kan) != 2 || kan[0].Automation != "[KAN] Mails -> Kaneo" {
		t.Fatalf("filtered: %+v, %v", kan, err)
	}
	if got, _ := StartedAt(kan[0].RunID); !got.Equal(base.Add((runs - 1) * time.Minute)) {
		t.Errorf("newest KAN run started %v", got)
	}
}

// A run that began earlier but finished later is not the newest run: order
// is by when runs began, as it was when the log was read front to back.
func TestRunsOrdersByStartNotByLastWrite(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	base := time.Now().Add(-time.Hour)
	long, skipped := idAt("nightly", base), idAt("nightly", base.Add(10*time.Minute))
	appendAll(t,
		Record{RunID: long, Automation: "nightly", Status: StatusRunning, At: base},
		Record{RunID: skipped, Automation: "nightly", Status: StatusSkipped, At: base.Add(10 * time.Minute)},
		Record{RunID: long, Automation: "nightly", Status: StatusDone, At: base.Add(20 * time.Minute)},
	)
	runs, err := Runs("nightly", 1)
	if err != nil || len(runs) != 1 || runs[0].RunID != skipped {
		t.Fatalf("Runs(nightly, 1) = %+v, %v; want the skipped run", runs, err)
	}
	if latest, _ := Latest(); latest["nightly"].RunID != skipped {
		t.Errorf("Latest disagrees with Runs: %+v", latest["nightly"])
	}
}

func TestTimelineIsOneRunOldestFirst(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	base := time.Now().Add(-time.Hour)
	a, b := idAt("a", base), idAt("b", base.Add(time.Second))
	appendAll(t,
		Record{RunID: a, Automation: "a", Status: StatusScheduled, At: base},
		Record{RunID: b, Automation: "b", Status: StatusScheduled, At: base.Add(time.Second)},
		Record{RunID: a, Automation: "a", Status: StatusRunning, At: base.Add(2 * time.Second)},
		Record{RunID: a, Automation: "a", Status: StatusDone, At: base.Add(3 * time.Second)},
	)
	got, err := Timeline(a)
	if err != nil {
		t.Fatal(err)
	}
	var steps []Status
	for _, r := range got {
		steps = append(steps, r.Status)
	}
	if fmt.Sprint(steps) != "[scheduled running done]" {
		t.Errorf("timeline = %v", steps)
	}
	if none, _ := Timeline(idAt("a", base.Add(time.Hour))); len(none) != 0 {
		t.Errorf("unknown run has records: %+v", none)
	}
}

func TestStartedAtReadsWhatNewIDWrote(t *testing.T) {
	id := NewID("[KAN] Mails -> Kaneo", "missed")
	if at, ok := StartedAt(id); !ok || time.Since(at) > time.Minute {
		t.Errorf("StartedAt(%q) = %v, %v", id, at, ok)
	}
	if _, ok := StartedAt("r-1"); ok {
		t.Error("a counter suffix is not a start time")
	}
}

// A run is orphaned only when it claims to be under way and the process that
// claimed it is gone.
func TestOrphanedNeedsADeadRunnerAndAnUnfinishedRun(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dead, live := gone.Process.Pid, os.Getpid()
	cases := []struct {
		r    Record
		want bool
	}{
		{Record{Status: StatusRunning, RunnerPID: dead}, true},
		{Record{Status: StatusScheduled, RunnerPID: dead}, true},
		{Record{Status: StatusRunning, RunnerPID: live}, false},
		{Record{Status: StatusDone, RunnerPID: dead}, false},
		{Record{Status: StatusRunning}, false}, // written before RunnerPID existed
	}
	for _, c := range cases {
		if got := Orphaned(c.r); got != c.want {
			t.Errorf("Orphaned(%s, pid %d) = %v", c.r.Status, c.r.RunnerPID, got)
		}
	}
}

// Review finding 12: list (Latest) and show (Runs) name the same last run
// when runs land in the log in another order than they began, as a run-now
// request's does: its ID is minted before its first record is written.
func TestLatestAndRunsAgreeWhenTheLogIsOutOfOrder(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	base := time.Now().Add(-time.Hour)
	later, earlier := idAt("a", base.Add(time.Minute)), idAt("a", base)
	appendAll(t,
		Record{RunID: later, Automation: "a", Status: StatusDone, At: base.Add(2 * time.Minute)},
		Record{RunID: earlier, Automation: "a", Status: StatusMissed, At: base.Add(3 * time.Minute)},
	)
	runs, _ := Runs("a", 1)
	latest, _ := Latest()
	if runs[0].RunID != later || latest["a"].RunID != later {
		t.Errorf("Runs says %s, Latest says %s; want %s", runs[0].RunID, latest["a"].RunID, later)
	}
}
