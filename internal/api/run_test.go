package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/request"
)

// holdDaemonLock stands in for a daemon: it holds the pidfile's lock and
// writes what a daemon writes. PID 0 keeps the test from signalling anything.
func holdDaemonLock(t *testing.T, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(config.StateDir(), "daemon.pid"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
}

func TestRunDetachedNeedsADaemonThatTakesRequests(t *testing.T) {
	sandbox(t, kept)
	if _, err := RunDetached("other", time.Now()); code(err) != CodeDaemonNotRunning {
		t.Fatalf("no daemon: %v", err)
	}
	if _, err := RunDetached("nope", time.Now()); code(err) != CodeNotFound {
		t.Errorf("unknown name: %v", err)
	}

	holdDaemonLock(t, "0\n") // a daemon from before run --detach
	if _, err := RunDetached("other", time.Now()); code(err) != CodeDaemonNotRunning {
		t.Errorf("old daemon: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(config.StateDir(), "requests")); len(entries) != 0 {
		t.Errorf("something was queued: %v", entries)
	}
}

func TestADetachedRunIsRequestedUntilTheDaemonTakesIt(t *testing.T) {
	sandbox(t, kept)
	holdDaemonLock(t, "0\nrequests\n")
	res, err := RunDetached("other", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if q := request.Find(res.RunID); q == nil || q.Automation != "other" {
		t.Fatalf("request = %+v", q)
	}
	run, err := RunTimeline(res.RunID)
	if err != nil || run.Run.Status != "requested" || len(run.Records) != 0 {
		t.Fatalf("--run while waiting: %+v %v", run, err)
	}
}

func TestARunWhoseProcessDiedShowsAsInterrupted(t *testing.T) {
	sandbox(t, kept)
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	id := history.NewID("other", "")
	history.Append(history.Record{RunID: id, Automation: "other", Status: history.StatusRunning, At: time.Now(), RunnerPID: gone.Process.Pid})
	res, err := History("other", 1)
	if err != nil || res.Runs[0].Status != "interrupted" || res.Runs[0].Error == nil {
		t.Fatalf("history: %+v %v", res, err)
	}
}

// Review finding 2: with no daemon to take it, a request is missed once it
// has waited past request.MaxWait, not "requested" forever.
func TestAnUntakenRequestTurnsMissed(t *testing.T) {
	sandbox(t, kept)
	id := history.NewID("other", "")
	request.Write(request.Request{RunID: id, Automation: "other", RequestedAt: time.Now().Add(-request.MaxWait - time.Second)})
	res, err := RunTimeline(id)
	if err != nil || res.Run.Status != "missed" || res.Run.Error == nil {
		t.Fatalf("run = %+v, %v", res, err)
	}
}
