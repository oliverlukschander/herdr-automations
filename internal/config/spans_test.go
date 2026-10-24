package config

import (
	"strings"
	"testing"
)

// handWritten has what a file someone maintains by hand has: a header, a
// heading comment over an entry, comments inside one, a block scalar with a
// line that only looks like a comment, uneven blank lines, and a footer.
const handWritten = `# header about the whole file
automations:
  - name: first
    cron: "@daily"
    repo: /x
    prompt: |
      Do the thing.
      # not a comment: part of the prompt

      Then stop.


  # heading for second
  - name: second
    # why 09:30
    cron: "30 9 * * 1"
    repo: /x
    workflow: w
  - {name: third, cron: "@hourly", repo: /x, prompt: p}

# footer, belongs to nobody
`

func spansOf(t *testing.T, raw string) (*File, []string) {
	t.Helper()
	f, err := parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(f.Items))
	}
	return f, texts(f.Raw)
}

func TestAnEntryOwnsItsLinesAndNothingBetween(t *testing.T) {
	f, lines := spansOf(t, handWritten)
	want := []struct{ first, last string }{
		{"  - name: first", "      Then stop."},
		{"  # heading for second", "    workflow: w"},
		{"  - {name: third, cron: \"@hourly\", repo: /x, prompt: p}", "  - {name: third, cron: \"@hourly\", repo: /x, prompt: p}"},
	}
	for i, w := range want {
		it := f.Items[i]
		if lines[it.first] != w.first || lines[it.last] != w.last {
			t.Errorf("%s spans %q … %q, want %q … %q", it.Name, lines[it.first], lines[it.last], w.first, w.last)
		}
	}
}

func TestTextIsTheEntryAsWrittenStandingOnItsOwn(t *testing.T) {
	f, _ := spansOf(t, handWritten)
	got := f.Text(f.Items[1])
	want := "# heading for second\nname: second\n# why 09:30\ncron: \"30 9 * * 1\"\nrepo: /x\nworkflow: w\n"
	if got != want {
		t.Errorf("Text(second) =\n%s\nwant\n%s", got, want)
	}
	// What Text hands out must load as the same entry.
	back, err := parse([]byte("automations:\n  - " + strings.ReplaceAll(strings.TrimSuffix(got, "\n"), "\n", "\n    ") + "\n"))
	if err != nil || len(back.Automations) != 1 || back.Automations[0].Workflow != "w" {
		t.Fatalf("Text does not round-trip: %v %+v", err, back)
	}
	if got := f.Text(f.Items[0]); !strings.Contains(got, "  # not a comment: part of the prompt\n\n  Then stop.\n") {
		t.Errorf("block scalar lost its shape:\n%s", got)
	}
}

func TestALastEntryWithAnIndentedCommentKeepsGoingPastIt(t *testing.T) {
	f, err := parse([]byte("automations:\n- name: a\n# a note at the dash\n  cron: \"@daily\"\n  repo: /x\n  prompt: p\nother: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	lines := texts(f.Raw)
	if it := f.Items[0]; lines[it.last] != "  prompt: p" {
		t.Errorf("last line = %q, want the prompt line", lines[it.last])
	}
}

// texts is raw's lines, as yaml.v3 counts them, without their breaks.
func texts(raw []byte) []string {
	var out []string
	for _, l := range splitLines(raw) {
		out = append(out, l.text)
	}
	return out
}
