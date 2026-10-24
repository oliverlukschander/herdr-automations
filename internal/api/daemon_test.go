package api

import (
	"errors"
	"testing"
	"time"
)

// fakeStart replaces the Herdr action for one test.
func fakeStart(t *testing.T, start func() error) *int {
	t.Helper()
	saved, savedWait := startScheduler, startWait
	t.Cleanup(func() { startScheduler, startWait = saved, savedWait })
	calls := 0
	startScheduler = func() error { calls++; return start() }
	startWait = 500 * time.Millisecond
	return &calls
}

func TestARunningSchedulerIsLeftAlone(t *testing.T) {
	sandbox(t, kept)
	holdDaemonLock(t, "4242\nrequests\n")
	calls := fakeStart(t, func() error { return nil })
	res, err := StartDaemon()
	if err != nil || res.Started || !res.Daemon.Running || res.Daemon.PID == nil || *res.Daemon.PID != 4242 || *calls != 0 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, *calls)
	}
}

func TestADeadSchedulerIsStartedThroughHerdr(t *testing.T) {
	sandbox(t, kept)
	calls := fakeStart(t, func() error {
		holdDaemonLock(t, "4343\nrequests\n") // what the action's daemon does first
		return nil
	})
	res, err := StartDaemon()
	if err != nil || !res.Started || *res.Daemon.PID != 4343 || *calls != 1 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, *calls)
	}
}

func TestAStartThatDoesNotTakeSaysSo(t *testing.T) {
	sandbox(t, kept)
	fakeStart(t, func() error { return errors.New("no server") })
	if _, err := StartDaemon(); code(err) != CodeDaemonNotRunning {
		t.Errorf("herdr refused: %v", err)
	}
	fakeStart(t, func() error { return nil }) // accepted, but nothing comes up
	if _, err := StartDaemon(); code(err) != CodeDaemonNotRunning {
		t.Errorf("nothing came up: %v", err)
	}
}
