package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DnzzL/herdr-automations/internal/history"
)

// sandbox points config and state at temp dirs and writes the config.
func sandbox(t *testing.T, yaml string) {
	t.Helper()
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(cfg, "automations.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// emitted runs Emit and decodes what it printed, which must be one object.
func emitted(t *testing.T, res Result, err error) (map[string]any, bool) {
	t.Helper()
	var buf bytes.Buffer
	ok := Emit(&buf, res, err)
	if strings.Count(strings.TrimSpace(buf.String()), "\n") != 0 {
		t.Fatalf("not one line of JSON:\n%s", buf.String())
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	return out, ok
}

func keys(m any) []string {
	var out []string
	for k := range m.(map[string]any) {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func wantKeys(t *testing.T, what string, m any, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := keys(m); !slices.Equal(got, want) {
		t.Errorf("%s keys = %v\nwant %v", what, got, want)
	}
}

var secondPrecision = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(Z|[+-]\d\d:\d\d)$`)

const twoAndABrokenOne = `# kept by hand
automations:
  - name: "[KAN] Mails -> Kaneo"
    cron: "10 * * * *"
    repo: ~/x
    prompt: p
  - name: paused
    cron: "CRON_TZ=Europe/Vienna 30 9 * * 1"
    repo: /x
    workflow: w
    disabled: true
  - name: broken
    cron: "0 99 * * *"
    repo: /x
    prompt: p
`

// The app types every response against the contract, so the keys are the
// contract: all of them, every time, null rather than missing.
func TestListCarriesEveryContractKey(t *testing.T) {
	sandbox(t, twoAndABrokenOne)
	started := time.Now().Add(-time.Hour)
	id := history.NewID("[KAN] Mails -> Kaneo", "")
	for _, st := range []history.Status{history.StatusScheduled, history.StatusDone} {
		if err := history.Append(history.Record{RunID: id, Automation: "[KAN] Mails -> Kaneo", Status: st, At: started, Trigger: history.TriggerCron}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := List(time.Now())
	out, ok := emitted(t, res, err)
	if !ok || out["ok"] != true || out["schema"] != float64(1) {
		t.Fatalf("not a success: %v", out)
	}
	wantKeys(t, "list", out, "schema", "version", "ok", "machine", "config", "daemon", "automations", "invalid")
	wantKeys(t, "machine", out["machine"], "hostname", "tz", "now")
	wantKeys(t, "config", out["config"], "path", "exists", "sha256")
	wantKeys(t, "daemon", out["daemon"], "running", "pid", "since")

	autos := out["automations"].([]any)
	if len(autos) != 2 {
		t.Fatalf("automations = %d, want 2", len(autos))
	}
	kan, paused := autos[0].(map[string]any), autos[1].(map[string]any)
	wantKeys(t, "automation", kan, "name", "line", "enabled", "cron_tz", "next_run", "entry", "effective", "last_run")
	wantKeys(t, "effective", kan["effective"], "name", "cron", "repo", "workspace", "workspace_id", "agent", "model",
		"prompt", "workflow", "mcp_config", "agent_args", "timeout_minutes", "catch_up_minutes", "close_when_no_action", "disabled")
	// entry is the file as written: ~ kept, no defaults.
	wantKeys(t, "entry", kan["entry"], "name", "cron", "repo", "prompt")
	if repo := kan["entry"].(map[string]any)["repo"]; repo != "~/x" {
		t.Errorf("entry.repo = %v, want ~/x as written", repo)
	}
	if repo := kan["effective"].(map[string]any)["repo"].(string); strings.HasPrefix(repo, "~") {
		t.Errorf("effective.repo = %v, want it expanded", repo)
	}
	run := kan["last_run"].(map[string]any)
	wantKeys(t, "run", run, "run_id", "automation", "status", "trigger", "started_at", "updated_at", "workspace_id",
		"pane_id", "user_action_required", "summary", "error", "report_path", "tab_closed", "agent", "model",
		"workflow", "session_id", "session_source", "cwd", "has_output")
	if run["status"] != "done" || run["error"] != nil {
		t.Errorf("last_run = %v", run)
	}
	for _, at := range []any{run["started_at"], run["updated_at"], kan["next_run"], out["machine"].(map[string]any)["now"]} {
		if s, _ := at.(string); !secondPrecision.MatchString(s) {
			t.Errorf("time %v is not RFC3339 at second precision", at)
		}
	}

	if paused["enabled"] != false || paused["next_run"] != nil || paused["cron_tz"] != "Europe/Vienna" || paused["last_run"] != nil {
		t.Errorf("paused = %v", paused)
	}

	invalid := out["invalid"].([]any)
	if len(invalid) != 1 {
		t.Fatalf("invalid = %v", invalid)
	}
	bad := invalid[0].(map[string]any)
	wantKeys(t, "invalid", bad, "name", "line", "message", "yaml")
	if bad["name"] != "broken" || bad["line"] != float64(12) || !strings.HasPrefix(bad["yaml"].(string), "name: broken\ncron: \"0 99 * * *\"\n") {
		t.Errorf("invalid entry = %v", bad)
	}
}

func TestAMachineWithoutAConfigListsNothing(t *testing.T) {
	sandbox(t, "")
	res, err := List(time.Now())
	out, ok := emitted(t, res, err)
	cfg := out["config"].(map[string]any)
	if !ok || cfg["exists"] != false || cfg["sha256"] != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" ||
		len(out["automations"].([]any)) != 0 || len(out["invalid"].([]any)) != 0 {
		t.Errorf("empty machine = %v", out)
	}
}

// A failure is the same single object on stdout, with ok false and the error
// filled in; anything that is not a Failure is reported as internal.
func TestFailuresAreOneObjectToo(t *testing.T) {
	out, ok := emitted(t, nil, Fail(CodeNotFound, "no automation named %q", "x"))
	if ok || out["ok"] != false {
		t.Fatalf("failure reported as success: %v", out)
	}
	wantKeys(t, "failure", out, "schema", "version", "ok", "error")
	e := out["error"].(map[string]any)
	wantKeys(t, "error", e, "code", "message", "diagnostics", "checks", "current_sha256")
	if e["code"] != "not_found" || e["current_sha256"] != nil || len(e["diagnostics"].([]any)) != 0 {
		t.Errorf("error = %v", e)
	}

	out, _ = emitted(t, nil, errors.New("disk on fire"))
	if e := out["error"].(map[string]any); e["code"] != "internal" || e["message"] != "disk on fire" {
		t.Errorf("plain error = %v", e)
	}
}

func TestHistoryOfOneRunIsItsTimeline(t *testing.T) {
	sandbox(t, "")
	id := history.NewID("a", "")
	for _, st := range []history.Status{history.StatusScheduled, history.StatusRunning, history.StatusFailed} {
		if err := history.Append(history.Record{RunID: id, Automation: "a", Status: st, At: time.Now(), Error: "boom"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := RunTimeline(id)
	out, ok := emitted(t, res, err)
	if !ok || out["run"].(map[string]any)["status"] != "failed" || len(out["records"].([]any)) != 3 {
		t.Fatalf("timeline = %v", out)
	}
	wantKeys(t, "record", out["records"].([]any)[0], "run_id", "automation", "status", "trigger", "at", "workspace_id",
		"pane_id", "user_action_required", "summary", "error", "report_path", "tab_closed", "agent", "model",
		"workflow", "session_id", "session_source", "cwd")

	_, err = RunTimeline(history.NewID("a", ""))
	if out, _ := emitted(t, nil, err); out["error"].(map[string]any)["code"] != "not_found" {
		t.Errorf("unknown run = %v", out)
	}
}
