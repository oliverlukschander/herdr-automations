package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withConfig(t *testing.T, yaml string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(dir, "automations.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	withConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Automations) != 0 {
		t.Fatalf("expected empty config, got %d entries", len(cfg.Automations))
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	withConfig(t, `
automations:
  - name: triage
    cron: "0 9 * * 1-5"
    repo: ~/Projects/foo
    prompt: "Triage the issues"
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Automations[0]
	if a.Workspace != WorkspaceWorktree || a.Agent != "claude" || a.TimeoutMinutes != 60 {
		t.Fatalf("defaults not applied: %+v", a)
	}
	home, _ := os.UserHomeDir()
	if a.Repo != filepath.Join(home, "Projects/foo") {
		t.Fatalf("home not expanded: %s", a.Repo)
	}
}

func TestLoadCarriesTheLineEachEntryStartsOn(t *testing.T) {
	withConfig(t, `automations:
  - name: first
    cron: "@daily"
    repo: /x
    prompt: p
  - name: second
    cron: "@daily"
    repo: /x
    prompt: p
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Automations[0].Line; got != 2 {
		t.Errorf("first starts on line %d, want 2", got)
	}
	if got := cfg.Automations[1].Line; got != 6 {
		t.Errorf("second starts on line %d, want 6", got)
	}
}

func TestLoadKeepsTheGoodEntriesWhenOneIsBad(t *testing.T) {
	// The failure this exists for: one error for the whole file meant a typo in
	// the seventh automation stopped the other six, silently.
	withConfig(t, `automations:
  - {name: fine, cron: "@daily", repo: /x, prompt: p}
  - {name: broken, cron: "0 99 * * *", repo: /x, prompt: p}
  - {name: also-fine, cron: "@hourly", repo: /x, prompt: p}
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a single bad entry must not fail the load: %v", err)
	}
	if len(cfg.Automations) != 2 {
		t.Fatalf("loaded %d entries, want the two good ones", len(cfg.Automations))
	}
	if len(cfg.Invalid) != 1 {
		t.Fatalf("diagnostics = %+v, want one", cfg.Invalid)
	}
	d := cfg.Invalid[0]
	if d.Name != "broken" {
		t.Errorf("diagnostic names %q", d.Name)
	}
	if d.Line != 3 {
		t.Errorf("diagnostic points at line %d, want 3", d.Line)
	}
	if got := d.String(); !strings.Contains(got, "automations.yaml:3") {
		t.Errorf("String() = %q, want a file:line an editor can open", got)
	}
}

func TestLoadStillFailsOnAFileItCannotParse(t *testing.T) {
	// Nothing to run and nothing to point at: that is a real error.
	withConfig(t, "automations: [oh: dear\n  ]]] not yaml")
	if _, err := Load(); err == nil {
		t.Fatal("want an error for a file that is not YAML")
	}
}

func TestLoadDiagnosesAnEntryWhoseTypesAreWrong(t *testing.T) {
	withConfig(t, `automations:
  - {name: fine, cron: "@daily", repo: /x, prompt: p}
  - {name: odd, cron: "@daily", repo: /x, prompt: p, timeout_minutes: soon}
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a malformed entry must not fail the load: %v", err)
	}
	if len(cfg.Automations) != 1 || len(cfg.Invalid) != 1 {
		t.Fatalf("loaded %d, invalid %d", len(cfg.Automations), len(cfg.Invalid))
	}
	// Even undecodable, the name is worth recovering: it is how the board and
	// `run` refer to the entry.
	if cfg.Invalid[0].Name != "odd" {
		t.Errorf("diagnostic names %q, want odd", cfg.Invalid[0].Name)
	}
}

func TestDiagnosticAndDeclaresSeeTheEntriesThatFailed(t *testing.T) {
	withConfig(t, `automations:
  - {name: broken, cron: "nope", repo: /x, prompt: p}
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Find("broken") != nil {
		t.Error("a broken entry must not be findable as runnable")
	}
	if cfg.Diagnostic("broken") == nil {
		t.Error(`Diagnostic("broken") = nil: "no automation named broken" is the wrong thing to say`)
	}
	if !cfg.Declares("broken") {
		t.Error("Declares() must see it, or the wizard writes a second one")
	}
}

func TestModelIsPassedThroughUnvalidated(t *testing.T) {
	// Model names change faster than any allowlist would survive, so anything
	// non-empty loads and the agent gets the final say.
	withConfig(t, `
automations:
  - {name: a, cron: "@daily", repo: /x, prompt: p, model: some-unreleased-model}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Automations[0].Model != "some-unreleased-model" {
		t.Fatalf("model not kept: %q", cfg.Automations[0].Model)
	}
}

func TestCollisionsReportsSharedOccurrencesOnce(t *testing.T) {
	withConfig(t, `
automations:
  - {name: early, cron: "0 6 * * *", repo: /x, prompt: p}
  - {name: also-early, cron: "0 6 * * *", repo: /x, prompt: p}
  - {name: alone, cron: "0 14 * * *", repo: /x, prompt: p}
  - {name: off, cron: "0 6 * * *", repo: /x, prompt: p, disabled: true}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	clashes := cfg.Collisions()
	// Both are daily, so they clash seven times over the horizon — but that is
	// one fact about the schedule, not seven.
	if len(clashes) != 1 {
		t.Fatalf("expected a single deduped collision, got %d: %+v", len(clashes), clashes)
	}
	if len(clashes[0].Names) != 2 {
		t.Fatalf("disabled entries must not collide: %+v", clashes[0].Names)
	}
}

func TestCollidesWithNamesTheClashingAutomations(t *testing.T) {
	withConfig(t, `
automations:
  - {name: sprint, cron: "0 9 * * 1", repo: /x, prompt: p}
  - {name: nightly, cron: "0 3 * * *", repo: /x, prompt: p}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.CollidesWith("0 9 * * 1"); len(got) != 1 || got[0] != "sprint" {
		t.Fatalf("expected sprint, got %v", got)
	}
	if got := cfg.CollidesWith("30 9 * * 1"); len(got) != 0 {
		t.Fatalf("expected no clash, got %v", got)
	}
}
