// Package output keeps the last screen of each run's pane.
//
// A run's report says what it concluded. How it got there lives in two
// places: the agent's own transcript, which history can point at by session
// ID, and the pane. A workflow run has only the pane, and a run whose tab
// closes on "no action" takes even that with it. So the runner reads the pane
// back at the end of every run, before anything closes it, and keeps the text
// here. Only the newest Keep per automation survive.
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// Keep is how many captures each automation keeps.
const Keep = 20

func dir() string { return filepath.Join(config.StateDir(), "output") }

// file names a run's capture <automation hash>-<start nanos>.txt: one prefix
// per automation so pruning can find its siblings, and start time order so
// the oldest sort first. Run IDs carry automation names, which may hold
// anything, hence the hash.
func file(runID string) (path, prefix string) {
	name, nanos := runID, ""
	if i := strings.LastIndexByte(runID, '-'); i > 0 {
		if _, err := strconv.ParseInt(runID[i+1:], 10, 64); err == nil {
			name, nanos = runID[:i], runID[i+1:]
		}
	}
	sum := sha256.Sum256([]byte(name))
	prefix = hex.EncodeToString(sum[:6])
	if nanos == "" {
		sum := sha256.Sum256([]byte(runID))
		nanos = hex.EncodeToString(sum[:6])
	}
	return filepath.Join(dir(), prefix+"-"+nanos+".txt"), prefix
}

// Save keeps a run's capture, then drops its automation's oldest beyond Keep.
func Save(runID, text string) error {
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	path, prefix := file(runID)
	tmp, err := os.CreateTemp(dir(), ".capture-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	siblings, _ := filepath.Glob(filepath.Join(dir(), prefix+"-*.txt"))
	sort.Strings(siblings)
	for len(siblings) > Keep {
		os.Remove(siblings[0])
		siblings = siblings[1:]
	}
	return nil
}

// Capture is one run's kept pane.
type Capture struct {
	Path string
	At   time.Time
	Text string
}

// Read returns a run's capture, if one was kept.
func Read(runID string) (Capture, bool) {
	path, _ := file(runID)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Capture{}, false
	}
	c := Capture{Path: path, Text: string(raw)}
	if info, err := os.Stat(path); err == nil {
		c.At = info.ModTime()
	}
	return c, true
}

// Exists reports whether a capture was kept for the run.
func Exists(runID string) bool {
	path, _ := file(runID)
	_, err := os.Stat(path)
	return err == nil
}
