package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// byHand is a file someone keeps by hand: a header, blank lines between
// entries, comments inside them, quoted values, a flow list, a comment after a
// value, a block scalar, a footer.
const byHand = `# Moved from the Mac on 2026-10-08.
automations:
  - name: "[HLT] Check"
    cron: "CRON_TZ=Europe/Vienna 5 9 * * *"
    repo: /home/agent/x
    workspace: existing
    workspace_id: w3
    agent: grok
    agent_args: ["--permission-mode", "bypassPermissions"]
    prompt: |
      Check things.

      # a heading inside the prompt
      Report.

  - name: "[THO] Storno"
    # 09:30, not 09:00: collided with another run.
    cron: "CRON_TZ=Europe/Vienna 30 9 * * 1"
    repo: /home/agent/storno
    workflow: storno-2026   # hwf runs .hwf/workflows/storno-2026.yaml
    timeout_minutes: 60


  - name: last
    cron: "@daily"
    repo: /x
    prompt: p
# footer
`

func loaded(t *testing.T, raw string) *File {
	t.Helper()
	f, err := parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Invalid) > 0 {
		t.Fatalf("fixture has broken entries: %v", f.Invalid)
	}
	return f
}

// onlyChanged fails unless before and after differ only inside lines
// [from, to] of before (0-based, inclusive): every other line byte-identical.
func onlyChanged(t *testing.T, before, after []byte, from, to int) {
	t.Helper()
	b, a := splitLines(before), splitLines(after)
	pre := 0
	for pre < len(b) && pre < len(a) && b[pre] == a[pre] {
		pre++
	}
	suf := 0
	for suf < len(b)-pre && suf < len(a)-pre && b[len(b)-1-suf] == a[len(a)-1-suf] {
		suf++
	}
	if pre < from || len(b)-1-suf > to {
		t.Errorf("changed lines %d..%d of the original, allowed %d..%d\n--- after:\n%s", pre, len(b)-1-suf, from, to, after)
	}
}

func setOne(t *testing.T, f *File, name string, set []Pair, del []string) []byte {
	t.Helper()
	out, err := f.SetKeys(*f.Item(name), set, del)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parse(out); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	return out
}

func scalar(tag, v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v} }

func TestPausingAddsOneLineAndResumingTakesItBack(t *testing.T) {
	f := loaded(t, byHand)
	it := f.Item("[THO] Storno")
	paused := setOne(t, f, "[THO] Storno", []Pair{{"disabled", scalar("!!bool", "true")}}, nil)
	onlyChanged(t, f.Raw, paused, it.last+1, it.last)
	if !strings.Contains(string(paused), "    timeout_minutes: 60\n    disabled: true\n") {
		t.Errorf("disabled not appended as the entry's last key:\n%s", paused)
	}

	g := loaded(t, string(paused))
	if !g.Find("[THO] Storno").Disabled {
		t.Fatal("not paused")
	}
	resumed := setOne(t, g, "[THO] Storno", nil, []string{"disabled"})
	if string(resumed) != byHand {
		t.Errorf("pause then resume is not the original:\n%s", resumed)
	}
}

func TestAChangedValueKeepsItsQuotesAndItsComment(t *testing.T) {
	f := loaded(t, byHand)
	out := setOne(t, f, "[THO] Storno", []Pair{
		{"cron", scalar("!!str", "CRON_TZ=Europe/Vienna 45 9 * * 1")},
		{"workflow", scalar("!!str", "storno-2027")},
	}, nil)
	want := strings.NewReplacer(
		`cron: "CRON_TZ=Europe/Vienna 30 9 * * 1"`, `cron: "CRON_TZ=Europe/Vienna 45 9 * * 1"`,
		`workflow: storno-2026   # hwf`, `workflow: storno-2027   # hwf`,
	).Replace(byHand)
	if string(out) != want {
		t.Errorf("got\n%s", out)
	}
}

// A value equal as data is not rewritten, so its formatting survives: the app
// sends JSON, in which the flow list and the block scalar look nothing alike.
func TestAnUnchangedValueIsLeftAsWritten(t *testing.T) {
	f := loaded(t, byHand)
	var args, prompt yaml.Node
	yaml.Unmarshal([]byte(`["--permission-mode","bypassPermissions"]`), &args)
	yaml.Unmarshal([]byte(`"Check things.\n\n# a heading inside the prompt\nReport.\n"`), &prompt)
	out := setOne(t, f, "[HLT] Check", []Pair{{"agent_args", args.Content[0]}, {"prompt", prompt.Content[0]}}, nil)
	if string(out) != byHand {
		t.Errorf("unchanged values were rewritten:\n%s", out)
	}
}

func TestDeletingAKeyTakesItsCommentsAlong(t *testing.T) {
	f := loaded(t, byHand)
	it := f.Item("[THO] Storno")
	out := setOne(t, f, "[THO] Storno", nil, []string{"cron"})
	onlyChanged(t, f.Raw, out, it.first, it.last)
	if strings.Contains(string(out), "09:30") || strings.Contains(string(out), "30 9 * * 1") {
		t.Errorf("cron or its comment survived:\n%s", out)
	}
}

func TestAReplacedListIsRewrittenInItsOwnStyle(t *testing.T) {
	f := loaded(t, byHand)
	var args yaml.Node
	yaml.Unmarshal([]byte(`["--permission-mode","plan"]`), &args)
	out := setOne(t, f, "[HLT] Check", []Pair{{"agent_args", args.Content[0]}}, nil)
	if !strings.Contains(string(out), "    agent_args: [--permission-mode, plan]\n    prompt: |\n") {
		t.Errorf("got\n%s", out)
	}
}

// What JSON hands over (double-quoted strings, flow lists) is written as
// someone would by hand, and a string that only looks like a number or a bool
// stays a string.
func TestNewValuesAreWrittenAsBlockYAML(t *testing.T) {
	f := loaded(t, byHand)
	var body yaml.Node
	yaml.Unmarshal([]byte(`{"model":"60","mcp_config":"[x]","agent_args":["a","b"],"prompt":"two\nlines\n"}`), &body)
	m := body.Content[0]
	var set []Pair
	for i := 0; i < len(m.Content); i += 2 {
		set = append(set, Pair{m.Content[i].Value, m.Content[i+1]})
	}
	out := setOne(t, f, "last", set, nil)
	if !strings.Contains(string(out), "    prompt: |\n      two\n      lines\n    model: \"60\"\n    mcp_config: '[x]'\n") {
		t.Errorf("got\n%s", out)
	}
	if a := loaded(t, string(out)).Find("last"); a.Model != "60" || a.MCPConfig != "[x]" {
		t.Errorf("round trip lost a string: %+v", a)
	}
}

func TestReplaceAppendRemoveTouchOnlyTheirEntry(t *testing.T) {
	f := loaded(t, byHand)
	it := f.Item("[THO] Storno")

	replaced, err := f.Replace(*it, f.Text(*it))
	if err != nil || string(replaced) != byHand {
		t.Errorf("replacing an entry with its own text changed the file (%v):\n%s", err, replaced)
	}

	removed, err := f.Remove(*it)
	if err != nil {
		t.Fatal(err)
	}
	onlyChanged(t, f.Raw, removed, it.first, it.last+2) // and the two blank lines after it
	if g := loaded(t, string(removed)); len(g.Automations) != 2 || g.Item("[THO] Storno") != nil {
		t.Errorf("remove: %+v", g.Automations)
	}

	appended, err := f.Append("name: new\ncron: \"@hourly\"\nrepo: /x\nprompt: p\n")
	if err != nil {
		t.Fatal(err)
	}
	last := f.Item("last")
	onlyChanged(t, f.Raw, appended, last.last+1, last.last)
	if !strings.Contains(string(appended), "    prompt: p\n\n  - name: new\n    cron: \"@hourly\"\n") {
		t.Errorf("append:\n%s", appended)
	}
}

func TestTheFirstEntryGoesUnderAnEmptyList(t *testing.T) {
	for _, raw := range []string{"", "# nothing yet\n", "automations:\n", "automations: []\n# keep\n"} {
		f := loaded(t, raw)
		out, err := f.Append("name: a\ncron: \"@daily\"\nrepo: /x\nprompt: p\n")
		if err != nil {
			t.Fatal(err)
		}
		if g := loaded(t, string(out)); len(g.Automations) != 1 {
			t.Errorf("from %q: %q", raw, out)
		}
	}
}

// Entries written as flow mappings cannot be edited line by line; the one
// entry is re-encoded instead, and its neighbours are not.
func TestAFlowEntryIsReEncodedAlone(t *testing.T) {
	raw := "automations:\n  - {name: a, cron: \"@daily\", repo: /x, prompt: p}\n  # b's heading\n  - {name: b, cron: \"@daily\", repo: /x, prompt: p}\n"
	f := loaded(t, raw)
	out := setOne(t, f, "a", []Pair{{"disabled", scalar("!!bool", "true")}}, nil)
	onlyChanged(t, f.Raw, out, 1, 1)
	if !loaded(t, string(out)).Find("a").Disabled {
		t.Errorf("got\n%s", out)
	}
}

// ── Change: the file, the lock, the checks ───────────────────────────

func onDisk(t *testing.T, raw string) string {
	t.Helper()
	withConfig(t, raw)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	return Path()
}

func TestChangeWritesAtomicallyThroughASymlink(t *testing.T) {
	onDisk(t, "")
	real := filepath.Join(t.TempDir(), "dotfiles-automations.yaml")
	if err := os.WriteFile(real, []byte(byHand), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, Path()); err != nil {
		t.Fatal(err)
	}
	changed, _, after, err := SetDisabled("last", true, Hash([]byte(byHand)))
	if err != nil || !changed {
		t.Fatalf("SetDisabled: %v %v", changed, err)
	}
	if fi, _ := os.Lstat(Path()); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a file")
	}
	raw, _ := os.ReadFile(real)
	if string(raw) != string(after.Raw) || !strings.Contains(string(raw), "    prompt: p\n    disabled: true\n# footer") {
		t.Errorf("target not written:\n%s", raw)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", fi.Mode().Perm())
	}
	if again, _, _, err := SetDisabled("last", true, ""); err != nil || again {
		t.Errorf("pausing twice: changed=%v err=%v", again, err)
	}
}

func TestChangeRefusesAStaleRead(t *testing.T) {
	onDisk(t, byHand)
	_, _, _, err := SetDisabled("last", true, Hash([]byte("something else")))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Current != Hash([]byte(byHand)) {
		t.Fatalf("err = %v", err)
	}
	if raw, _ := os.ReadFile(Path()); string(raw) != byHand {
		t.Error("file written despite the conflict")
	}
}

// Whatever an edit claims, it may not change an entry it did not name.
func TestChangeRefusesAnEditThatReachesAnotherEntry(t *testing.T) {
	onDisk(t, byHand)
	_, _, err := Change("", false, func(f *File) (Edit, error) {
		raw := strings.Replace(string(f.Raw), "timeout_minutes: 60", "timeout_minutes: 61", 1)
		return Edit{Raw: []byte(raw), Touched: []string{"last"}}, nil
	})
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v", err)
	}
	if raw, _ := os.ReadFile(Path()); string(raw) != byHand {
		t.Error("file written despite the refusal")
	}
}

// A broken entry elsewhere does not stop an edit, as long as it stays broken
// the same way.
func TestChangeWorksAroundABrokenEntryAndLeavesItBroken(t *testing.T) {
	broken := byHand + "  - name: broken\n    cron: \"0 99 * * *\"\n    repo: /x\n    prompt: p\n"
	onDisk(t, broken)
	changed, _, after, err := SetDisabled("last", true, "")
	if err != nil || !changed {
		t.Fatalf("SetDisabled: %v %v", changed, err)
	}
	if after.Diagnostic("broken") == nil {
		t.Error("the broken entry was dropped or fixed")
	}
	if _, _, _, err := SetDisabled("broken", true, ""); err == nil {
		t.Error("pausing a broken entry should say it does not load")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	onDisk(t, byHand)
	_, after, err := Change("", true, func(f *File) (Edit, error) {
		raw, err := f.SetKeys(*f.Item("last"), []Pair{{"disabled", scalar("!!bool", "true")}}, nil)
		return Edit{Raw: raw, Touched: []string{"last"}, MustLoad: []string{"last"}}, err
	})
	if err != nil || !after.Find("last").Disabled {
		t.Fatalf("dry run: %v", err)
	}
	if raw, _ := os.ReadFile(Path()); string(raw) != byHand {
		t.Error("a dry run wrote the file")
	}
}
