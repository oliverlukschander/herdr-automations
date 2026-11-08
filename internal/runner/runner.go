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
	"os"
	"sync"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/host"
	"github.com/DnzzL/herdr-automations/internal/notify"
	"github.com/DnzzL/herdr-automations/internal/report"
)

// Runner runs automations on a host. It holds the in-flight set, which used to
// be a package global that the daemon reached into to decide whether it could
// re-exec itself.
type Runner struct {
	host     host.Host
	notifier *notify.Notifier
	inFlight sync.Map
}

// New returns a Runner that works through h. Its notifier is nil, so it stays
// silent — the board and the CLI's `run` build one this way on purpose (see
// runner.record).
func New(h host.Host) *Runner { return &Runner{host: h} }

// NewWith returns a Runner that also toasts through n.
func NewWith(h host.Host, n *notify.Notifier) *Runner { return &Runner{host: h, notifier: n} }

// Default returns a Runner driving the backend automations.yaml names.
func Default() *Runner {
	b := config.BackendHerdr
	if cfg, err := config.Load(); err == nil {
		b = cfg.Backend
	}
	return New(host.For(b))
}

// Busy reports whether any automation is mid-run.
func (r *Runner) Busy() bool {
	busy := false
	r.inFlight.Range(func(_, _ any) bool { busy = true; return false })
	return busy
}

// Run executes the automation synchronously.
//
// Overlapping runs of the same automation are skipped rather than queued: if
// the 9:00 run is still working at 10:00, the 10:00 occurrence is dropped and
// recorded as such.
func (r *Runner) Run(a config.Automation, trigger history.Trigger) error {
	if _, busy := r.inFlight.LoadOrStore(a.Name, true); busy {
		r.record(history.NewID(a.Name, ""), a, trigger, history.StatusSkipped, host.Session{},
			"previous run still in flight")
		return fmt.Errorf("%s: previous run still in flight, skipped", a.Name)
	}
	defer r.inFlight.Delete(a.Name)

	id := history.NewID(a.Name, "")
	r.record(id, a, trigger, history.StatusScheduled, host.Session{}, "")

	if a.CloseWhenNoAction {
		var err error
		if a.ReportPath, err = report.NewPath(); err != nil {
			r.record(id, a, trigger, history.StatusFailed, host.Session{}, err.Error())
			return err
		}
		if a.Prompt != "" {
			a.Prompt += report.Ask(a.ReportPath)
		}
	}
	session, err := r.host.Provision(a)
	if err != nil {
		removeReport(a)
		r.record(id, a, trigger, history.StatusFailed,
			host.Session{WorkspaceID: session.WorkspaceID, HerdrSession: session.HerdrSession}, err.Error())
		return err
	}
	r.record(id, a, trigger, history.StatusRunning, session, "")
	return r.finish(id, a, trigger, session)
}

// Follow waits on the run again in the terminal it goes on in: its agent
// stopped (host.ErrStopped) and was started again there, as Claude Code is
// as another Claude account after a usage limit. The run is running again,
// back in the inbox, and ends as Run ends one, reading the report its prompt
// named. run is over: one still running has its own waiter.
func (r *Runner) Follow(a config.Automation, run history.Record, terminal string) error {
	if err := history.MarkUnseen(run.RunID); err != nil {
		return err
	}
	a.ReportPath = run.Report
	s := host.Session{HerdrSession: run.Session, WorkspaceID: run.WorkspaceID, PaneID: terminal}
	r.record(run.RunID, a, run.Trigger, history.StatusRunning, s, "")
	return r.finish(run.RunID, a, run.Trigger, s)
}

// finish waits for the work in s and records how it ended. The report goes
// with the run, unless its agent only stopped: then Follow may read it yet.
func (r *Runner) finish(id string, a config.Automation, trigger history.Trigger, s host.Session) error {
	timeout := time.Duration(a.TimeoutMinutes) * time.Minute
	if err := r.host.Do(s, a, timeout); err != nil {
		// A run that failed may still have said why, as a command does when
		// its pane is gone with its output.
		if rep, rerr := report.Read(a.ReportPath); rerr == nil {
			err = fmt.Errorf("%w; %s", err, rep.Summary)
		}
		if !errors.Is(err, host.ErrStopped) {
			removeReport(a)
		}
		r.record(id, a, trigger, statusFor(err), s, err.Error())
		return err
	}
	defer removeReport(a)
	if a.CloseWhenNoAction {
		return r.complete(id, a, trigger, s)
	}
	r.record(id, a, trigger, history.StatusDone, s, "")
	return nil
}

func removeReport(a config.Automation) {
	if a.ReportPath != "" {
		_ = os.Remove(a.ReportPath)
	}
}

// complete records a finished run's report and, when it says nobody needs to
// act, ends the run's terminal and marks the run seen, so it stays out of the
// inbox. The verdict is on record before anything closes. A run without a
// readable report failed at what it was asked, and stays open to be looked at.
func (r *Runner) complete(id string, a config.Automation, trigger history.Trigger, s host.Session) error {
	rep, err := report.Read(a.ReportPath)
	if err != nil {
		err = fmt.Errorf("the run wrote no report (%v); look at its terminal", err)
		r.record(id, a, trigger, history.StatusFailed, s, err.Error())
		return err
	}
	rec := history.Record{
		RunID: id, Automation: a.Name, Trigger: trigger, Status: history.StatusDone, At: time.Now(),
		WorkspaceID: s.WorkspaceID, PaneID: s.PaneID, Action: rep.Action, Summary: rep.Summary,
	}
	if err := r.write(rec); err != nil {
		return fmt.Errorf("could not record the report, so the run stays open: %w", err)
	}
	if *rep.Action {
		return nil
	}
	c, ok := r.host.(host.Closer)
	if !ok {
		return nil
	}
	if err := c.Close(s); err != nil {
		log.Printf("%s: no action needed, but its terminal stays open: %v", a.Name, err)
		return nil
	}
	if err := history.MarkSeen(id); err != nil {
		log.Printf("%s: marking the closed run seen: %v", a.Name, err)
	}
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

// record writes one state of a run of a.
func (r *Runner) record(id string, a config.Automation, trigger history.Trigger, st history.Status, s host.Session,
	errMsg string) {
	_ = r.write(history.Record{
		RunID: id, Automation: a.Name, Trigger: trigger, Status: st, At: time.Now(),
		Session: s.HerdrSession, WorkspaceID: s.WorkspaceID, PaneID: s.PaneID, Error: errMsg, Report: a.ReportPath,
	})
}

// write is the only writer of run history, which is what makes it the only
// caller of notify: the two cannot drift apart, and a future status inherits
// its notification for free without runner filtering anything.
func (r *Runner) write(rec history.Record) error {
	err := history.Append(rec)
	if err != nil {
		log.Printf("history append failed: %v", err)
	}
	r.notifier.Outcome(rec.Automation, rec.Status, rec.Error)
	return err
}
