package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// acquireLock keeps a single scheduler alive per machine. Two daemons would
// fire every automation twice.
//
// The lock is an advisory flock on the pidfile, not the PID it contains. The
// kernel drops an flock when the holder exits, however it exits, so a daemon
// killed with the Herdr server cannot leave a lock behind. Trusting the PID
// instead would strand the scheduler: the startup hook runs again on every
// server restart, and by then the recorded PID may belong to an unrelated
// process, which looks exactly like a running daemon. The PID is still written,
// for humans reading the file and for the error below, but never decides
// whether a daemon is live.
func acquireLock() (release func(), err error) {
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		return nil, err
	}
	path := pidPath()

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	// Probe takes the lock shared for a moment. A daemon that started in that
	// moment and gave up at once would take the probe for a second daemon, and
	// nothing runs the startup hook again — so it asks for a while first.
	for deadline := time.Now().Add(lockWait); ; time.Sleep(lockPoll) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another daemon is already running (pid %s)", holderPID(path))
		}
	}

	if err := f.Truncate(0); err != nil {
		releaseFile(f)
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		releaseFile(f)
		return nil, err
	}

	return func() {
		os.Remove(path)
		releaseFile(f)
	}, nil
}

// lockWait is how long a starting daemon keeps asking for a lock somebody
// holds; far longer than a Probe holds it, far shorter than any real daemon.
const (
	lockWait = 2 * time.Second
	lockPoll = 50 * time.Millisecond
)

func pidPath() string { return filepath.Join(config.StateDir(), "daemon.pid") }

// Status is what Probe found out about the scheduler on this machine.
type Status struct {
	Running bool
	// PID is what the pidfile says, when a daemon holds it.
	PID int
	// Since is when that daemon took the lock: the pidfile is written then.
	Since time.Time
}

// Probe reports whether a daemon holds the lock, for `list`. It asks the kernel
// the same question acquireLock does — the pidfile alone proves nothing (see
// above) — by trying for the lock shared and letting go at once. It never
// creates the pidfile: no file means no daemon has ever run here.
func Probe() Status {
	path := pidPath()
	f, err := os.Open(path)
	if err != nil {
		return Status{}
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return Status{}
	} else if !errors.Is(err, syscall.EWOULDBLOCK) {
		return Status{}
	}
	st := Status{Running: true}
	st.PID, _ = strconv.Atoi(holderPID(path))
	if info, err := f.Stat(); err == nil {
		st.Since = info.ModTime()
	}
	return st
}

// releaseFile drops the flock. Closing the descriptor would be enough, but
// unlocking first keeps the intent obvious at the call sites.
func releaseFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}

// holderPID reports what the pidfile claims, for the error message only. The
// file may be empty if the holder has not written its PID yet.
func holderPID(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	if pid := strings.TrimSpace(string(raw)); pid != "" {
		return pid
	}
	return "unknown"
}
