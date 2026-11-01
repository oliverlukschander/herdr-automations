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
	"path/filepath"
	"strings"
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

// Default returns a Runner driving the real Herdr.
func Default() *Runner { return New(host.New()) }

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
		r.record(history.NewID(a.Name, ""), a.Name, trigger, history.StatusSkipped, host.Session{},
			"previous run still in flight")
		return fmt.Errorf("%s: previous run still in flight, skipped", a.Name)
	}
	defer r.inFlight.Delete(a.Name)

	id := history.NewID(a.Name, "")
	r.record(id, a.Name, trigger, history.StatusScheduled, host.Session{}, "")

	if a.CloseWhenNoAction {
		dir := filepath.Join(config.StateDir(), "reports")
		if err := os.MkdirAll(dir, 0700); err != nil {
			r.record(id, a.Name, trigger, history.StatusFailed, host.Session{}, err.Error())
			return err
		}
		dir, err := os.MkdirTemp(dir, "run-")
		if err != nil {
			r.record(id, a.Name, trigger, history.StatusFailed, host.Session{}, err.Error())
			return err
		}
		a.ReportPath = filepath.Join(dir, "result.json")
		if a.Workflow == "" {
			a.Prompt += fmt.Sprintf(`

COMPLETION REPORT (takes precedence over older end-of-run instructions in skills):
Always report whether Oliver personally needs to act. Finish the work first.
Before ending or asking Oliver a question, run:
herdr-automations report --file %s --action yes|no --summary 'short German summary; if yes, the concrete next action and relevant file/link'
Choose exactly yes or no. Use no only when all authorized work is complete and no manual review, decision, login, or unresolved failure remains. If unsure, use yes and explain.
Also print "Aktion für Oliver: JA/NEIN — <summary>". Save any detailed report to a persistent file and include its path in the summary before using no.
Do not close your tab yourself. Do not ask a question merely to keep it open. The scheduler preserves action-needed tabs and closes no-action tabs after the agent settles. This report is required even for an empty inbox or an early stop. Never put secrets in it.
`, "'"+strings.ReplaceAll(a.ReportPath, "'", "'\\''")+"'")
		}
	}
	session, err := r.host.Provision(a)
	if err != nil {
		r.record(id, a.Name, trigger, history.StatusFailed,
			host.Session{WorkspaceID: session.WorkspaceID}, err.Error())
		return err
	}
	r.record(id, a.Name, trigger, history.StatusRunning, session, "")

	timeout := time.Duration(a.TimeoutMinutes) * time.Minute
	if err := r.host.Do(session, a, timeout); err != nil {
		if rep, readErr := report.Read(a.ReportPath); a.CloseWhenNoAction && readErr == nil {
			err = fmt.Errorf("%w; %s", err, rep.Summary)
		}
		r.record(id, a.Name, trigger, statusFor(err), session, err.Error())
		return err
	}
	if a.CloseWhenNoAction {
		if err := r.complete(id, a, trigger, session); err != nil {
			r.record(id, a.Name, trigger, history.StatusFailed, session, err.Error())
			return err
		}
		return nil
	}
	r.record(id, a.Name, trigger, history.StatusDone, session, "")
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

// record is the only writer of run history, which is what makes it the only
// caller of notify: the two cannot drift apart, and a future status inherits
// its notification for free without runner filtering anything.
func (r *Runner) record(id, name string, trigger history.Trigger, st history.Status, s host.Session, errMsg string) {
	rec := history.Record{
		RunID: id, Automation: name, Trigger: trigger, Status: st, At: time.Now(),
		WorkspaceID: s.WorkspaceID, PaneID: s.PaneID, Error: errMsg,
	}
	if st == history.StatusFailed {
		required := true
		rec.UserActionRequired = &required
		rec.Summary = errMsg
	}
	err := history.Append(rec)
	if err != nil {
		log.Printf("history append failed: %v", err)
	}
	r.notifier.Outcome(name, st, errMsg)
}

// Persist the report before closing anything; unreadable reports never authorize closure.
func (r *Runner) complete(id string, a config.Automation, trigger history.Trigger, s host.Session) error {
	rep, err := report.Read(a.ReportPath)
	if err != nil {
		return fmt.Errorf("completion report missing or invalid; inspect the open tab: %w", err)
	}
	status := history.StatusDone
	if *rep.UserActionRequired {
		status = history.StatusNeedsAction
	}
	rec := history.Record{RunID: id, Automation: a.Name, Trigger: trigger, Status: status, At: time.Now(),
		WorkspaceID: s.WorkspaceID, PaneID: s.PaneID, UserActionRequired: rep.UserActionRequired, Summary: rep.Summary, ReportPath: a.ReportPath}
	if err := history.Append(rec); err != nil {
		return fmt.Errorf("cannot preserve completion report; tab kept open: %w", err)
	}
	fmt.Printf("Action required: %t — %s\n", *rep.UserActionRequired, rep.Summary)
	r.notifier.Outcome(a.Name, status, rep.Summary)
	if *rep.UserActionRequired {
		return nil
	}
	if err := r.host.CloseTab(s, a); err != nil {
		return fmt.Errorf("no work remains but the tab could not be safely closed: %w", err)
	}
	rec.TabClosed = true
	rec.At = time.Now()
	if err := history.Append(rec); err != nil {
		log.Printf("tab closed; could not record closure: %v", err)
	}
	return nil
}
