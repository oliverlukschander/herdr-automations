package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/cleanup"
	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/herdr"
	"github.com/DnzzL/herdr-automations/internal/schedule"
)

// checker answers what a machine has to say yes to before it can run an
// automation: `apply --dry-run` on the target of a move, and the guard of
// `--require-runnable`. Each question is a field so the tests can answer for
// a machine that is not this one.
type checker struct {
	// find locates an executable the way the agent's pane will.
	find      func(bin string) (string, error)
	isGit     func(dir string) bool
	workflow  func(repo, name string) error
	workspace func(id string) (bool, error)
	zone      func() string
}

var machineChecks = checker{
	find: findExecutable,
	isGit: func(dir string) bool {
		return cleanup.GitCommand(dir, "rev-parse", "--is-inside-work-tree").Run() == nil
	},
	workflow:  inspectWorkflow,
	workspace: herdr.Client{}.WorkspaceExists,
	zone:      zoneName,
}

func runChecks(a config.Automation, cfg *config.Config, oldName string, now time.Time) []Check {
	return machineChecks.run(a, cfg, oldName, now)
}

func (c checker) run(a config.Automation, cfg *config.Config, oldName string, now time.Time) []Check {
	var out []Check
	add := func(id, level string, ok bool, format string, args ...any) {
		out = append(out, Check{ID: id, OK: ok, Level: level, Message: fmt.Sprintf(format, args...)})
	}

	repoOK := isDir(a.Repo)
	if repoOK {
		add("repo_exists", "error", true, "%s exists", a.Repo)
	} else {
		add("repo_exists", "error", false, "%s does not exist on this machine", a.Repo)
	}
	if a.Workspace == config.WorkspaceWorktree {
		if repoOK && c.isGit(a.Repo) {
			add("repo_is_git", "error", true, "%s is a git repository", a.Repo)
		} else {
			add("repo_is_git", "error", false, "workspace: worktree needs a git repository at %s", a.Repo)
		}
	}

	if a.Workflow == "" {
		bin := agentBinary(a.Agent)
		if path, err := c.find(bin); err == nil {
			add("agent_installed", "error", true, "%s at %s", bin, path)
		} else {
			add("agent_installed", "error", false, "%s (agent %s) is not on this machine's shell PATH", bin, a.Agent)
		}
	} else {
		path, err := c.find("hwf")
		if err == nil {
			add("hwf_installed", "error", true, "hwf at %s", path)
		} else {
			add("hwf_installed", "error", false, "hwf (herdr-workflows) is not on this machine's shell PATH")
		}
		if err == nil && repoOK {
			if err := c.workflow(a.Repo, a.Workflow); err == nil {
				add("workflow_exists", "error", true, "workflow %s found", a.Workflow)
			} else {
				add("workflow_exists", "error", false, "workflow %s: %v", a.Workflow, err)
			}
		}
	}

	if a.MCPConfig != "" {
		if _, err := os.Stat(a.MCPConfig); err == nil {
			add("mcp_config_exists", "error", true, "%s exists", a.MCPConfig)
		} else {
			add("mcp_config_exists", "error", false, "%s does not exist on this machine", a.MCPConfig)
		}
	}

	if a.Workspace == config.WorkspaceExisting {
		switch open, err := c.workspace(a.WorkspaceID); {
		case err != nil:
			add("workspace_exists", "error", false, "could not ask Herdr about workspace %s: %v", a.WorkspaceID, err)
		case open:
			add("workspace_exists", "error", true, "workspace %s is open", a.WorkspaceID)
		default:
			add("workspace_exists", "error", false, "workspace %s is not open in this Herdr", a.WorkspaceID)
		}
	}

	if tz := cronTZ(a.Cron); tz != nil {
		add("cron_timezone", "warning", true, "pinned to %s", *tz)
	} else {
		zone := c.zone()
		if zone == "" {
			zone = "unknown"
		}
		add("cron_timezone", "warning", false, "no CRON_TZ: runs on this machine's zone (%s)", zone)
	}

	others := &config.Config{}
	for _, x := range cfg.Automations {
		if x.Name != a.Name && x.Name != oldName {
			others.Automations = append(others.Automations, x)
		}
	}
	if clash := schedule.CollidesWith(others, a.Cron, now); len(clash) > 0 {
		add("collisions", "warning", false, "due at the same minute as %s within 7 days", strings.Join(clash, ", "))
	} else {
		add("collisions", "warning", true, "no other automation due at the same minute")
	}
	return out
}

// runnable reports whether no error-level check failed.
func runnable(checks []Check) bool {
	return failing(checks) == ""
}

// failing names the error-level checks that failed.
func failing(checks []Check) string {
	var ids []string
	for _, c := range checks {
		if !c.OK && c.Level == "error" {
			ids = append(ids, c.ID)
		}
	}
	return strings.Join(ids, ", ")
}

// agentBinary is the executable behind an agent kind, where the two differ.
func agentBinary(kind string) string {
	if kind == "cursor" {
		return "cursor-agent"
	}
	return kind
}

// findExecutable looks for bin the way the agent's pane will. Herdr types the
// agent command into an interactive shell, so the user's rc files decide PATH
// (grok lives in ~/.grok/bin, added only in .zshrc), not this process's.
// This PATH is tried first; scripts/run.sh already adds the usual user dirs.
func findExecutable(bin string) (string, error) {
	if path, err := exec.LookPath(bin); err == nil {
		return path, nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, shell, "-l", "-i", "-c", "command -v "+shellQuote(bin)).Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "/") {
			return line, nil
		}
	}
	return "", fmt.Errorf("%s not found", bin)
}

// inspectWorkflow asks herdr-workflows whether it knows the workflow, from the
// repo, where hwf finds a repo's own .hwf/workflows.
func inspectWorkflow(repo, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hwf", "workflow", "inspect", name)
	cmd.Dir = repo
	if path, err := findExecutable("hwf"); err == nil {
		cmd.Path = path
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s", strings.SplitN(msg, "\n", 2)[0])
		}
		return err
	}
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
