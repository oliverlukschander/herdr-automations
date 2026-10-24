package api

import (
	"syscall"
	"time"

	"github.com/DnzzL/herdr-automations/internal/daemon"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/request"
)

// DetachResult is `run <name> --detach --json`.
type DetachResult struct {
	Head
	RunID       string `json:"run_id"`
	Automation  string `json:"automation"`
	RequestedAt Time   `json:"requested_at"`
}

// RunDetached hands a run to the daemon and returns its ID at once. Nothing is
// queued when there is no daemon to take it (ADR 0010).
func RunDetached(name string, now time.Time) (*DetachResult, error) {
	f, err := readConfig()
	if err != nil {
		return nil, err
	}
	if f.Find(name) == nil {
		if d := f.Diagnostic(name); d != nil {
			fail := Fail(CodeInvalid, "%s did not load: %s", name, d.Message)
			fail.Diagnostics = []Diagnostic{{Name: str(d.Name), Line: d.Line, Message: d.Message}}
			return nil, fail
		}
		return nil, Fail(CodeNotFound, "no automation named %q", name)
	}
	st := daemon.Probe()
	switch {
	case !st.Running:
		return nil, Fail(CodeDaemonNotRunning, "the scheduler is not running on this machine; nothing was queued")
	case !st.TakesRequests:
		return nil, Fail(CodeDaemonNotRunning,
			"the scheduler running here predates run --detach; it restarts itself within 30 s of a plugin update, try again then")
	}

	q := request.Request{RunID: history.NewID(name, ""), Automation: name, RequestedAt: now}
	if err := request.Write(q); err != nil {
		return nil, err
	}
	// The tick takes it within 30 s anyway; the signal makes it now. Probe read
	// the PID from a pidfile whose lock was held, so it is the daemon's.
	if st.PID > 0 {
		syscall.Kill(st.PID, syscall.SIGUSR1)
	}
	return &DetachResult{RunID: q.RunID, Automation: name, RequestedAt: Time{now}}, nil
}
