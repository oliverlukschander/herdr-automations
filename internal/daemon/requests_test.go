package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/host"
	"github.com/DnzzL/herdr-automations/internal/notify"
	"github.com/DnzzL/herdr-automations/internal/request"
	"github.com/DnzzL/herdr-automations/internal/runner"
)

// quietHost is a machine where every run works at once.
type quietHost struct{}

func (quietHost) Provision(config.Automation) (host.Session, error) {
	return host.Session{WorkspaceID: "w1", PaneID: "w1:p1"}, nil
}
func (quietHost) Do(host.Session, config.Automation, time.Duration) error { return nil }
func (quietHost) CloseTab(host.Session, config.Automation) error          { return nil }

func sandboxWith(t *testing.T, yaml string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(cfg, "automations.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// settled waits for a run to reach a status no runner moves it on from.
func settled(t *testing.T, id string) []history.Record {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		recs, err := history.Timeline(id)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(recs); n > 0 && recs[n-1].Status != history.StatusScheduled && recs[n-1].Status != history.StatusRunning {
			return recs
		}
	}
	t.Fatalf("run %s never finished", id)
	return nil
}

const oneAutomation = "automations:\n  - name: \"[KAN] Mails -> Kaneo\"\n    cron: \"@yearly\"\n    repo: /x\n    prompt: p\n    disabled: true\n"

// The run the app asked for is run, paused or not, under the ID the app was
// given, and the request is gone so it cannot run twice.
func TestARequestRunsUnderTheIDTheAppWasGiven(t *testing.T) {
	sandboxWith(t, oneAutomation)
	id := history.NewID("[KAN] Mails -> Kaneo", "")
	if err := request.Write(request.Request{RunID: id, Automation: "[KAN] Mails -> Kaneo", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	takeRequests(runner.New(quietHost{}), notify.With(&fakeSink{}), time.Now())

	recs := settled(t, id)
	last := recs[len(recs)-1]
	if last.Status != history.StatusDone || last.Trigger != history.TriggerManual || last.RunnerPID != os.Getpid() {
		t.Errorf("last record = %+v", last)
	}
	if request.Find(id) != nil {
		t.Error("the request is still waiting")
	}
}

// A request nobody took for longer than MaxWait is dropped and says so, under
// its own ID; one for an automation that no longer loads fails under it.
func TestAStaleOrOrphanRequestIsRecordedNotRun(t *testing.T) {
	sandboxWith(t, oneAutomation)
	now := time.Now()
	stale := history.NewID("[KAN] Mails -> Kaneo", "")
	gone := history.NewID("deleted since", "")
	request.Write(request.Request{RunID: stale, Automation: "[KAN] Mails -> Kaneo", RequestedAt: now.Add(-3 * time.Minute)})
	request.Write(request.Request{RunID: gone, Automation: "deleted since", RequestedAt: now})

	sink := &fakeSink{}
	takeRequests(runner.New(quietHost{}), notify.With(sink), now)

	if recs, _ := history.Timeline(stale); len(recs) != 1 || recs[0].Status != history.StatusMissed {
		t.Errorf("stale request: %+v", recs)
	}
	if recs, _ := history.Timeline(gone); len(recs) != 1 || recs[0].Status != history.StatusFailed {
		t.Errorf("request for a deleted automation: %+v", recs)
	}
	if sink.toasts != 1 {
		t.Errorf("toasts = %d, want one for the missed request", sink.toasts)
	}
}

// A run left "running" by a process that is gone is closed as interrupted,
// once, with a toast. Runs whose process lives, and runs from before
// runner_pid existed, are left alone.
func TestTheSweepClosesRunsWhoseProcessDied(t *testing.T) {
	sandboxWith(t, oneAutomation)
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dead, alive, legacy := history.NewID("a", ""), history.NewID("b", ""), history.NewID("c", "")
	for _, r := range []history.Record{
		{RunID: dead, Automation: "a", Status: history.StatusRunning, RunnerPID: gone.Process.Pid},
		{RunID: alive, Automation: "b", Status: history.StatusRunning, RunnerPID: os.Getpid()},
		{RunID: legacy, Automation: "c", Status: history.StatusRunning},
	} {
		r.At = time.Now()
		history.Append(r)
	}

	sink := &fakeSink{}
	sweepInterrupted(notify.With(sink))
	sweepInterrupted(notify.With(sink))

	if recs, _ := history.Timeline(dead); len(recs) != 2 || recs[1].Status != history.StatusInterrupted ||
		recs[1].UserActionRequired == nil || !*recs[1].UserActionRequired {
		t.Errorf("dead runner: %+v", recs)
	}
	for _, id := range []string{alive, legacy} {
		if recs, _ := history.Timeline(id); len(recs) != 1 {
			t.Errorf("%s was touched: %+v", id, recs)
		}
	}
	if sink.toasts != 1 {
		t.Errorf("toasts = %d, want exactly one", sink.toasts)
	}
}
