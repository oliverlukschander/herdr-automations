package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteThenRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	no := false
	if err := Write(path, Report{Action: &no, Summary: "nothing new"}); err != nil {
		t.Fatal(err)
	}
	r, err := Read(path)
	if err != nil || r.Action == nil || *r.Action || r.Summary != "nothing new" {
		t.Fatalf("got %+v, %v", r, err)
	}
}

// Only a whole report decides anything: the file NewPath leaves empty, or one
// without a yes or no, must never let a run be closed.
func TestReadRefusesAnythingButAWholeReport(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty":     "",
		"no action": `{"summary":"done"}`,
		"no text":   `{"user_action_required":false,"summary":" "}`,
		"torn":      `{"user_action_required":fa`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil {
			t.Errorf("%s: read as a report", name)
		}
	}
}

func TestAskNamesThePathQuoted(t *testing.T) {
	if got := Ask("/state/it's.json"); !strings.Contains(got, `--file '/state/it'\''s.json'`) {
		t.Fatalf("got %s", got)
	}
}
