// Package runner executes one automation end to end and records every state
// transition in the history log. What it means to provision a workspace or get
// an agent to do something lives behind the host seam; what is left here is the
// lifecycle: skip an overlapping run, record what happened, decide whether a
// failure was a failure at all.
package runner

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/host"
)

// Runner runs automations on a host. It holds the in-flight set, which used to
// be a package global that the daemon reached into to decide whether it could
// re-exec itself.
type Runner struct {
	host     host.Host
	inFlight sync.Map
}

// New returns a Runner that works through h.
func New(h host.Host) *Runner { return &Runner{host: h} }

// Default returns a Runner driving the real Herdr.
func Default() *Runner { return New(host.New()) }

// Busy reports whether any automation is mid-run.
func (r *Runner) Busy() bool {
	busy := false
	r.inFlight.Range(func(_, _ any) bool { busy = true; return false })
	return busy
}

// Run executes the automation synchronously. trigger is "cron", "catchup" or
// "manual".
//
// Overlapping runs of the same automation are skipped rather than queued: if
// the 9:00 run is still working at 10:00, the 10:00 occurrence is dropped and
// recorded as such.
func (r *Runner) Run(a config.Automation, trigger string) error {
	if _, busy := r.inFlight.LoadOrStore(a.Name, true); busy {
		record(runID(a.Name), a.Name, trigger, history.StatusSkipped, host.Session{},
			"previous run still in flight")
		return fmt.Errorf("%s: previous run still in flight, skipped", a.Name)
	}
	defer r.inFlight.Delete(a.Name)

	id := runID(a.Name)
	record(id, a.Name, trigger, history.StatusScheduled, host.Session{}, "")

	session, err := r.host.Provision(a)
	if err != nil {
		record(id, a.Name, trigger, history.StatusFailed,
			host.Session{WorkspaceID: session.WorkspaceID}, err.Error())
		return err
	}
	record(id, a.Name, trigger, history.StatusRunning, session, "")

	timeout := time.Duration(a.TimeoutMinutes) * time.Minute
	if err := r.host.Do(session, a, timeout); err != nil {
		record(id, a.Name, trigger, statusFor(err), session, err.Error())
		return err
	}
	record(id, a.Name, trigger, history.StatusDone, session, "")
	return nil
}

// statusFor decides how a run that ended in err is remembered. Only a workspace
// closed under the run is not a failure.
func statusFor(err error) history.Status {
	if errors.Is(err, host.ErrCancelled) {
		return history.StatusCancelled
	}
	return history.StatusFailed
}

func runID(name string) string {
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}

func record(id, name, trigger string, st history.Status, s host.Session, errMsg string) {
	err := history.Append(history.Record{
		RunID: id, Automation: name, Trigger: trigger, Status: st, At: time.Now(),
		WorkspaceID: s.WorkspaceID, PaneID: s.PaneID, Error: errMsg,
	})
	if err != nil {
		log.Printf("history append failed: %v", err)
	}
}
