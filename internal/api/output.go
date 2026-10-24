package api

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/output"
)

// OutputResult is `output <run_id> --json`: where a run's output is to be
// found. The report is in Run already.
type OutputResult struct {
	Head
	Run        Run         `json:"run"`
	Transcript *Transcript `json:"transcript"`
	Pane       *Pane       `json:"pane"`
}

// Transcript points at the agent's own session log, which the app reads with
// the same reader it uses for any chat log.
type Transcript struct {
	Agent     string  `json:"agent"`
	Format    string  `json:"format"`
	SessionID string  `json:"session_id"`
	Path      *string `json:"path"`
}

// Pane is the run's pane, kept when the run ended.
type Pane struct {
	Path       string `json:"path"`
	CapturedAt Time   `json:"captured_at"`
	LineCount  int    `json:"line_count"`
	Text       string `json:"text"`
}

// Output answers `output <run_id> --json`. A run recorded before sessions and
// captures existed has neither, which is still a success.
func Output(runID string) (*OutputResult, error) {
	recs, err := history.Timeline(runID)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, Fail(CodeNotFound, "no run %q in the history", runID)
	}
	last := recs[len(recs)-1]
	res := &OutputResult{Run: runOf(last)}
	if last.SessionID != "" {
		res.Transcript = &Transcript{
			Agent: last.Agent, Format: last.Agent, SessionID: last.SessionID,
			Path: str(transcriptPath(last.Agent, last.SessionID)),
		}
	}
	if c, ok := output.Read(runID); ok {
		res.Pane = &Pane{
			Path: c.Path, CapturedAt: Time{c.At}, Text: c.Text,
			LineCount: strings.Count(strings.TrimSuffix(c.Text, "\n"), "\n") + 1,
		}
	}
	return res, nil
}

// transcriptLayouts is where each agent keeps a session's log, as a glob in
// the home directory with %s for the session ID. Claude and Grok were checked
// on the Mac (2026-10-24); Codex's is its documented rollout layout.
var transcriptLayouts = map[string]string{
	"claude": ".claude/projects/*/%s.jsonl",
	"grok":   ".grok/sessions/*/%s/chat_history.jsonl",
	"codex":  ".codex/sessions/*/*/*/rollout-*-%s.jsonl",
}

// transcriptPath finds the session's log, or "" when the agent kind has no
// known layout or the file is not there.
func transcriptPath(agent, sessionID string) string {
	layout, ok := transcriptLayouts[agent]
	if !ok || sessionID == "" || strings.ContainsAny(sessionID, `/*?[\`) {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, strings.Replace(layout, "%s", sessionID, 1)))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}
