// Package history persists one JSONL record per run under the plugin state
// dir. Append-only: readers reconstruct the latest state per run ID.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
)

type Status string

const (
	StatusScheduled   Status = "scheduled"
	StatusRunning     Status = "running"
	StatusDone        Status = "done"
	StatusNeedsAction Status = "needs_action"
	StatusFailed      Status = "failed"
	StatusSkipped     Status = "skipped"
	// StatusMissed records an occurrence the scheduler could not run at all —
	// typically the machine was asleep past the catch-up window. Recording it
	// is the point: a silently absent run is worse than a visible failure.
	StatusMissed Status = "missed"
	// StatusInvalid records an automation the scheduler could not even read —
	// a broken entry in automations.yaml. It is not a run that failed, it is a
	// run that was never possible, and it belongs in the log for the same
	// reason StatusMissed does: the board is where you look.
	StatusInvalid Status = "invalid"
	// StatusCancelled records a run whose workspace was closed under it.
	// Closing a run's workspace is how you call one off, so it is not a
	// failure: nothing broke, somebody decided.
	StatusCancelled Status = "cancelled"
)

// Trigger is why a run started. CONTEXT.md names three.
type Trigger string

const (
	// TriggerCron: it came due and ran on time.
	TriggerCron Trigger = "cron"
	// TriggerCatchup: it came due while the machine was unavailable and ran
	// late, inside the catch-up window.
	TriggerCatchup Trigger = "catchup"
	// TriggerManual: somebody asked for it — `r` on the board, or `run`.
	TriggerManual Trigger = "manual"
)

// NewID names a run in the log. Everything that appends here built the same
// name-nanos shape by hand, three times, with three different infixes.
func NewID(automation, kind string) string {
	if kind != "" {
		automation += "-" + kind
	}
	return fmt.Sprintf("%s-%d", automation, time.Now().UnixNano())
}

type Record struct {
	UserActionRequired *bool     `json:"user_action_required,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	ReportPath         string    `json:"report_path,omitempty"`
	TabClosed          bool      `json:"tab_closed,omitempty"`
	RunID              string    `json:"run_id"`
	Automation         string    `json:"automation"`
	Status             Status    `json:"status"`
	At                 time.Time `json:"at"`
	Trigger            Trigger   `json:"trigger,omitempty"`
	WorkspaceID        string    `json:"workspace_id,omitempty"`
	PaneID             string    `json:"pane_id,omitempty"`
	Error              string    `json:"error,omitempty"`
}

func path() string { return filepath.Join(config.StateDir(), "history.jsonl") }

// Append writes one record; failures are returned but callers generally just
// log them — history must never break a run.
func Append(r Record) error {
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(line))
	return err
}

// StartedAt is when a run began, read back from the nanoseconds NewID puts at
// the end of every ID. ok is false for an ID NewID did not make.
func StartedAt(runID string) (time.Time, bool) {
	i := strings.LastIndexByte(runID, '-')
	if i < 0 {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(runID[i+1:], 10, 64)
	if err != nil || n < 1e17 { // a plain counter, not a clock reading
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}

// lateWrite is how far out of time order a record may land in the log. Runs
// append from goroutines and from other processes, so the file is only roughly
// sorted; a backwards scan stops this far past where it could.
const lateWrite = time.Minute

// Runs returns the latest record per run, newest run first, optionally filtered
// by automation name, capped at limit (0 = no cap).
//
// It reads the log backwards, so what it costs follows limit rather than the
// size of a file nothing ever truncates (2.5 MB on a Mac after a few weeks).
// The first record met for a run is its latest; once limit runs are held, the
// scan stops at the first record older than any run that could still displace
// one of them.
func Runs(automation string, limit int) ([]Record, error) {
	latest := map[string]Record{}
	start := map[string]time.Time{}
	var ids []string
	var floor time.Time
	err := backward(func(r Record) bool {
		if automation != "" && r.Automation != automation {
			return true
		}
		if limit > 0 && len(ids) >= limit && r.At.Before(floor.Add(-lateWrite)) {
			return false
		}
		if _, seen := latest[r.RunID]; !seen {
			latest[r.RunID] = r
			ids = append(ids, r.RunID)
		}
		// An ID NewID did not make starts at its earliest record.
		if t, ok := StartedAt(r.RunID); ok {
			start[r.RunID] = t
		} else if s, ok := start[r.RunID]; !ok || r.At.Before(s) {
			start[r.RunID] = r.At
		}
		if limit > 0 && len(ids) >= limit {
			floor = newestStarts(ids, start)[limit-1]
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ids, func(i, j int) bool { return start[ids[i]].After(start[ids[j]]) })
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		out = append(out, latest[id])
	}
	return out, nil
}

// newestStarts is the runs' start times, newest first.
func newestStarts(ids []string, start map[string]time.Time) []time.Time {
	out := make([]time.Time, len(ids))
	for i, id := range ids {
		out[i] = start[id]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].After(out[j]) })
	return out
}

// Latest returns, for every automation in the log, the latest record of its
// newest run. It is one pass over the whole file, where asking per automation
// was one pass each — and the board asks every two seconds.
func Latest() (map[string]Record, error) {
	f, err := os.Open(path())
	if os.IsNotExist(err) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	latest := map[string]Record{}
	newest := map[string]string{} // automation -> its most recently begun run
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // tolerate a torn write rather than losing the file
		}
		if _, seen := latest[r.RunID]; !seen {
			newest[r.Automation] = r.RunID
		}
		latest[r.RunID] = r
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]Record, len(newest))
	for name, id := range newest {
		out[name] = latest[id]
	}
	return out, nil
}

// Timeline returns every record of one run, oldest first. It reads backwards
// and stops once the records predate the run, so looking at a recent run
// costs a recent read.
func Timeline(runID string) ([]Record, error) {
	start, known := StartedAt(runID)
	var out []Record
	err := backward(func(r Record) bool {
		if known && r.At.Before(start.Add(-lateWrite)) {
			return false
		}
		if r.RunID == runID {
			out = append(out, r)
		}
		return true
	})
	slices.Reverse(out)
	return out, err
}

// backward hands fn the log's records newest line first, until fn returns
// false. Lines that do not decode are skipped, as everywhere else.
func backward(fn func(Record) bool) error {
	f, err := os.Open(path())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}

	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	var rest []byte // the start of the line the previous chunk began inside
	emit := func(line []byte) bool {
		var r Record
		if len(line) == 0 || json.Unmarshal(line, &r) != nil {
			return true
		}
		return fn(r)
	}
	for pos := info.Size(); pos > 0; {
		n := min(int64(chunk), pos)
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil {
			return err
		}
		data := append(buf[:n:n], rest...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			if !emit(data[i+1:]) {
				return nil
			}
			data = data[:i]
		}
		rest = append([]byte(nil), data...)
	}
	emit(rest)
	return nil
}
