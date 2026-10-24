// Package schedule answers one question — what should happen right now — and
// owns every walk over a cron expression the plugin makes.
//
// The answer comes back as values. Nothing here fires a run, writes history or
// touches the filesystem, which is what makes the catch-up policy testable: the
// laptop-was-asleep rules were the point of the wall-clock scheduler and had no
// test at all while they lived inline in the daemon's tick.
package schedule

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
)

// State is what the scheduler has to remember between ticks: the occurrence
// each automation was last evaluated for. Without it a restart replays this
// morning's runs and a sleep loses them.
type State struct {
	LastOccurrence map[string]time.Time
}

// Kind is what to do about an occurrence.
type Kind string

const (
	// Fire: run it.
	Fire Kind = "fire"
	// Missed: it came due and is not worth running any more. Recording it is
	// the point — a silently absent run is worse than a visible failure.
	Missed Kind = "missed"
	// Register: an automation seen for the first time. It starts counting from
	// now, so adding one at noon does not retroactively fire this morning.
	Register Kind = "register"
)

// Decision is one thing to do about one automation.
type Decision struct {
	Kind       Kind
	Automation config.Automation
	// At is the occurrence this is about; for Register, the moment it was seen.
	At time.Time
	// Trigger is set on a Fire: on time, or late but inside the window.
	Trigger history.Trigger
	// Count is how many occurrences a Missed covers.
	Count int
	// Reason explains a Missed in the words the history log will carry.
	Reason string
}

// Result is a tick's worth of decisions plus the bookkeeping to persist.
type Result struct {
	Decisions []Decision
	State     State
	// Changed reports whether State differs from what was handed in, so the
	// caller only writes the file when there is something to write.
	Changed bool
}

// Plan decides what to do about every automation in cfg as of now.
//
// It reads the wall clock rather than arming timers: on a laptop, macOS
// suspends the monotonic clock during sleep, so a timer armed for "in 15 hours"
// fires 15 hours of *awake* time later — which is how a 9am Monday run silently
// never happened.
func Plan(cfg *config.Config, state State, now time.Time) Result {
	res := Result{State: State{LastOccurrence: map[string]time.Time{}}}
	for k, v := range state.LastOccurrence {
		res.State.LastOccurrence[k] = v
	}

	live := map[string]bool{}
	for _, a := range cfg.Automations {
		live[a.Name] = true
		if a.Disabled {
			continue
		}
		sched, err := config.ParseCron(a.Cron)
		if err != nil {
			// Load diagnoses this, so reaching here means the entry bypassed
			// it. Skipping beats guessing at a schedule.
			continue
		}

		last, seen := res.State.LastOccurrence[a.Name]
		if !seen {
			res.State.LastOccurrence[a.Name] = now
			res.Changed = true
			res.Decisions = append(res.Decisions, Decision{
				Kind: Register, Automation: a, At: next(sched, now),
			})
			continue
		}

		occ, skipped, ok := due(sched, last, now)
		if !ok {
			continue
		}
		res.State.LastOccurrence[a.Name] = occ
		res.Changed = true

		if skipped > 0 {
			res.Decisions = append(res.Decisions, Decision{
				Kind: Missed, Automation: a, At: occ,
				Count: skipped, Reason: "machine unavailable",
			})
		}

		lateness := now.Sub(occ)
		if lateness > a.CatchUp() {
			window := fmt.Sprintf("past the %s catch-up window", a.CatchUp())
			if a.CatchUp() == 0 {
				window = "catch-up disabled"
			}
			res.Decisions = append(res.Decisions, Decision{
				Kind: Missed, Automation: a, At: occ, Count: 1,
				Reason: fmt.Sprintf("due %s ago, %s", lateness.Round(time.Minute), window),
			})
			continue
		}

		trigger := history.TriggerCron
		if lateness > time.Minute {
			trigger = history.TriggerCatchup
		}
		res.Decisions = append(res.Decisions, Decision{
			Kind: Fire, Automation: a, At: occ, Trigger: trigger,
		})
	}

	// Forget automations that are gone, so re-adding one later starts clean
	// rather than firing for every occurrence since it was deleted.
	for name := range res.State.LastOccurrence {
		if !live[name] {
			delete(res.State.LastOccurrence, name)
			res.Changed = true
		}
	}
	return res
}

// due reports the most recent occurrence at or before now, how many earlier
// occurrences were skipped along the way, and whether anything is due at all.
func due(sched cron.Schedule, last, now time.Time) (occ time.Time, skipped int, ok bool) {
	first := next(sched, last)
	if first.IsZero() || first.After(now) {
		return time.Time{}, 0, false
	}
	occ = first
	for {
		n := next(sched, occ)
		if n.IsZero() || n.After(now) {
			break
		}
		skipped++
		occ = n
	}
	return occ, skipped, true
}

// next is when sched comes due after t. robfig/cron walks wall-clock fields
// in the schedule's zone, so on the night the clocks go back, 02:30 matches
// twice, an hour apart, and a daily 02:30 run fired twice (Vienna, 25 Oct).
// Like classic cron, a schedule with a fixed hour comes due once: the second
// pass through a wall-clock time it already had is skipped. One with a * in
// the hour field (every hour, every 30 minutes) keeps running by real time.
func next(sched cron.Schedule, t time.Time) time.Time {
	n := sched.Next(t)
	for !n.IsZero() && repeated(sched, n) {
		n = sched.Next(n)
	}
	return n
}

// starBit is how robfig/cron marks a field written with * (or */n).
const starBit = 1 << 63

// repeated reports whether n is a fixed-hour occurrence whose wall-clock time
// already came due within the last three hours: the autumn clock change.
func repeated(sched cron.Schedule, n time.Time) bool {
	spec, ok := sched.(*cron.SpecSchedule)
	if !ok || spec.Hour&starBit != 0 || spec.Location == nil {
		return false
	}
	at := n.In(spec.Location)
	earlier := n.Add(-3 * time.Hour)
	if _, before := earlier.In(spec.Location).Zone(); before == offset(at) {
		return false // no clock change in between: nothing can repeat
	}
	for t := sched.Next(earlier); !t.IsZero() && t.Before(n); t = sched.Next(t) {
		if t.In(spec.Location).Format(time.DateTime) == at.Format(time.DateTime) {
			return true
		}
	}
	return false
}

func offset(t time.Time) int {
	_, off := t.Zone()
	return off
}

// NextRun is when this automation comes due next, for the board and the list.
// The second return is false when the cron cannot be parsed or never comes
// due.
func NextRun(a config.Automation, from time.Time) (time.Time, bool) {
	runs, err := Upcoming(a.Cron, from, 1)
	if err != nil || len(runs) == 0 {
		return time.Time{}, false
	}
	return runs[0], true
}

// Upcoming is the next n times expr comes due after from, or fewer: robfig
// answers a zero time once it finds no date within five years ("0 0 30 2 *"
// never comes), and a zero time is not a run.
func Upcoming(expr string, from time.Time, n int) ([]time.Time, error) {
	sched, err := config.ParseCron(expr)
	if err != nil {
		return nil, err
	}
	out := []time.Time{}
	for t := from; len(out) < n; {
		if t = next(sched, t); t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out, nil
}

// Collision is a moment when more than one automation comes due. Herdr is a
// multi-agent runtime and the scheduler honours the cron literally: every one
// of them starts. Nothing here serialises anything — the point is to show the
// overlap while the cron is still yours to change.
type Collision struct {
	At    time.Time
	Names []string
}

// horizon is how far ahead overlaps are looked for: far enough to catch weekly
// schedules, near enough that the answer still means something.
const horizon = 7 * 24 * time.Hour

// maxOccurrences bounds the walk so a per-minute cron cannot spin the report.
const maxOccurrences = 2000

// Collisions reports overlapping occurrences over the next week, one entry per
// distinct set of automations rather than one per occurrence: two @daily
// entries that clash are a single fact, not seven.
func Collisions(cfg *config.Config, from time.Time) []Collision {
	byMoment := map[int64][]string{}
	for _, a := range cfg.Automations {
		if a.Disabled {
			continue
		}
		for _, t := range occurrences(a.Cron, from) {
			byMoment[t.Unix()] = append(byMoment[t.Unix()], a.Name)
		}
	}

	var out []Collision
	for unix, names := range byMoment {
		if len(names) < 2 {
			continue
		}
		out = append(out, Collision{At: time.Unix(unix, 0), Names: names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })

	seen := map[string]bool{}
	deduped := out[:0]
	for _, col := range out {
		key := strings.Join(col.Names, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, col)
	}
	return deduped
}

// CollidesWith names the enabled automations that would share an occurrence
// with cronExpr in the next week. The wizard asks before writing, so the
// warning lands while the schedule is still being decided.
func CollidesWith(cfg *config.Config, cronExpr string, from time.Time) []string {
	moments := map[int64]bool{}
	for _, t := range occurrences(cronExpr, from) {
		moments[t.Unix()] = true
	}
	var names []string
	for _, a := range cfg.Automations {
		if a.Disabled {
			continue
		}
		for _, t := range occurrences(a.Cron, from) {
			if moments[t.Unix()] {
				names = append(names, a.Name)
				break
			}
		}
	}
	return names
}

// occurrences walks a cron expression over the horizon. An invalid expression
// yields nothing: diagnosing it is Load's job, not this report's.
func occurrences(expr string, from time.Time) []time.Time {
	sched, err := config.ParseCron(expr)
	if err != nil {
		return nil
	}
	deadline := from.Add(horizon)
	var out []time.Time
	for t := next(sched, from); !t.After(deadline) && len(out) < maxOccurrences; t = next(sched, t) {
		out = append(out, t)
	}
	return out
}
