package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// Review finding 7: add used to write back the config it read before its
// questions, undoing whatever another writer saved meanwhile and dropping
// every comment, and it refused outright while any entry was broken. Now it
// appends its entry's lines to the file as it is when it writes.
func TestAddAppendsToTheFileAsItIsNow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	// What another writer saved while the questions were being answered: a
	// comment, and a broken entry that must stay as it is.
	now := "# kept by hand\nautomations:\n  - name: theirs\n    cron: \"@daily\"\n    repo: /x\n    prompt: p\n" +
		"  - name: broken\n    cron: \"0 99 * * *\"\n    repo: /x\n    prompt: p\n"
	if err := os.WriteFile(filepath.Join(dir, "automations.yaml"), []byte(now), 0o644); err != nil {
		t.Fatal(err)
	}

	err := add(config.Automation{Name: "mine", Cron: "@hourly", Repo: "/y", Workspace: config.WorkspaceWorktree,
		Agent: "claude", Model: "sonnet", Prompt: "two\nlines\n", TimeoutMinutes: 30})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "automations.yaml"))
	if !strings.HasPrefix(string(raw), now) {
		t.Errorf("the file as it was is not kept:\n%s", raw)
	}
	f, _ := config.Read()
	if m := f.Find("mine"); m == nil || m.Model != "sonnet" || m.Prompt != "two\nlines\n" || m.TimeoutMinutes != 30 {
		t.Errorf("mine = %+v", m)
	}
	if f.Diagnostic("broken") == nil || f.Find("theirs") == nil {
		t.Error("another entry changed")
	}
	if err := add(config.Automation{Name: "broken", Cron: "@daily", Repo: "/y", Workspace: config.WorkspaceWorktree,
		Agent: "claude", Prompt: "p", TimeoutMinutes: 60}); err == nil {
		t.Error("a name taken by a broken entry was added twice")
	}
}
