// Package config loads and validates automations.yaml, the single source of
// truth for the plugin. Everything else (wizard, pane, daemon) reads or
// rewrites this file.
//
// Loading is deliberately partial: an entry that does not validate becomes a
// Diagnostic and the rest of the file still runs. Returning one error for the
// whole file meant a typo in the seventh automation stopped the other six, and
// the daemon's only response was a log line nobody reads at 09:00. For a tool
// whose promise is "it ran while you slept", a silent absence is the worst
// failure mode there is.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"github.com/DnzzL/herdr-automations/internal/hostpath"
)

// cronParser accepts the standard 5-field crontab syntax plus descriptors
// like @daily. Shared by the daemon and the wizard preview.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ParseCron parses a cron expression, and never panics doing it. robfig/cron
// v3 slices past the end of "CRON_TZ=Europe/Vienna" with nothing after the
// zone: a typo in one automation took the whole daemon down, every 30 s,
// and `apply` along with it.
func ParseCron(expr string) (s cron.Schedule, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = nil, fmt.Errorf("cannot parse it (%v)", r)
		}
	}()
	return cronParser.Parse(expr)
}

type Workspace string

const (
	WorkspaceWorktree Workspace = "worktree" // fresh git worktree per run
	WorkspaceExisting Workspace = "existing" // fresh tab in an existing workspace
	WorkspaceRoot     Workspace = "root"     // workspace on the repo root
)

// Automation is one scheduled entry: a prompt (or delegated workflow) fired
// on a cron schedule against an agent in a provisioned workspace.
type Automation struct {
	// CloseWhenNoAction requires a completion report and closes only this run's tab.
	CloseWhenNoAction bool   `yaml:"close_when_no_action,omitempty"`
	ReportPath        string `yaml:"-"`
	Name              string `yaml:"name"`
	Cron              string `yaml:"cron"`
	// Repo the automation operates on (absolute path, ~ expanded).
	Repo string `yaml:"repo"`
	// Workspace provisioning mode; defaults to worktree.
	Workspace Workspace `yaml:"workspace,omitempty"`
	// WorkspaceID is required for existing mode. Each run gets a fresh tab at Repo.
	WorkspaceID string `yaml:"workspace_id,omitempty"`
	// Agent kind as understood by `herdr agent start --kind`; defaults to claude.
	Agent string `yaml:"agent,omitempty"`
	// Model handed to the agent executable as --model. Empty leaves the agent
	// on its own default, which may not be the one your quota can afford.
	// The value is passed through untouched: model names move faster than any
	// list we could keep here, so a typo surfaces when the agent refuses it.
	Model string `yaml:"model,omitempty"`
	// Prompt submitted to the agent. Mutually exclusive with Workflow.
	Prompt string `yaml:"prompt,omitempty"`
	// Workflow delegates execution to herdr-workflows: `hwf run <name>`.
	Workflow string `yaml:"workflow,omitempty"`
	// MCPConfig is a path to an MCP servers JSON passed to the agent
	// (claude: --mcp-config). Sugar over AgentArgs.
	MCPConfig string `yaml:"mcp_config,omitempty"`
	// AgentArgs are extra args appended to the agent command verbatim.
	AgentArgs []string `yaml:"agent_args,omitempty"`
	// TimeoutMinutes bounds the agent prompt --wait; defaults to 60.
	TimeoutMinutes int `yaml:"timeout_minutes,omitempty"`
	// CatchUpMinutes is how late a missed occurrence may still run — the
	// laptop-was-asleep case. 0 uses the default (120); -1 never catches up.
	CatchUpMinutes int `yaml:"catch_up_minutes,omitempty"`
	// Disabled keeps the entry in the file but out of the scheduler.
	Disabled bool `yaml:"disabled,omitempty"`

	// Line is where this entry starts in automations.yaml, 1-based, so an
	// editor can be opened right at it. Set by Load, never written back.
	Line int `yaml:"-"`
}

type Config struct {
	// Automations are the entries that loaded. Only these run.
	Automations []Automation `yaml:"automations"`
	// Invalid are the entries that did not, one Diagnostic each. Kept out of
	// Automations so nothing has to check before scheduling, and reported so
	// the failure is visible where you look rather than only in a log.
	Invalid []Diagnostic `yaml:"-"`
}

// Diagnostic is one entry that did not load, and where to go and fix it.
type Diagnostic struct {
	// Name of the automation, or empty when the entry is malformed enough that
	// even its name could not be read.
	Name string
	// Line the entry starts on, 1-based. 0 when it could not be located.
	Line    int
	Message string
}

// String renders a diagnostic the way an editor and a human both want it:
// file, line, then what is wrong.
func (d Diagnostic) String() string {
	where := filepath.Base(Path())
	if d.Line > 0 {
		where = fmt.Sprintf("%s:%d", where, d.Line)
	}
	return where + ": " + d.Message
}

func (a *Automation) applyDefaults() {
	if a.Workspace == "" {
		a.Workspace = WorkspaceWorktree
	}
	if a.Agent == "" {
		a.Agent = "claude"
	}
	if a.TimeoutMinutes <= 0 {
		a.TimeoutMinutes = 60
	}
	if a.CatchUpMinutes == 0 {
		a.CatchUpMinutes = 120
	}
}

// CatchUp is how late this automation may still start; negative means never.
func (a Automation) CatchUp() time.Duration {
	if a.CatchUpMinutes < 0 {
		return 0
	}
	return time.Duration(a.CatchUpMinutes) * time.Minute
}

func (a *Automation) validate() error {
	if a.Name == "" {
		return fmt.Errorf("automation without a name")
	}
	if a.Cron == "" {
		return fmt.Errorf("%s: missing cron", a.Name)
	}
	if _, err := ParseCron(a.Cron); err != nil {
		return fmt.Errorf("%s: invalid cron %q: %w", a.Name, a.Cron, err)
	}
	if a.Repo == "" {
		return fmt.Errorf("%s: missing repo", a.Name)
	}
	if (a.Prompt == "") == (a.Workflow == "") {
		return fmt.Errorf("%s: exactly one of prompt or workflow is required", a.Name)
	}
	if a.Workspace != WorkspaceWorktree && a.Workspace != WorkspaceRoot && a.Workspace != WorkspaceExisting {
		return fmt.Errorf("%s: workspace must be worktree, root or existing, got %q", a.Name, a.Workspace)
	}
	if a.Workspace == WorkspaceExisting && strings.TrimSpace(a.WorkspaceID) == "" {
		return fmt.Errorf("%s: workspace_id is required for existing mode", a.Name)
	}
	if a.Workspace != WorkspaceExisting && a.WorkspaceID != "" {
		return fmt.Errorf("%s: workspace_id requires existing mode", a.Name)
	}
	if a.Model != "" && !KindAcceptsModel(a.Agent) {
		return fmt.Errorf("%s: agent %q takes no --model; put the flag it does take in agent_args", a.Name, a.Agent)
	}
	return nil
}

// modelFlagKinds are the agent kinds whose executable takes `--model`. Herdr
// forwards the flag verbatim, so a model: on any other kind would make the
// agent refuse to start — better to fail here, while the file is being
// written, than at 3am inside a scheduler goroutine.
//
// This is a known-good allowlist, not a gate: `herdr agent start --help`
// accepts more kinds than this table lists (see ADR 0003), and a kind missing
// here is a false rejection, not a safety net doing its job.
var modelFlagKinds = map[string]bool{
	"claude":   true,
	"codex":    true,
	"cursor":   true,
	"gemini":   true,
	"opencode": true,
	"grok":     true,
	"qwen":     true,
	"kimi":     true,
	"amp":      true,
	"droid":    true,
	"copilot":  true,
	"hermes":   true,
	"pi":       true,
}

// KindAcceptsModel reports whether this agent kind understands `--model`.
func KindAcceptsModel(kind string) bool { return modelFlagKinds[kind] }

// Dir is where automations.yaml lives.
func Dir() string { return hostpath.ConfigDir() }

// StateDir is where run history and scheduler state live.
func StateDir() string { return hostpath.StateDir() }

func Path() string { return filepath.Join(Dir(), "automations.yaml") }

// Load reads the config. A missing file is an empty config, not an error: the
// daemon idles until the first automation exists. An unreadable or unparseable
// file is an error — there is nothing to run and nothing to point at. Anything
// short of that is a Diagnostic on Config.Invalid, and the entries that did
// load still run.
func Load() (*Config, error) {
	f, err := Read()
	if err != nil {
		return nil, err
	}
	return f.Config, nil
}

// File is automations.yaml as read: the Config it loads to, plus the text and
// where each entry sits in it, so a writer can change one entry and leave every
// other byte alone.
type File struct {
	*Config
	Raw    []byte
	Exists bool
	// Items are the entries in file order, the ones that did not load included.
	Items []Item
	// dash is the 0-based column of the sequence's dashes, or -1 when the
	// automations are not a block sequence and have no lines of their own.
	dash int
	// keyLine is the 0-based line of the automations: key, -1 when absent.
	keyLine int
	// lines is Raw cut where yaml.v3 counts lines (lines.go).
	lines []line
}

// Item is one entry of the automations sequence, valid or not.
type Item struct {
	// Name is the entry's name, or empty when even that could not be read.
	Name string
	// Line is where the entry's first key sits, 1-based: the same number its
	// Automation or Diagnostic carries.
	Line int
	node *yaml.Node
	// first and last are the 0-based lines the entry occupies: its own leading
	// comments included, the blank lines around it not. -1 when the sequence
	// is not a block sequence.
	first, last int
}

// Decode decodes the entry as written, without defaults or validation.
func (it Item) Decode(v any) error { return it.node.Decode(v) }

// Read reads and loads automations.yaml. See Load for what counts as an error.
func Read() (*File, error) {
	raw, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return &File{Config: &Config{}, lines: splitLines(nil), dash: -1, keyLine: -1}, nil
	}
	if err != nil {
		return nil, err
	}
	f, err := parse(raw)
	if err != nil {
		return nil, err
	}
	f.Exists = true
	return f, nil
}

// parse loads a config from its text.
//
// It never panics. Everything below is meant not to, and the recover is the
// last line of defence: the daemon loads the file every 30 s, and a loader
// that panics on some file is a scheduler that is gone for good.
func parse(raw []byte) (f *File, err error) {
	defer func() {
		if r := recover(); r != nil {
			f, err = nil, fmt.Errorf("parse %s: cannot read it (%v)", Path(), r)
		}
	}()
	// Decoding through yaml.Node rather than straight into Config is what buys
	// the line numbers: a diagnostic that cannot say where to look is only
	// marginally better than silence.
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	f = &File{Config: &Config{}, Raw: raw, lines: splitLines(raw), dash: -1, keyLine: -1}
	var seq *yaml.Node
	next := len(f.lines) // the top-level key after automations:, if any
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode && !isNull(root) {
			return nil, fmt.Errorf("parse %s: line %d: expected automations: at the top", Path(), root.Line)
		}
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value != "automations" {
				continue
			}
			if seq != nil {
				return nil, fmt.Errorf("parse %s: line %d: automations is declared twice", Path(), root.Content[i].Line)
			}
			f.keyLine = root.Content[i].Line - 1
			seq = root.Content[i+1]
			if i+2 < len(root.Content) {
				next = root.Content[i+2].Line - 1
			}
		}
	}
	switch {
	case seq == nil, isNull(seq), seq.Kind == yaml.SequenceNode && len(seq.Content) == 0:
		return f, nil // no automations yet
	case seq.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("parse %s: line %d: automations must be a list", Path(), seq.Line)
	}

	cfg := f.Config
	seen := map[string]bool{}
	for _, node := range seq.Content {
		f.Items = append(f.Items, Item{Name: nameOf(*node), Line: node.Line, node: node})
		var a Automation
		if err := node.Decode(&a); err != nil {
			cfg.Invalid = append(cfg.Invalid, Diagnostic{
				Name: nameOf(*node), Line: node.Line, Message: cleanYAMLError(err),
			})
			continue
		}
		a.Line = node.Line
		a.applyDefaults()
		a.Repo = expandHome(a.Repo)
		a.MCPConfig = expandHome(a.MCPConfig)

		if err := a.validate(); err != nil {
			cfg.Invalid = append(cfg.Invalid, Diagnostic{
				Name: a.Name, Line: a.Line, Message: err.Error(),
			})
			continue
		}
		if seen[a.Name] {
			cfg.Invalid = append(cfg.Invalid, Diagnostic{
				Name: a.Name, Line: a.Line,
				Message: fmt.Sprintf("%s: an automation by that name is already declared above", a.Name),
			})
			continue
		}
		seen[a.Name] = true
		cfg.Automations = append(cfg.Automations, a)
	}
	f.locate(seq, next)
	return f, nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// nameOf recovers just the name from an entry too malformed to decode whole,
// so the diagnostic can still say which automation it is about.
func nameOf(node yaml.Node) string {
	var partial struct {
		Name string `yaml:"name"`
	}
	if node.Decode(&partial) != nil {
		return ""
	}
	return partial.Name
}

// cleanYAMLError trims the library's line prefix: the diagnostic carries the
// entry's own line, which is the one worth opening.
func cleanYAMLError(err error) string {
	msg := err.Error()
	if _, rest, found := strings.Cut(msg, ": "); found && strings.HasPrefix(msg, "yaml: line ") {
		return rest
	}
	return strings.TrimPrefix(msg, "yaml: ")
}

// Find returns the automation named name, or nil.
func (c *Config) Find(name string) *Automation {
	for i := range c.Automations {
		if c.Automations[i].Name == name {
			return &c.Automations[i]
		}
	}
	return nil
}

// Diagnostic returns the reason an automation by that name did not load, or
// nil. "No automation named x" is the wrong thing to say about one that is
// right there in the file with a broken cron.
func (c *Config) Diagnostic(name string) *Diagnostic {
	for i := range c.Invalid {
		if c.Invalid[i].Name == name {
			return &c.Invalid[i]
		}
	}
	return nil
}

// Declares reports whether the file mentions this name at all, valid or not.
// The wizard needs it: refusing a duplicate only when the existing entry
// happens to be valid writes a second one nobody asked for.
func (c *Config) Declares(name string) bool {
	return c.Find(name) != nil || c.Diagnostic(name) != nil
}

func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
