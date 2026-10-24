package daemon

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A daemon killed with the Herdr server leaves its pidfile behind, and the
// startup hook runs again on the next restart. By then the recorded PID may
// have been reused by an unrelated process, which must not be mistaken for a
// running daemon.
func TestAcquireLockIgnoresStalePIDOwnedByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	// A live process of our own that is plainly not a daemon. Signalling it
	// succeeds, so any liveness check based on the PID alone reports it as one.
	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	defer func() {
		_ = victim.Process.Kill()
		_, _ = victim.Process.Wait()
	}()

	pidfile := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(victim.Process.Pid)), 0o644); err != nil {
		t.Fatalf("seed stale pidfile: %v", err)
	}

	release, err := acquireLock()
	if err != nil {
		t.Fatalf("acquireLock refused to start over a stale pidfile: %v", err)
	}
	defer release()

	got, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); string(got) != want {
		t.Errorf("pidfile = %q, want %q", got, want)
	}
}

// The lock must still do its job: a real second daemon is a separate process,
// so the check has to hold across processes rather than within one.
func TestAcquireLockRejectsSecondDaemonProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	release, err := acquireLock()
	if err != nil {
		t.Fatalf("first acquireLock: %v", err)
	}
	defer release()

	out, err := runLockHelper(dir)
	if err != nil {
		t.Fatalf("run helper: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Errorf("second daemon was not refused; two daemons would double-fire every automation (helper said: %s)", out)
	}
}

func TestAcquireLockAfterReleaseSucceeds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	release, err := acquireLock()
	if err != nil {
		t.Fatalf("first acquireLock: %v", err)
	}
	release()

	out, err := runLockHelper(dir)
	if err != nil {
		t.Fatalf("run helper: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "ACQUIRED") {
		t.Errorf("lock was not reusable after release (helper said: %s)", out)
	}
}

// runLockHelper re-executes this test binary as a child process that tries to
// take the lock once and reports what happened.
func runLockHelper(stateDir string) (string, error) {
	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess")
	cmd.Env = append(os.Environ(), "HERDR_LOCK_HELPER=1", "HERDR_PLUGIN_STATE_DIR="+stateDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestLockHelperProcess is the child half of runLockHelper, not a test of its
// own; it does nothing unless the parent asks for it.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("HERDR_LOCK_HELPER") != "1" {
		t.Skip("helper process for the lock tests")
	}
	release, err := acquireLock()
	if err != nil {
		fmt.Println("REFUSED")
		return
	}
	defer release()
	fmt.Println("ACQUIRED")
	if os.Getenv("HERDR_LOCK_HOLD") == "1" {
		io.Copy(io.Discard, os.Stdin) // hold the lock until the parent hangs up
	}
}

// A daemon is another process, so that is what Probe has to see through.
func TestProbeSeesTheDaemonOfAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess")
	cmd.Env = append(os.Environ(), "HERDR_LOCK_HELPER=1", "HERDR_LOCK_HOLD=1", "HERDR_PLUGIN_STATE_DIR="+dir)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if !strings.Contains(line, "ACQUIRED") {
		t.Fatalf("helper did not take the lock: %q", line)
	}

	st := Probe()
	if !st.Running || st.PID != cmd.Process.Pid || time.Since(st.Since) > time.Minute {
		t.Errorf("Probe() = %+v, want running as pid %d since just now", st, cmd.Process.Pid)
	}

	stdin.Close()
	cmd.Wait()
	if st := Probe(); st.Running {
		t.Errorf("Probe() after the daemon exited = %+v", st)
	}
}

// A pidfile left by a daemon that died is not a daemon, and a machine that
// never ran one gets no pidfile from being asked.
func TestProbeTrustsTheLockNotThePidfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	if st := Probe(); st.Running {
		t.Fatalf("no pidfile: Probe() = %+v", st)
	}
	if _, err := os.Stat(pidPath()); !os.IsNotExist(err) {
		t.Fatalf("Probe created the pidfile: %v", err)
	}
	if err := os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Probe(); st.Running {
		t.Errorf("stale pidfile: Probe() = %+v", st)
	}
}

// A daemon starting while a probe holds the lock shared must wait the probe
// out, not take it for a second daemon and exit for good.
func TestAStartingDaemonWaitsOutAProbe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	f, err := os.OpenFile(pidPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, func() { releaseFile(f) })

	release, err := acquireLock()
	if err != nil {
		t.Fatalf("daemon gave up behind a probe: %v", err)
	}
	release()
}
