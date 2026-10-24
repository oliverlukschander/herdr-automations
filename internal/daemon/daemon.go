// Package daemon is the long-running scheduler started by the plugin's startup
// hook. Every tick it re-reads automations.yaml, asks the schedule module what
// should happen now, and does it. It re-executes itself when the plugin binary
// is upgraded underneath it.
//
// Deciding is schedule's job; this package is the part with the side effects.
package daemon

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/host"
	"github.com/DnzzL/herdr-automations/internal/notify"
	"github.com/DnzzL/herdr-automations/internal/request"
	"github.com/DnzzL/herdr-automations/internal/runner"
	"github.com/DnzzL/herdr-automations/internal/schedule"
)

// tickInterval is how often the wall clock is consulted. Short enough that a
// run resumes within a minute of the machine waking up.
const tickInterval = 30 * time.Second

func Run() error {
	log.SetPrefix("[herdr-automations] ")

	// Listen before taking the lock: the lock is what tells `run --detach`
	// there is a daemon to wake, and SIGUSR1 kills a process that is not
	// listening for it.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)

	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	log.Printf("daemon starting, config=%s", config.Path())
	state := loadState()
	binary := binaryStamp()
	n := notify.New()
	runs := runner.NewWith(host.New(), n)

	tick := time.NewTicker(tickInterval)
	defer tick.Stop()

	sweepInterrupted(n)
	evaluate(state, runs, n) // don't wait a full tick to notice what is already due
	takeRequests(runs, n, time.Now())

	for {
		select {
		case <-tick.C:
			if stamp := binaryStamp(); stamp != binary && stamp != "" {
				restart(release, runs)
			}
			evaluate(state, runs, n)
			takeRequests(runs, n, time.Now())
		case <-wake:
			takeRequests(runs, n, time.Now())
		case s := <-sigs:
			log.Printf("received %v, shutting down", s)
			return nil
		}
	}
}

// takeRequests starts the runs asked for with `run --detach`, under the IDs
// the CLI already handed out. A request that waited past request.MaxWait is
// dropped and recorded missed: occurrences are dropped, never queued.
func takeRequests(runs *runner.Runner, n *notify.Notifier, now time.Time) {
	reqs, err := request.Take()
	if err != nil {
		log.Printf("reading run-now requests: %v", err)
		return
	}
	if len(reqs) == 0 {
		return
	}
	cfg, cfgErr := config.Load()
	var missed []string
	for _, q := range reqs {
		rec := history.Record{RunID: q.RunID, Automation: q.Automation, Trigger: history.TriggerManual, At: now}
		var a *config.Automation
		if cfgErr == nil {
			a = cfg.Find(q.Automation)
		}
		switch {
		case q.Stale(now):
			rec.Status = history.StatusMissed
			rec.Error = fmt.Sprintf("the run-now request waited %s for the scheduler and was dropped",
				now.Sub(q.RequestedAt).Round(time.Second))
			missed = append(missed, q.Automation)
		case a == nil:
			rec.Status = history.StatusFailed
			rec.Error = "no automation by that name loads any more"
			if cfgErr != nil {
				rec.Error = cfgErr.Error()
			} else if d := cfg.Diagnostic(q.Automation); d != nil {
				rec.Error = d.String()
			}
		default:
			log.Printf("%s: running now, as requested", q.Automation)
			go func(id string, a config.Automation) {
				if err := runs.RunAs(id, a, history.TriggerManual); err != nil {
					log.Printf("run %s: %v", a.Name, err)
				}
			}(q.RunID, *a)
			continue
		}
		if err := history.Append(rec); err != nil {
			log.Printf("history append failed: %v", err)
		}
	}
	n.Tick(missed, nil)
}

// sweepInterrupted closes the runs a dead process left open. A run whose last
// record says scheduled or running, while the process that wrote it is gone,
// will never say anything else: a CLI run whose SSH session hung up, or the
// runs of a daemon killed with the Herdr server. Run once at start; the views
// in internal/api already show such runs as interrupted in between.
func sweepInterrupted(n *notify.Notifier) {
	runs, err := history.Runs("", 0)
	if err != nil {
		log.Printf("looking for interrupted runs: %v", err)
		return
	}
	for _, r := range runs {
		if !history.Orphaned(r) {
			continue
		}
		msg := fmt.Sprintf("the process running it (pid %d) exited before it finished", r.RunnerPID)
		required := true
		r.Status, r.At, r.Error, r.Summary, r.UserActionRequired = history.StatusInterrupted, time.Now(), msg, msg, &required
		if err := history.Append(r); err != nil {
			log.Printf("history append failed: %v", err)
			continue
		}
		log.Printf("%s: %s", r.Automation, msg)
		n.Outcome(r.Automation, history.StatusInterrupted, msg)
	}
}

// evaluate asks the schedule what should happen now, and does it.
func evaluate(state *scheduleState, runs *runner.Runner, n *notify.Notifier) {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config error, leaving the schedule untouched: %v", err)
		return
	}
	dirty, invalid := reportInvalid(cfg, state)

	res := schedule.Plan(cfg, schedule.State{LastOccurrence: state.LastOccurrence}, time.Now())
	state.LastOccurrence = res.State.LastOccurrence
	dirty = dirty || res.Changed

	var missed []string
	for _, d := range res.Decisions {
		switch d.Kind {
		case schedule.Register:
			log.Printf("%s: scheduled, next run %s", d.Automation.Name, d.At.Format(time.RFC1123))
		case schedule.Missed:
			recordMissed(d.Automation.Name, d.Count, d.Reason)
			missed = append(missed, d.Automation.Name)
			log.Printf("%s: missed (%s)", d.Automation.Name, d.Reason)
		case schedule.Fire:
			if d.Trigger == history.TriggerCatchup {
				log.Printf("%s: running %s late", d.Automation.Name,
					time.Since(d.At).Round(time.Minute))
			}
			go func(a config.Automation, trigger history.Trigger) {
				if err := runs.Run(a, trigger); err != nil {
					log.Printf("run %s: %v", a.Name, err)
				}
			}(d.Automation, d.Trigger)
		}
	}
	n.Tick(missed, invalid)

	if dirty {
		if err := state.save(); err != nil {
			log.Printf("saving schedule state: %v", err)
		}
	}
}

// reportInvalid records each broken entry once, and returns the automations
// newly reported this tick so evaluate can fold them into one Tick toast.
// Re-recording an unfixed typo every 30s would bury the log and the board
// under the same line all day; going quiet about it entirely is how a
// "missing" automation stays a mystery.
func reportInvalid(cfg *config.Config, state *scheduleState) (changed bool, newlyInvalid []string) {
	if state.Invalid == nil {
		state.Invalid = map[string]bool{}
	}
	seen := map[string]bool{}
	for _, d := range cfg.Invalid {
		line := d.String()
		seen[line] = true
		if state.Invalid[line] {
			continue
		}
		state.Invalid[line] = true
		changed = true
		log.Printf("not scheduled — %s", line)
		name := d.Name
		if name == "" {
			name = "(unnamed)"
		}
		newlyInvalid = append(newlyInvalid, name)
		err := history.Append(history.Record{
			RunID:      history.NewID(name, "invalid"),
			Automation: name, Status: history.StatusInvalid,
			At: time.Now(), Error: line,
		})
		if err != nil {
			log.Printf("history append failed: %v", err)
		}
	}
	// Forget the ones that were fixed, so breaking them again is reported again.
	for line := range state.Invalid {
		if !seen[line] {
			delete(state.Invalid, line)
			changed = true
		}
	}
	return changed, newlyInvalid
}

func recordMissed(name string, count int, why string) {
	detail := why
	if count > 1 {
		detail = fmt.Sprintf("%d occurrences: %s", count, why)
	}
	err := history.Append(history.Record{
		RunID:      history.NewID(name, "missed"),
		Automation: name, Trigger: history.TriggerCron, Status: history.StatusMissed,
		At: time.Now(), Error: detail,
	})
	if err != nil {
		log.Printf("history append failed: %v", err)
	}
}

// restart re-executes the daemon so a plugin upgrade takes effect without
// waiting for the Herdr server to be restarted.
func restart(release func(), runs *runner.Runner) {
	if runs.Busy() {
		return // let the in-flight run finish; we'll notice again next tick
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("cannot locate the new binary: %v", err)
		return
	}
	log.Printf("binary changed, re-executing %s", exe)
	release() // the new process takes the lock
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("re-exec failed, continuing with the old build: %v", err)
	}
}

func binaryStamp() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	st, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", st.ModTime().UnixNano(), st.Size())
}
