package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"
	"time"
)

// Edit is one change to automations.yaml: the text the file should have
// afterwards, and the entries the change is about. Every other entry must
// come out of it exactly as it went in.
type Edit struct {
	Raw []byte
	// Touched names the entries the edit is allowed to change.
	Touched []string
	// MustLoad names the touched entries that have to load afterwards.
	MustLoad []string
	// Verify, when set, sees the result before it is written and can still
	// refuse it.
	Verify func(after *File) error
}

// ErrLocked means another writer held the file for longer than lockWait.
var ErrLocked = errors.New("another writer is changing automations.yaml; try again")

// ConflictError means the file is no longer the one the caller read.
type ConflictError struct{ Current string }

func (e *ConflictError) Error() string {
	return "automations.yaml changed since it was read"
}

// RefusedError is an edit that would leave the file worse: the entry would
// not load, or some other entry would change.
type RefusedError struct {
	Message     string
	Diagnostics []Diagnostic
}

func (e *RefusedError) Error() string { return e.Message }

// UnreadableError means automations.yaml could not be read or parsed at all,
// so there is nothing to splice into.
type UnreadableError struct{ Err error }

func (e *UnreadableError) Error() string { return e.Err.Error() }
func (e *UnreadableError) Unwrap() error { return e.Err }

// NotFoundError means no entry, loaded or not, has that name.
type NotFoundError struct{ Name string }

func (e *NotFoundError) Error() string { return fmt.Sprintf("no automation named %q", e.Name) }

// Hash is what --if-match compares: the sha256 of the file's bytes, a missing
// file hashing as empty.
func Hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Change makes one edit to automations.yaml. Under the file lock it reads the
// file afresh, refuses when it no longer hashes to ifMatch (unless that is
// empty), lets edit work out the new text, and reads that text back the way
// Load would. The edit is refused if a touched entry that must load does not,
// or if any other entry loads differently or is diagnosed differently than
// before — the last line of defence against a splice that went wrong. Unless
// dryRun, the result is then written atomically. It returns the file as it
// was and as it is (or would be).
func Change(ifMatch string, dryRun bool, edit func(f *File) (Edit, error)) (before, after *File, err error) {
	unlock, err := lockFile()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()

	before, err = Read()
	if err != nil {
		return nil, nil, &UnreadableError{Err: err}
	}
	if ifMatch != "" && ifMatch != Hash(before.Raw) {
		return nil, nil, &ConflictError{Current: Hash(before.Raw)}
	}
	e, err := edit(before)
	if err != nil {
		return nil, nil, err
	}
	after, err = parse(e.Raw)
	if err != nil {
		return nil, nil, &RefusedError{Message: fmt.Sprintf("the result would not load: %v", err)}
	}
	after.Exists = before.Exists || len(e.Raw) > 0
	if err := keeps(before, after, e); err != nil {
		return nil, nil, err
	}
	if e.Verify != nil {
		if err := e.Verify(after); err != nil {
			return nil, nil, err
		}
	}
	if dryRun || bytes.Equal(e.Raw, before.Raw) {
		return before, after, nil
	}
	return before, after, write(e.Raw)
}

// keeps checks that an edit changed only what it was about.
func keeps(before, after *File, e Edit) error {
	for _, name := range e.MustLoad {
		if after.Find(name) == nil {
			var diags []Diagnostic
			if d := after.Diagnostic(name); d != nil {
				// Counted from the entry's first line, which is the first
				// line of the text the caller sent.
				if it := after.Item(name); it != nil && it.first >= 0 {
					d.Line -= it.first
				}
				diags = append(diags, *d)
			}
			msg := name + " would not load"
			if len(diags) > 0 {
				msg = diags[0].Message
			}
			return &RefusedError{Message: msg, Diagnostics: diags}
		}
	}

	touched := func(name string) bool { return slices.Contains(e.Touched, name) }
	diagnosed := func(f *File) map[string]int {
		out := map[string]int{}
		for _, d := range f.Invalid {
			if !touched(d.Name) {
				out[d.Name+"\x00"+d.Message]++
			}
		}
		return out
	}
	if !maps.Equal(diagnosed(before), diagnosed(after)) {
		var changed []Diagnostic
		for _, d := range after.Invalid {
			if !touched(d.Name) && before.Diagnostic(d.Name) == nil {
				changed = append(changed, d)
			}
		}
		return &RefusedError{Message: "the change would alter how other entries load", Diagnostics: changed}
	}

	loaded := func(f *File) map[string]Automation {
		out := map[string]Automation{}
		for _, a := range f.Automations {
			if !touched(a.Name) {
				a.Line = 0 // entries move when one above them changes size
				out[a.Name] = a
			}
		}
		return out
	}
	if !reflect.DeepEqual(loaded(before), loaded(after)) {
		return &RefusedError{Message: "the change would alter other entries"}
	}
	return nil
}

// lockWait is how long a writer waits for another one. Writes take
// milliseconds; anything holding the lock this long is stuck.
const lockWait = 5 * time.Second

// lockFile serialises writers: the CLI, the board and the app over SSH can all
// edit at once, and each does read-modify-write. It lives in the state dir so
// a config dir kept in a dotfiles repo does not grow a lock file. Readers,
// the daemon included, never need it: the write is an atomic rename.
func lockFile() (unlock func(), err error) {
	if err := os.MkdirAll(StateDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(StateDir(), "config.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	for deadline := time.Now().Add(lockWait); ; time.Sleep(50 * time.Millisecond) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, ErrLocked
			}
			return nil, err
		}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// write replaces automations.yaml atomically: a temp file next to it, synced,
// then renamed over it. The daemon reads the file every 30 s without a lock;
// with a plain truncate-and-write it could read half of one. A symlinked file
// (dotfiles) is written through the link, and keeps its mode.
func write(raw []byte) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	target := Path()
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".automations-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		dir.Sync()
		dir.Close()
	}
	return nil
}
