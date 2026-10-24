package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/schedule"
)

// ShowResult is `show <name> --json`.
type ShowResult struct {
	Head
	Config     ConfigInfo  `json:"config"`
	Automation *Automation `json:"automation"`
	Diagnostic *Diagnostic `json:"diagnostic"`
	YAML       string      `json:"yaml"`
}

// ApplyResult is `apply --json`.
type ApplyResult struct {
	Head
	DryRun     bool        `json:"dry_run"`
	Action     string      `json:"action"`
	Config     ConfigInfo  `json:"config"`
	Automation *Automation `json:"automation"`
	NextRuns   []Time      `json:"next_runs"`
	Runnable   *bool       `json:"runnable"`
	Checks     []Check     `json:"checks"`
}

// DeleteResult is `delete <name> --json`.
type DeleteResult struct {
	Head
	Deleted string     `json:"deleted"`
	Config  ConfigInfo `json:"config"`
}

// PauseResult is `pause|resume <name> --json`.
type PauseResult struct {
	Head
	Changed    bool       `json:"changed"`
	Config     ConfigInfo `json:"config"`
	Automation Automation `json:"automation"`
}

// ApplyOptions are apply's flags.
type ApplyOptions struct {
	DryRun          bool
	New             bool
	RequireRunnable bool
	RenameFrom      string
	IfMatch         string
	// Patch is a JSON object applied to the body before anything else.
	Patch string
}

// Show answers `show <name> --json`: the entry as written, loaded or not.
func Show(name string, now time.Time) (*ShowResult, error) {
	f, err := readConfig()
	if err != nil {
		return nil, err
	}
	it := f.Item(name)
	if it == nil {
		return nil, Fail(CodeNotFound, "no automation named %q", name)
	}
	res := &ShowResult{Config: configInfo(f), YAML: f.Text(*it)}
	if a := f.Find(name); a != nil && a.Line == it.Line {
		v, err := viewOf(f, *a, now)
		if err != nil {
			return nil, err
		}
		res.Automation = &v
	} else if d := f.Diagnostic(name); d != nil {
		res.Diagnostic = &Diagnostic{Name: str(d.Name), Line: d.Line, Message: d.Message}
	}
	return res, nil
}

// Delete answers `delete <name> --json`. A broken entry can be deleted too.
func Delete(name, ifMatch string) (*DeleteResult, error) {
	_, after, err := config.Change(ifMatch, false, func(f *config.File) (config.Edit, error) {
		it := f.Item(name)
		if it == nil || name == "" { // an entry without a name cannot be told apart from another
			return config.Edit{}, &config.NotFoundError{Name: name}
		}
		if err := f.Single(name); err != nil {
			return config.Edit{}, err
		}
		raw, err := f.Remove(*it)
		return config.Edit{Raw: raw, Touched: []string{name}}, err
	})
	if err != nil {
		return nil, failure(err)
	}
	return &DeleteResult{Deleted: name, Config: configInfo(after)}, nil
}

// SetPaused answers `pause|resume <name> --json`.
func SetPaused(name string, paused bool, ifMatch string, now time.Time) (*PauseResult, error) {
	changed, _, after, err := config.SetDisabled(name, paused, ifMatch)
	if err != nil {
		return nil, failure(err)
	}
	v, err := viewOf(after, *after.Find(name), now)
	if err != nil {
		return nil, err
	}
	return &PauseResult{Changed: changed, Config: configInfo(after), Automation: v}, nil
}

// Apply answers `apply --json`: create or replace one entry from the body.
func Apply(body []byte, o ApplyOptions, now time.Time) (*ApplyResult, error) {
	if o.New && o.RenameFrom != "" {
		return nil, Fail(CodeUsage, "--new and --rename-from do not go together")
	}
	b, err := parseBody(body)
	if err != nil {
		return nil, err
	}
	if o.Patch != "" {
		if err := b.patch(o.Patch); err != nil {
			return nil, err
		}
	}
	name, err := b.check()
	if err != nil {
		return nil, err
	}

	res := &ApplyResult{DryRun: o.DryRun, Checks: []Check{}, NextRuns: []Time{}}
	target := name
	if o.RenameFrom != "" {
		target = o.RenameFrom
	}
	edit := func(f *config.File) (config.Edit, error) {
		// --new asks for a name nobody has: taken once or twice, the answer
		// is the same.
		if o.New && f.Declares(name) {
			return config.Edit{}, Fail(CodeExists, "an automation named %q already exists", name)
		}
		// By name, an edit of a name declared twice would land on the first
		// of them, whichever was meant.
		if err := f.Single(target); err != nil {
			return config.Edit{}, err
		}
		it := f.Item(target)
		switch {
		case o.RenameFrom != "" && it == nil:
			return config.Edit{}, &config.NotFoundError{Name: o.RenameFrom}
		case o.New && it != nil, target != name && f.Declares(name):
			return config.Edit{}, Fail(CodeExists, "an automation named %q already exists", name)
		}

		var raw []byte
		var err error
		switch {
		case it == nil:
			res.Action = "created"
			var text string
			if text, err = b.text(); err == nil {
				raw, err = f.Append(text)
			}
		case b.json:
			res.Action = "replaced"
			raw, err = f.SetKeys(*it, b.pairs(), missing(*it, b.node))
		default:
			res.Action = "replaced"
			raw, err = f.Replace(*it, b.yaml)
		}
		if target != name {
			res.Action = "renamed"
		}
		if err == nil && bytes.Equal(raw, f.Raw) {
			res.Action = "unchanged"
		}
		return config.Edit{Raw: raw, Touched: []string{name, target}, MustLoad: []string{name},
			Entry: name, Want: b.want()}, err
	}

	// The checks run shells and ask Herdr, seconds each, so never while the
	// file is locked: the edit is worked out first, dry, the checks run on
	// the result, and a real write then happens only if the file is still the
	// one that was checked.
	before, after, err := config.Change(o.IfMatch, o.DryRun || o.RequireRunnable, edit)
	if err != nil {
		return nil, failure(err)
	}
	if o.DryRun || o.RequireRunnable {
		res.Checks = runChecks(*after.Find(name), after.Config, target, now)
		ok := runnable(res.Checks)
		res.Runnable = &ok
		if o.RequireRunnable && !ok {
			f := Fail(CodeChecksFailed, "not runnable here: %s", failing(res.Checks))
			f.Checks = res.Checks
			return nil, f
		}
		if !o.DryRun {
			if _, after, err = config.Change(config.Hash(before.Raw), false, edit); err != nil {
				return nil, failure(err)
			}
		}
	}

	a := *after.Find(name)
	v, err := viewOf(after, a, now)
	if err != nil {
		return nil, err
	}
	res.Automation = &v
	// A dry run describes the file as it is: its hash is what the real apply
	// has to send as --if-match.
	res.Config = configInfo(after)
	if o.DryRun {
		res.Config = configInfo(before)
	}
	runs, _ := schedule.Upcoming(a.Cron, now, 5)
	for _, t := range runs {
		res.NextRuns = append(res.NextRuns, Time{t})
	}
	return res, nil
}

// viewOf is the Automation view of one entry, its last run included.
func viewOf(f *config.File, a config.Automation, now time.Time) (Automation, error) {
	var last *history.Record
	if runs, err := history.Runs(a.Name, 1); err == nil && len(runs) > 0 {
		last = &runs[0]
	}
	return automationOf(f, a, last, now)
}

// failure turns what config reports into the codes the app branches on.
func failure(err error) error {
	var (
		f        *Failure
		conflict *config.ConflictError
		refused  *config.RefusedError
		notFound *config.NotFoundError
		dup      *config.DuplicateError
	)
	switch {
	case errors.As(err, &f):
		return f
	case errors.As(err, &conflict):
		f := Fail(CodeConflict, "%v", err)
		f.CurrentSHA256 = &conflict.Current
		return f
	case errors.As(err, &refused):
		f := Fail(CodeInvalid, "%s", refused.Message)
		for _, d := range refused.Diagnostics {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{Name: str(d.Name), Line: d.Line, Message: d.Message})
		}
		return f
	case errors.As(err, &notFound):
		return Fail(CodeNotFound, "%v", err)
	case errors.As(err, &dup):
		f := Fail(CodeInvalid, "%v", err)
		for _, l := range dup.Lines {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{Name: str(dup.Name), Line: l,
				Message: fmt.Sprintf("%s: declared here too", dup.Name)})
		}
		return f
	case errors.Is(err, config.ErrLocked):
		return Fail(CodeLocked, "%v", err)
	case errors.Is(err, config.ErrNotSpliceable), errors.As(err, new(*config.UnreadableError)):
		return Fail(CodeConfigUnreadable, "%v", err)
	}
	return err
}

// ── the body ─────────────────────────────────────────────────────────

// body is what apply read from stdin: one automation, as JSON (merged onto
// the entry key by key) or as YAML (written as it is).
type body struct {
	json bool
	yaml string
	node *yaml.Node // the mapping
}

// entryKeys are the keys an entry may have; anything else is a typo that the
// loader would silently ignore.
var entryKeys = []string{
	"name", "cron", "repo", "workspace", "workspace_id", "agent", "model", "prompt", "workflow",
	"mcp_config", "agent_args", "timeout_minutes", "catch_up_minutes", "close_when_no_action", "disabled",
}

func invalidBody(format string, args ...any) error {
	f := Fail(CodeInvalid, format, args...)
	f.Diagnostics = []Diagnostic{{Line: 1, Message: f.Message}}
	return f
}

func parseBody(raw []byte) (body, error) {
	text := string(raw)
	if strings.TrimSpace(text) == "" {
		return body{}, invalidBody("the body is empty; send one automation on stdin")
	}
	b := body{json: strings.HasPrefix(strings.TrimSpace(text), "{")}
	if !b.json {
		// A document marker would be laid into the list as if it were a key.
		text = strings.TrimPrefix(strings.TrimLeft(text, "\n"), "---\n")
	}
	if b.json && !json.Valid(raw) {
		return body{}, invalidBody("the body starts with { but is not valid JSON")
	}
	node, err := mapping(raw)
	if err != nil {
		return body{}, invalidBody("%v", err)
	}
	b.node = node
	if !b.json {
		b.yaml = text
	}
	return b, nil
}

// mapping reads exactly one YAML (or JSON) document holding a mapping.
func mapping(raw []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if dec.Decode(new(yaml.Node)) != io.EOF {
		return nil, fmt.Errorf("the body holds more than one document")
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the body must be one automation (a mapping), not a list or a value")
	}
	return doc.Content[0], nil
}

// patch applies --patch: listed keys set, null ones removed, the rest kept.
// A YAML body keeps its text apart from the patched keys.
func (b *body) patch(raw string) error {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") || !json.Valid([]byte(raw)) {
		return Fail(CodeUsage, "--patch takes a JSON object")
	}
	p, err := mapping([]byte(raw))
	if err != nil {
		return Fail(CodeUsage, "--patch: %v", err)
	}
	var set []config.Pair
	var del []string
	for i := 0; i+1 < len(p.Content); i += 2 {
		k, v := p.Content[i], p.Content[i+1]
		if v.Tag == "!!null" {
			del = append(del, k.Value)
		} else {
			set = append(set, config.Pair{Key: k.Value, Value: v})
		}
	}
	if b.json {
		for _, pr := range set {
			setKey(b.node, pr.Key, pr.Value)
		}
		for _, k := range del {
			deleteKey(b.node, k)
		}
		return nil
	}
	text, err := config.SetKeysInText(b.yaml, set, del)
	if err != nil {
		return invalidBody("%v", err)
	}
	b.yaml = text
	b.node, err = mapping([]byte(text))
	return err
}

// check makes sure the body names an automation and sets only keys an entry
// has, and returns its name.
func (b body) check() (string, error) {
	var diags []Diagnostic
	name := ""
	for i := 0; i+1 < len(b.node.Content); i += 2 {
		k, v := b.node.Content[i], b.node.Content[i+1]
		switch {
		case k.Value == "name" && v.Kind == yaml.ScalarNode && v.Tag == "!!str":
			name = v.Value
		case !slices.Contains(entryKeys, k.Value):
			diags = append(diags, Diagnostic{Line: k.Line, Message: fmt.Sprintf("unknown key %q", k.Value)})
		}
	}
	if name == "" {
		diags = append(diags, Diagnostic{Line: 1, Message: "the body has no name"})
	}
	if len(diags) > 0 {
		f := Fail(CodeInvalid, "%s", diags[0].Message)
		for i := range diags {
			diags[i].Name = str(name)
		}
		f.Diagnostics = diags
		return "", f
	}
	return name, nil
}

// want is the entry as it has to read back once written: the body's data. In
// a JSON body null means "not set", so those keys are not there at all.
func (b body) want() map[string]any {
	m := map[string]any{}
	if b.node.Decode(&m) != nil {
		return nil
	}
	if b.json {
		for k, v := range m {
			if v == nil {
				delete(m, k)
			}
		}
	}
	return m
}

// pairs is every key of a JSON body that has a value; null means "not set".
func (b body) pairs() []config.Pair {
	var out []config.Pair
	for i := 0; i+1 < len(b.node.Content); i += 2 {
		if v := b.node.Content[i+1]; v.Tag != "!!null" {
			out = append(out, config.Pair{Key: b.node.Content[i].Value, Value: v})
		}
	}
	return out
}

// text is the body as an entry's text: verbatim for YAML, written out for JSON.
func (b body) text() (string, error) {
	if !b.json {
		return b.yaml, nil
	}
	return config.NewEntryText(b.pairs())
}

// missing are the keys the entry has and a JSON body leaves out: a JSON body
// is the whole entry, so those go.
func missing(it config.Item, n *yaml.Node) []string {
	var e map[string]any
	if it.Decode(&e) != nil {
		return nil
	}
	var out []string
	for k := range e {
		if i := keyIndex(n, k); i < 0 || n.Content[i+1].Tag == "!!null" {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func setKey(m *yaml.Node, key string, v *yaml.Node) {
	if i := keyIndex(m, key); i >= 0 {
		m.Content[i+1] = v
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func deleteKey(m *yaml.Node, key string) {
	if i := keyIndex(m, key); i >= 0 {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
}
