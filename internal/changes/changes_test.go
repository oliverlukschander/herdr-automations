package changes

import (
	"reflect"
	"testing"

	"github.com/DnzzL/herdr-automations/internal/config"
)

func TestOfListsAnAutomationsChangesNewestFirstThroughARename(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HOME", "/home/me")
	made := config.Automation{Name: "a", Cron: "@daily", Repo: "~/x", Prompt: "p"}
	// The form writes the defaults out and ~ in full: that is no change.
	saved := config.Automation{Name: "a", Cron: "@hourly", Repo: "/home/me/x", Prompt: "p", Agent: "claude",
		Workspace: config.WorkspaceWorktree, TimeoutMinutes: 60, CatchUpMinutes: 120}
	renamed := saved
	renamed.Name, renamed.Account = "b", "it@golf.at"
	other := config.Automation{Name: "c", Cron: "@daily", Repo: "/x", Prompt: "q"}
	for _, c := range []struct {
		op            string
		before, after *config.Automation
	}{
		{"put", nil, &made}, {"put", &made, &saved}, {"put", nil, &other}, {"put", &saved, &renamed},
		{"rm", &other, nil},
	} {
		if err := Record(c.op, c.before, c.after); err != nil {
			t.Fatal(err)
		}
	}

	list, err := Of("b")
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, c := range list {
		got = append(got, append([]string{c.Op}, c.Fields...))
	}
	want := [][]string{{"put", "account", "name"}, {"put", "cron"}, {"put"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if list[2].Before != nil || list[2].After.Repo != "~/x" {
		t.Fatalf("the first change is the entry as written: %+v", list[2])
	}
	if list, _ := Of("c"); len(list) != 2 || list[0].After != nil {
		t.Fatalf("c: %+v", list)
	}
}
