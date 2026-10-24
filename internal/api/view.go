package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/schedule"
)

// Automation is one entry that loaded, as the app shows it.
type Automation struct {
	Name    string  `json:"name"`
	Line    int     `json:"line"`
	Enabled bool    `json:"enabled"`
	CronTZ  *string `json:"cron_tz"`
	NextRun Time    `json:"next_run"`
	// Entry is the entry as written: what the app edits and sends back, so
	// that saving it neither bakes the defaults in nor turns ~ into a path
	// that only exists on this machine.
	Entry     Entry     `json:"entry"`
	Effective Effective `json:"effective"`
	LastRun   *Run      `json:"last_run"`
}

// Entry holds only the keys the file sets.
type Entry struct {
	CloseWhenNoAction *bool     `json:"close_when_no_action,omitempty" yaml:"close_when_no_action"`
	Name              *string   `json:"name,omitempty" yaml:"name"`
	Cron              *string   `json:"cron,omitempty" yaml:"cron"`
	Repo              *string   `json:"repo,omitempty" yaml:"repo"`
	Workspace         *string   `json:"workspace,omitempty" yaml:"workspace"`
	WorkspaceID       *string   `json:"workspace_id,omitempty" yaml:"workspace_id"`
	Agent             *string   `json:"agent,omitempty" yaml:"agent"`
	Model             *string   `json:"model,omitempty" yaml:"model"`
	Prompt            *string   `json:"prompt,omitempty" yaml:"prompt"`
	Workflow          *string   `json:"workflow,omitempty" yaml:"workflow"`
	MCPConfig         *string   `json:"mcp_config,omitempty" yaml:"mcp_config"`
	AgentArgs         *[]string `json:"agent_args,omitempty" yaml:"agent_args"`
	TimeoutMinutes    *int      `json:"timeout_minutes,omitempty" yaml:"timeout_minutes"`
	CatchUpMinutes    *int      `json:"catch_up_minutes,omitempty" yaml:"catch_up_minutes"`
	Disabled          *bool     `json:"disabled,omitempty" yaml:"disabled"`
}

// Effective is every key with the defaults Load applies.
type Effective struct {
	Name              string   `json:"name"`
	Cron              string   `json:"cron"`
	Repo              string   `json:"repo"`
	Workspace         string   `json:"workspace"`
	WorkspaceID       *string  `json:"workspace_id"`
	Agent             string   `json:"agent"`
	Model             *string  `json:"model"`
	Prompt            *string  `json:"prompt"`
	Workflow          *string  `json:"workflow"`
	MCPConfig         *string  `json:"mcp_config"`
	AgentArgs         []string `json:"agent_args"`
	TimeoutMinutes    int      `json:"timeout_minutes"`
	CatchUpMinutes    int      `json:"catch_up_minutes"`
	CloseWhenNoAction bool     `json:"close_when_no_action"`
	Disabled          bool     `json:"disabled"`
}

// Invalid is an entry that did not load, with its text so it can be fixed.
type Invalid struct {
	Diagnostic
	YAML string `json:"yaml"`
}

// runFields are what a run and each of its records share.
type runFields struct {
	WorkspaceID        *string `json:"workspace_id"`
	PaneID             *string `json:"pane_id"`
	UserActionRequired *bool   `json:"user_action_required"`
	Summary            *string `json:"summary"`
	Error              *string `json:"error"`
	ReportPath         *string `json:"report_path"`
	TabClosed          bool    `json:"tab_closed"`
	Agent              *string `json:"agent"`
	Model              *string `json:"model"`
	Workflow           *string `json:"workflow"`
	SessionID          *string `json:"session_id"`
	SessionSource      *string `json:"session_source"`
	CWD                *string `json:"cwd"`
}

// Run is the latest state of one run.
type Run struct {
	RunID      string  `json:"run_id"`
	Automation string  `json:"automation"`
	Status     string  `json:"status"`
	Trigger    *string `json:"trigger"`
	StartedAt  Time    `json:"started_at"`
	UpdatedAt  Time    `json:"updated_at"`
	runFields
	HasOutput bool `json:"has_output"`
}

// Record is one line of the history log.
type Record struct {
	RunID      string  `json:"run_id"`
	Automation string  `json:"automation"`
	Status     string  `json:"status"`
	Trigger    *string `json:"trigger"`
	At         Time    `json:"at"`
	runFields
}

func fieldsOf(r history.Record) runFields {
	return runFields{
		WorkspaceID:        str(r.WorkspaceID),
		PaneID:             str(r.PaneID),
		UserActionRequired: r.UserActionRequired,
		Summary:            str(r.Summary),
		Error:              str(r.Error),
		ReportPath:         str(r.ReportPath),
		TabClosed:          r.TabClosed,
	}
}

// runOf is the view of a run's latest record. A run whose process died under
// it is shown as interrupted right away; the daemon only writes that down
// the next time it starts.
func runOf(r history.Record) Run {
	started, _ := history.StartedAt(r.RunID)
	run := Run{
		RunID: r.RunID, Automation: r.Automation, Status: string(r.Status),
		Trigger: str(string(r.Trigger)), StartedAt: Time{started}, UpdatedAt: Time{r.At},
		runFields: fieldsOf(r),
	}
	if history.Orphaned(r) {
		required := true
		run.Status = string(history.StatusInterrupted)
		run.Error = str(fmt.Sprintf("the process running it (pid %d) exited before it finished", r.RunnerPID))
		run.Summary, run.UserActionRequired = run.Error, &required
	}
	return run
}

func recordOf(r history.Record) Record {
	return Record{
		RunID: r.RunID, Automation: r.Automation, Status: string(r.Status),
		Trigger: str(string(r.Trigger)), At: Time{r.At}, runFields: fieldsOf(r),
	}
}

// automationOf assembles the view of a loaded entry. last may be nil.
func automationOf(f *config.File, a config.Automation, last *history.Record, now time.Time) (Automation, error) {
	v := Automation{
		Name: a.Name, Line: a.Line, Enabled: !a.Disabled, CronTZ: cronTZ(a.Cron),
		Effective: effectiveOf(a),
	}
	if !a.Disabled {
		if next, ok := schedule.NextRun(a, now); ok {
			v.NextRun = Time{next}
		}
	}
	if it := f.ItemAt(a.Line); it != nil {
		if err := it.Decode(&v.Entry); err != nil {
			return v, err
		}
	}
	if last != nil {
		run := runOf(*last)
		v.LastRun = &run
	}
	return v, nil
}

func effectiveOf(a config.Automation) Effective {
	args := a.AgentArgs
	if args == nil {
		args = []string{}
	}
	return Effective{
		Name: a.Name, Cron: a.Cron, Repo: a.Repo, Workspace: string(a.Workspace),
		WorkspaceID: str(a.WorkspaceID), Agent: a.Agent, Model: str(a.Model),
		Prompt: str(a.Prompt), Workflow: str(a.Workflow), MCPConfig: str(a.MCPConfig),
		AgentArgs: args, TimeoutMinutes: a.TimeoutMinutes, CatchUpMinutes: a.CatchUpMinutes,
		CloseWhenNoAction: a.CloseWhenNoAction, Disabled: a.Disabled,
	}
}

// cronTZ is the zone a cron pins itself to, or nil when it follows the
// machine's. The parser accepts both spellings.
func cronTZ(expr string) *string {
	for _, prefix := range []string{"CRON_TZ=", "TZ="} {
		if rest, ok := strings.CutPrefix(expr, prefix); ok {
			zone, _, _ := strings.Cut(rest, " ")
			return str(zone)
		}
	}
	return nil
}

func invalidOf(f *config.File, d config.Diagnostic) Invalid {
	v := Invalid{Diagnostic: Diagnostic{Name: str(d.Name), Line: d.Line, Message: d.Message}}
	if it := f.ItemAt(d.Line); it != nil {
		v.YAML = f.Text(*it)
	}
	return v
}
