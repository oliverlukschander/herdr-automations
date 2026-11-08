// Package changes keeps what each put and rm did to automations.yaml: the
// entry as it was written before and after, with when, one JSON line per
// change in the state dir. An automation's earlier versions can be listed and
// put back from it; nothing else reads it, and a write never fails for it.
package changes

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/DnzzL/herdr-automations/internal/api"
	"github.com/DnzzL/herdr-automations/internal/config"
)

// Change is one put or rm. Before is nil where the put made the automation,
// After where the rm deleted it.
type Change struct {
	At     time.Time  `json:"at"`
	Op     string     `json:"op"`
	Before *api.Entry `json:"before"`
	After  *api.Entry `json:"after"`
	// Fields are the keys whose values differ, the defaults filled in on both
	// sides, so an entry saved as the form writes it shows only what changed.
	// Filled in by Of, never stored.
	Fields []string `json:"fields"`
}

func path() string { return filepath.Join(config.StateDir(), "changes.jsonl") }

// Record appends one change: op is put or rm, before and after as written.
func Record(op string, before, after *config.Automation) error {
	c := Change{At: time.Now(), Op: op, Before: entry(before), After: entry(after)}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, string(line))
	return err
}

// Of lists the changes of the automation named name, newest first, with those
// made under the names it had before a rename.
func Of(name string) ([]Change, error) {
	all, err := read()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{name: true}
	out := []Change{}
	for i := len(all) - 1; i >= 0; i-- {
		c := all[i]
		if !touches(c.Before, names) && !touches(c.After, names) {
			continue
		}
		for _, e := range []*api.Entry{c.Before, c.After} {
			if e != nil {
				names[e.Name] = true
			}
		}
		c.Fields = differ(c.Before, c.After)
		out = append(out, c)
	}
	return out, nil
}

func read() ([]Change, error) {
	f, err := os.Open(path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []Change
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var c Change
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			all = append(all, c)
		}
	}
	return all, sc.Err()
}

func entry(a *config.Automation) *api.Entry {
	if a == nil {
		return nil
	}
	e := api.EntryOf(*a)
	return &e
}

// touches says whether e is there and has one of the names.
func touches(e *api.Entry, names map[string]bool) bool { return e != nil && names[e.Name] }

// differ names the keys whose values differ once both sides are normalized;
// none where either side is missing.
func differ(before, after *api.Entry) []string {
	if before == nil || after == nil {
		return nil
	}
	b, a := fields(*before), fields(*after)
	out := []string{}
	for key, v := range b {
		if !reflect.DeepEqual(v, a[key]) {
			out = append(out, key)
		}
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// fields is e normalized, as a map of its JSON keys.
func fields(e api.Entry) map[string]any {
	raw, _ := json.Marshal(api.EntryOf(config.Normalize(e.Config())))
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}
