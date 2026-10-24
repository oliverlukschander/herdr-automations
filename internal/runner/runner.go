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
	"github.com/DnzzL/herdr-automations/internal/output"
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
	return r.RunAs(history.NewID(a.Name, ""), a, trigger)
}

// RunAs is Run under an ID chosen beforehand: a run-now request hands its ID
// back to the app at once, and the app looks the run up by it. A skipped
// request is recorded under that ID too, so the app learns it was skipped.
func (r *Runner) RunAs(id string, a config.Automation, trigger history.Trigger) error {
	t, err := r.claim(id, a, trigger)
	if err != nil {
		return err
	}
	defer r.inFlight.Delete(a.Name)
	return r.carryOut(t, a)
}

// Start is RunAs for the daemon: it returns as soon as the run is claimed and
// its first record written, and carries it out in the background. Claiming
// before returning is the point: once Start returns, the run is in Busy (a
// re-exec on upgrade waits for it) and in the history (the app can see it),
// with no moment between where it is neither.
func (r *Runner) Start(id string, a config.Automation, trigger history.Trigger) {
	t, err := r.claim(id, a, trigger)
	if err != nil {
		log.Printf("run %s: %v", a.Name, err)
		return
	}
	go func() {
		defer r.inFlight.Delete(a.Name)
		if err := r.carryOut(t, a); err != nil {
			log.Printf("run %s: %v", a.Name, err)
		}
	}()
}

// claim takes the automation's in-flight slot and records the run as
// scheduled, or records it skipped when a run of it is still going.
func (r *Runner) claim(id string, a config.Automation, trigger history.Trigger) (*attempt, error) {
	t := &attempt{id: id, a: a, trigger: trigger}
	if _, busy := r.inFlight.LoadOrStore(a.Name, true); busy {
		r.record(t, history.StatusSkipped, "previous run still in flight")
		return nil, fmt.Errorf("%s: previous run still in flight, skipped", a.Name)
	}
	r.record(t, history.StatusScheduled, "")
	return t, nil
}

// carryOut is the run after its claim: provisioning, the work, the outcome.
func (r *Runner) carryOut(t *attempt, a config.Automation) error {
	if a.CloseWhenNoAction {
		dir := filepath.Join(config.StateDir(), "reports")
		if err := os.MkdirAll(dir, 0700); err != nil {
			r.record(t, history.StatusFailed, err.Error())
			return err
		}
		dir, err := os.MkdirTemp(dir, "run-")
		if err != nil {
			r.record(t, history.StatusFailed, err.Error())
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
		t.session = host.Session{WorkspaceID: session.WorkspaceID}
		r.record(t, history.StatusFailed, err.Error())
		return err
	}
	t.session = session
	r.record(t, history.StatusRunning, "")

	timeout := time.Duration(a.TimeoutMinutes) * time.Minute
	err = r.host.Do(session, a, timeout, func(ag host.Agent) {
		known := t.agent.SessionID != ""
		t.agent = ag
		if !known {
			// Written at once, so the app can follow the transcript live.
			r.record(t, history.StatusRunning, "")
		}
	})
	r.capture(t)
	if err != nil {
		if rep, readErr := report.Read(a.ReportPath); a.CloseWhenNoAction && readErr == nil {
			err = fmt.Errorf("%w; %s", err, rep.Summary)
		}
		r.record(t, statusFor(err), err.Error())
		return err
	}
	if a.CloseWhenNoAction {
		if err := r.complete(t, a); err != nil {
			r.record(t, history.StatusFailed, err.Error())
			return err
		}
		return nil
	}
	r.record(t, history.StatusDone, "")
	return nil
}

// attempt is one run as the history sees it: every record written about it
// carries everything known about it so far.
type attempt struct {
	id      string
	a       config.Automation
	trigger history.Trigger
	session host.Session
	agent   host.Agent
}

// base is the record every status of the run starts from.
func (t *attempt) base(st history.Status) history.Record {
	rec := history.Record{
		RunID: t.id, Automation: t.a.Name, Trigger: t.trigger, Status: st, At: time.Now(),
		WorkspaceID: t.session.WorkspaceID, PaneID: t.session.PaneID, RunnerPID: os.Getpid(),
		Workflow: t.a.Workflow, SessionID: t.agent.SessionID, SessionSource: t.agent.SessionSource, CWD: t.agent.CWD,
	}
	if t.a.Workflow == "" {
		rec.Agent, rec.Model = t.a.Agent, t.a.Model
	}
	return rec
}

// capture keeps the run's last screen of output. It runs once the work is
// over and before anything closes the tab: for a workflow run it is the only
// output there is, and an agent run's tab may be about to close.
func (r *Runner) capture(t *attempt) {
	if t.session.PaneID == "" {
		return
	}
	text, err := r.host.Capture(t.session)
	if err != nil || strings.TrimSpace(text) == "" {
		return
	}
	if err := output.Save(t.id, text); err != nil {
		log.Printf("%s: keeping the pane's output: %v", t.a.Name, err)
	}
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
func (r *Runner) record(t *attempt, st history.Status, errMsg string) {
	rec := t.base(st)
	rec.Error = errMsg
	if st == history.StatusFailed {
		required := true
		rec.UserActionRequired = &required
		rec.Summary = errMsg
	}
	err := history.Append(rec)
	if err != nil {
		log.Printf("history append failed: %v", err)
	}
	r.notifier.Outcome(t.a.Name, st, errMsg)
}

// Persist the report before closing anything; unreadable reports never authorize closure.
func (r *Runner) complete(t *attempt, a config.Automation) error {
	rep, err := report.Read(a.ReportPath)
	if err != nil {
		return fmt.Errorf("completion report missing or invalid; inspect the open tab: %w", err)
	}
	status := history.StatusDone
	if *rep.UserActionRequired {
		status = history.StatusNeedsAction
	}
	rec := t.base(status)
	rec.UserActionRequired, rec.Summary, rec.ReportPath = rep.UserActionRequired, rep.Summary, a.ReportPath
	if err := history.Append(rec); err != nil {
		return fmt.Errorf("cannot preserve completion report; tab kept open: %w", err)
	}
	fmt.Printf("Action required: %t — %s\n", *rep.UserActionRequired, rep.Summary)
	r.notifier.Outcome(a.Name, status, rep.Summary)
	if *rep.UserActionRequired {
		return nil
	}
	if err := r.host.CloseTab(t.session, a); err != nil {
		return fmt.Errorf("no work remains but the tab could not be safely closed: %w", err)
	}
	rec.TabClosed = true
	rec.At = time.Now()
	if err := history.Append(rec); err != nil {
		log.Printf("tab closed; could not record closure: %v", err)
	}
	return nil
}
