// Package request carries a run-now from the CLI to the daemon.
//
// `run <name>` used to do the run itself, in the calling process, for as long
// as the agent took. Asked for over SSH, that process dies with the session
// and leaves its run "running" forever. It also runs in the SSH session's
// environment, not Herdr's, with an in-flight set of its own that cannot see
// the daemon's. So `run --detach` writes a request into the state dir and
// wakes the daemon, which runs it like any other: one in-flight set, toasts,
// Herdr's environment. See ADR 0010.
package request

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// MaxWait is how long a request may wait for the daemon before it is dropped
// and recorded as missed. Occurrences are dropped, never queued (ADR 0006);
// a run-now nobody picked up for minutes is not one to start by surprise.
const MaxWait = 2 * time.Minute

// Request is one run-now waiting for the daemon.
type Request struct {
	RunID       string    `json:"run_id"`
	Automation  string    `json:"automation"`
	RequestedAt time.Time `json:"requested_at"`
}

// Stale reports whether the request waited past MaxWait.
func (r Request) Stale(now time.Time) bool { return now.Sub(r.RequestedAt) > MaxWait }

func dir() string { return filepath.Join(config.StateDir(), "requests") }

// fileFor names a request's file. Run IDs carry automation names, which may
// hold anything; a hash of the ID is a file name that never needs escaping.
func fileFor(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(dir(), hex.EncodeToString(sum[:8])+".json")
}

// Write files a request atomically, so the daemon never reads half of one.
func Write(r Request) error {
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir(), ".request-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), fileFor(r.RunID))
}

// Take removes and returns every waiting request, oldest first. It removes
// each file before handing it over: a daemon that dies mid-run must not start
// the same run again when it comes back.
func Take() ([]Request, error) {
	all, err := read()
	if err != nil {
		return nil, err
	}
	var out []Request
	for _, r := range all {
		if err := os.Remove(fileFor(r.RunID)); err != nil {
			continue // not ours to run if we cannot claim it
		}
		out = append(out, r)
	}
	return out, nil
}

// Find returns the waiting request with this run ID, or nil.
func Find(runID string) *Request {
	raw, err := os.ReadFile(fileFor(runID))
	if err != nil {
		return nil
	}
	var r Request
	if json.Unmarshal(raw, &r) != nil || r.RunID != runID {
		return nil
	}
	return &r
}

func read() ([]Request, error) {
	entries, err := os.ReadDir(dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Request
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".json") {
			continue // a request still being written, or not ours
		}
		raw, err := os.ReadFile(filepath.Join(dir(), e.Name()))
		if err != nil {
			continue
		}
		var r Request
		if json.Unmarshal(raw, &r) != nil || r.RunID == "" {
			os.Remove(filepath.Join(dir(), e.Name())) // unreadable: it would never run
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out, nil
}
