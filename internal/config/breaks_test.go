package config

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	ls  = "\u2028"
	ps  = "\u2029"
	nel = "\u0085"
)

// Lines are counted where yaml.v3 counts them, so a node's Line is the right
// line to edit.
func TestLinesBreakWhereYAMLBreaksThem(t *testing.T) {
	raw := "a: 'x" + ls + "  y'\r\nb: \"z" + nel + "w\"\rc: 'p" + ps + "q'\nd: 1\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	lines := splitLines([]byte(raw))
	for i := 0; i < len(doc.Content[0].Content); i += 2 {
		k := doc.Content[0].Content[i]
		if got := lines[k.Line-1].text; !strings.HasPrefix(got, k.Value+":") {
			t.Errorf("key %s is on line %d, which reads %q", k.Value, k.Line, got)
		}
	}
	if !bytes.Equal(joinLines(lines), []byte(raw)) {
		t.Error("joinLines does not give the file back")
	}
}

// Review finding 1, as the reviewer saw it through the app: a prompt with an
// LS in it, then an edit of the next entry. The LS is written escaped, and
// the edit lands on b's cron, not on its repo.
func TestAnLSInOneEntryDoesNotMoveAnEditOfTheNext(t *testing.T) {
	onDisk(t, "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n  - name: b\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n")
	write := func(name string, set ...Pair) {
		t.Helper()
		_, _, err := Change("", false, func(f *File) (Edit, error) {
			raw, err := f.SetKeys(*f.Item(name), set, nil)
			return Edit{Raw: raw, Touched: []string{name}, MustLoad: []string{name}}, err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	write("a", Pair{"prompt", scalar("!!str", "first line"+ls+"second line")})
	write("b", Pair{"cron", scalar("!!str", "0 10 * * *")})

	f, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if b := f.Find("b"); b.Cron != "0 10 * * *" || b.Repo != "/x" {
		t.Errorf("b = %+v", b)
	}
	if a := f.Find("a"); a.Prompt != "first line"+ls+"second line" {
		t.Errorf("a's prompt = %q", a.Prompt)
	}
	if strings.Contains(string(f.Raw), ls) {
		t.Errorf("the LS was written raw:\n%s", f.Raw)
	}
}

// Review finding 1: an LS someone else wrote raw into a quoted value, before
// the entry being paused. Pausing panicked in editMapping.
func TestARawLSBeforeTheEntryBeingPaused(t *testing.T) {
	raw := "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    model: 'grok" + ls + "  4.6'\n    prompt: p\n" +
		"  - name: b\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n"
	onDisk(t, raw)
	changed, _, after, err := SetDisabled("b", true, "")
	if err != nil || !changed || !after.Find("b").Disabled {
		t.Fatalf("pause: %v %v", changed, err)
	}
	if want := raw + "    disabled: true\n"; string(after.Raw) != want {
		t.Errorf("got\n%q\nwant\n%q", after.Raw, want)
	}
}

// Review finding 1: files that made Load panic. The daemon loads every 30 s.
func TestFilesThatPanickedTheLoader(t *testing.T) {
	tenLS := strings.Repeat(ls, 10)
	for _, raw := range []string{
		"automations:\r-",
		"automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: 'x" + tenLS + "y'\n  - {name: b, cron: \"@daily\", repo: /x, prompt: p}\n",
		"automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: p" + ls + ls + ls + "\n",
	} {
		f, err := parse([]byte(raw))
		if err != nil && strings.Contains(err.Error(), "cannot read it") {
			t.Errorf("%q: the loader panicked: %v", raw, err)
			continue
		}
		if err != nil {
			continue // a plain YAML error is a fine answer
		}
		for _, it := range f.Items {
			if it.first >= 0 && !f.readsBack(it) {
				t.Errorf("%q: %s is editable but does not read back", raw, it.Name)
			}
		}
	}
}

// NEL, LS, PS and \r are line breaks to yaml.v3: written raw into a value,
// each one shifts every line after it. Written, they are escaped.
func TestNewTextEscapesHiddenLineBreaks(t *testing.T) {
	for _, brk := range []string{ls, ps, nel, "\r"} {
		text, err := NewEntryText([]Pair{{"name", scalar("!!str", "n")}, {"prompt", scalar("!!str", "one"+brk+"two\nthree")}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(text, "\r\u0085\u2028\u2029") {
			t.Errorf("%q written raw: %q", brk, text)
		}
		var back struct{ Prompt string }
		if yaml.Unmarshal([]byte(text), &back); back.Prompt != "one"+brk+"two\nthree" {
			t.Errorf("%q: read back %q", brk, back.Prompt)
		}
	}
}

// Review finding 3: a key written with nothing after the colon.
func TestAnEmptyValueGetsItsSpace(t *testing.T) {
	f := loaded(t, "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n    model:\n    disabled:   # paused by hand\n")
	out := setOne(t, f, "a", []Pair{{"model", scalar("!!str", "opus")}, {"disabled", scalar("!!bool", "true")}}, nil)
	if !strings.Contains(string(out), "    model: opus\n    disabled: true   # paused by hand\n") {
		t.Errorf("got\n%s", out)
	}
}

// Review finding 4: a |+ value keeps its trailing newlines when a key is added.
func TestAKeptBlockKeepsItsTrailingLines(t *testing.T) {
	raw := "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: |+\n      text\n\n\n  - name: b\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n"
	onDisk(t, raw)
	_, _, after, err := SetDisabled("a", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := after.Find("a"); a.Prompt != "text\n\n\n" || !a.Disabled {
		t.Errorf("a = %q disabled=%v\n%s", a.Prompt, a.Disabled, after.Raw)
	}
}

// Review finding 5: a tab before an inline comment is still a comment.
func TestATabBeforeACommentKeepsTheComment(t *testing.T) {
	f := loaded(t, "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    workflow: w\t# why\n")
	out := setOne(t, f, "a", []Pair{{"workflow", scalar("!!str", "v")}}, nil)
	if !strings.Contains(string(out), "    workflow: v\t# why\n") {
		t.Errorf("got\n%q", out)
	}
}

// Review finding 6: a CRLF file stays CRLF, untouched lines and new ones.
func TestACRLFFileStaysCRLF(t *testing.T) {
	raw := strings.ReplaceAll(byHand, "\n", "\r\n")
	onDisk(t, raw)
	if _, _, _, err := SetDisabled("[THO] Storno", true, ""); err != nil {
		t.Fatal(err)
	}
	_, after, err := Change("", false, func(f *File) (Edit, error) {
		raw, err := f.SetKeys(*f.Item("last"), []Pair{{"cron", scalar("!!str", "@hourly")}, {"model", scalar("!!str", "x")}}, nil)
		return Edit{Raw: raw, Touched: []string{"last"}, MustLoad: []string{"last"}}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(after.Raw), "\n"); strings.Count(string(after.Raw), "\r\n") != n {
		t.Errorf("mixed line endings:\n%q", after.Raw)
	}
}

// Every break character yaml.v3 knows, and a few that change how a line
// reads, dropped at every position of a hand-written file: loading must never
// panic, and every edit that succeeds must leave the other entries' text
// exactly as it was and give the edited one the value asked for.
func TestBreakCharactersAnywhereNeverPanicOrSpill(t *testing.T) {
	quiet(t)
	breaks := []string{"\r", "\r\n", "\n", nel, ls, ps}
	for i, ch := range append(breaks, "\t", "#", "'", "\"", "-", ":", " ") {
		step := 1 // every position for the line breaks, every fifth for the rest
		if i >= len(breaks) {
			step = 5
		}
		for p := 0; p <= len(byHand); p += step {
			if p > 0 && p < len(byHand) && byHand[p]&0xC0 == 0x80 {
				continue // inside a multi-byte character
			}
			spliceEverything(t, byHand[:p]+ch+byHand[p:], true)
		}
	}
}

// FuzzSplice is the same property for `go test -fuzz`; the seeds run always.
func FuzzSplice(f *testing.F) {
	for _, seed := range []string{byHand, "automations:\r-", "automations:\n  - name: a\n    cron: '@daily'\n    repo: /x\n    prompt: 'x" + ls + "y'\n"} {
		f.Add(seed)
	}
	quiet(f)
	f.Fuzz(func(t *testing.T, raw string) { spliceEverything(t, raw, true) })
}

// sameEntry is what Change holds an untouched entry to.
func sameEntry(f *File, a Item, g *File, b Item) bool { return f.untouchable(a) == g.untouchable(b) }

// quiet keeps parse from asking the real herdr where the config lives: every
// parse error names the file, and without HERDR_PLUGIN_CONFIG_DIR finding it
// means running `herdr plugin config-dir`.
func quiet(tb testing.TB) {
	tb.Setenv("HERDR_PLUGIN_CONFIG_DIR", tb.TempDir())
	tb.Setenv("HERDR_BIN_PATH", "/nonexistent/herdr")
}

func spliceEverything(t *testing.T, raw string, edits bool) {
	t.Helper()
	f, err := parse([]byte(raw))
	if err != nil {
		if strings.Contains(err.Error(), "cannot read it") {
			t.Fatalf("parse panicked on %q: %v", raw, err)
		}
		return
	}
	// accepted is whether Change would write out. When it would, no other
	// entry may differ (matched by position: names may be missing or
	// repeated) and the edited one must be what was asked for.
	accepted := func(op string, i int, out []byte, shift int, want map[string]any) {
		t.Helper()
		g, err := parse(out)
		if err != nil || len(g.Items) != len(f.Items)+shift {
			return // Change refuses what does not load, or loses entries
		}
		e := Edit{Raw: out, Touched: []string{f.Items[i].Name}}
		if want != nil {
			e.Entry, e.Want = f.Items[i].Name, want
		}
		if keeps(f, g, e) != nil {
			return // refused: nothing would be written
		}
		for j := range f.Items {
			if j == i {
				continue
			}
			k := j
			if j > i {
				k = j + shift
			}
			if !sameEntry(f, f.Items[j], g, g.Items[k]) {
				t.Fatalf("%s of entry %d reached into entry %d, and Change would write it:\n%q\n→\n%q", op, i, j, raw, out)
			}
		}
		if want != nil && shift == 0 {
			if m, _ := g.Items[i].Map(); !reflect.DeepEqual(m, want) {
				t.Fatalf("%s of entry %d wrote %v, want %v:\n%q\n→\n%q", op, i, m, want, raw, out)
			}
		}
	}
	for i, it := range f.Items {
		_ = f.Text(it)
		if it.first < 0 || !edits || it.Name == "" { // nothing can address an entry without a name
			continue
		}
		if was, err := it.Map(); err == nil {
			want := maps.Clone(was)
			want["disabled"] = true
			if out, err := f.SetKeys(it, []Pair{{"disabled", scalar("!!bool", "true")}}, nil); err == nil {
				accepted("pause", i, out, 0, want)
			}
			if out, err := f.Replace(it, f.Text(it)); err == nil {
				accepted("replace", i, out, 0, was)
			}
		}
		if out, err := f.Remove(it); err == nil {
			accepted("remove", i, out, -1, nil)
		}
	}
	_, _ = f.Append("name: appended\ncron: \"@daily\"\nrepo: /x\nprompt: p\n")
}

// yaml.v3 puts line numbers into some diagnostics. An edit above a broken
// entry moves them; the entry is still broken the same way, and the edit
// must not be refused for it.
func TestAnEditAboveATypeErrorIsNotRefused(t *testing.T) {
	onDisk(t, byHand+"  - {name: odd, cron: \"@daily\", repo: /x, prompt: p, timeout_minutes: soon}\n")
	if _, _, _, err := SetDisabled("[HLT] Check", true, ""); err != nil {
		t.Fatalf("pause refused: %v", err)
	}
}

// Review round 2, finding 1: a comment closing an entry became the head of
// the disabled: key a pause appended after it, and resume took it along.
func TestACommentClosingAnEntrySurvivesPauseAndResume(t *testing.T) {
	raw := "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n    # TODO: lower the timeout once stable\n" +
		"  - name: b\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n"
	onDisk(t, raw)
	for _, paused := range []bool{true, false} {
		if _, _, _, err := SetDisabled("a", paused, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile(Path()); string(got) != raw {
		t.Errorf("pause then resume changed the file:\n%s", got)
	}
}

// Review round 2, finding 3: a |+ value at the very end of the file made the
// entry, and so every append, impossible to edit; deleting the entry after
// such a value was refused.
func TestAKeptBlockAtTheEndOfTheFile(t *testing.T) {
	onDisk(t, "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: |+\n      text\n\n")
	_, _, after, err := SetDisabled("a", true, "")
	if err != nil || after.Find("a").Prompt != "text\n\n" {
		t.Fatalf("pause: %v", err)
	}

	onDisk(t, "automations:\n  - name: a\n    cron: \"@daily\"\n    repo: /x\n    prompt: |+\n      text\n\n"+
		"  - name: b\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n")
	_, after, err = Change("", false, func(f *File) (Edit, error) {
		raw, err := f.Remove(*f.Item("b"))
		return Edit{Raw: raw, Touched: []string{"b"}}, err
	})
	if err != nil || after.Find("a").Prompt != "text\n\n" || after.Item("b") != nil {
		t.Fatalf("delete b: %v", err)
	}
}

// Review round 2, finding 5: appending needs only where the last entry ends,
// not that the entry could be edited itself; one using an anchor from
// elsewhere cannot.
func TestAppendingAfterAnEntryThatCannotBeEdited(t *testing.T) {
	onDisk(t, "defaults: &d\n  repo: /x\nautomations:\n  - name: a\n    cron: \"@daily\"\n    <<: *d\n    prompt: p\n")
	_, after, err := Change("", false, func(f *File) (Edit, error) {
		if f.Item("a").first >= 0 {
			t.Fatal("an entry merging an anchor from elsewhere should not be editable line by line")
		}
		raw, err := f.Append("name: b\ncron: \"@daily\"\nrepo: /y\nprompt: p\n")
		return Edit{Raw: raw, Touched: []string{"b"}, MustLoad: []string{"b"}}, err
	})
	if err != nil || after.Find("b") == nil || after.Find("a").Repo != "/x" {
		t.Fatalf("append: %v", err)
	}
}

// Review round 2, finding 2 and 4: a name declared twice cannot be edited by
// name; the refusal names every line.
func TestADuplicatedNameIsNotEditedByName(t *testing.T) {
	raw := "automations:\n  - name: x\n    cron: \"@daily\"\n    repo: /good\n    prompt: keep me\n" +
		"  - name: x\n    cron: \"@daily\"\n    repo: /dup\n    prompt: p\n"
	onDisk(t, raw)
	_, _, _, err := SetDisabled("x", true, "")
	var dup *DuplicateError
	if !errors.As(err, &dup) || !reflect.DeepEqual(dup.Lines, []int{2, 6}) || !strings.Contains(err.Error(), "lines 2, 6") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(Path()); string(got) != raw {
		t.Error("written anyway")
	}
}
