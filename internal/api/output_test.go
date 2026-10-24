package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/output"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOutputPointsAtTheTranscriptAndHandsOverThePane(t *testing.T) {
	sandbox(t, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	touch(t, filepath.Join(home, ".claude/projects/-Users-x-repo/11111111-2222-3333-4444-555555555555.jsonl"))
	touch(t, filepath.Join(home, ".grok/sessions/%2FUsers%2Fx%2Frepo/01a16cd1-4869-7940-8d30-d0615adf181f/chat_history.jsonl"))

	runs := map[string]history.Record{
		"claude": {Agent: "claude", SessionID: "11111111-2222-3333-4444-555555555555", SessionSource: "herdr:claude"},
		"grok":   {Agent: "grok", SessionID: "01a16cd1-4869-7940-8d30-d0615adf181f", SessionSource: "herdr:grok"},
		"gemini": {Agent: "gemini", SessionID: "abc"},
		"legacy": {Agent: ""},
	}
	ids := map[string]string{}
	for kind, r := range runs {
		r.RunID, r.Automation, r.Status, r.At = history.NewID(kind, ""), kind, history.StatusDone, time.Now()
		ids[kind] = r.RunID
		history.Append(r)
	}
	output.Save(ids["claude"], "line 1\nline 2\nline 3\n")

	res, err := Output(ids["claude"])
	if err != nil {
		t.Fatal(err)
	}
	if tr := res.Transcript; tr == nil || tr.Format != "claude" || tr.Path == nil || filepath.Base(*tr.Path) != "11111111-2222-3333-4444-555555555555.jsonl" {
		t.Errorf("claude transcript = %+v", tr)
	}
	if p := res.Pane; p == nil || p.LineCount != 3 || p.Text != "line 1\nline 2\nline 3\n" || !res.Run.HasOutput {
		t.Errorf("pane = %+v, has_output %v", p, res.Run.HasOutput)
	}

	if res, _ := Output(ids["grok"]); res.Transcript == nil || res.Transcript.Path == nil || filepath.Base(*res.Transcript.Path) != "chat_history.jsonl" {
		t.Errorf("grok transcript = %+v", res.Transcript)
	}
	if res, _ := Output(ids["gemini"]); res.Transcript == nil || res.Transcript.Path != nil {
		t.Errorf("an agent with no known layout: %+v", res.Transcript)
	}
	if res, err := Output(ids["legacy"]); err != nil || res.Transcript != nil || res.Pane != nil {
		t.Errorf("a run from before sessions: %+v %v", res, err)
	}
	if _, err := Output(history.NewID("nope", "")); code(err) != CodeNotFound {
		t.Errorf("unknown run: %v", err)
	}
}
