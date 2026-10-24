// Package wizard implements `herdr-automations add`: a plain stdin Q&A that
// appends a validated entry to automations.yaml and previews the next runs.
// The entry is added the way every other write works (config.Change): the
// file read afresh under the lock, its own lines appended, every other byte
// kept, so a broken entry elsewhere no longer stands in the way.
package wizard

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/schedule"
)

func Run() error {
	in := bufio.NewReader(os.Stdin)
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ask(in, "Name (kebab-case)", "")
	if name == "" {
		return fmt.Errorf("a name is required")
	}
	if cfg.Declares(name) {
		// Declares, not Find: an existing entry that is currently broken is
		// still an existing entry, and writing a second one by the same name
		// makes both invalid.
		return fmt.Errorf("automation %q already exists", name)
	}

	var cronExpr string
	for {
		cronExpr = ask(in, "Cron (5 fields or @daily/@hourly)", "0 9 * * 1-5")
		sched, err := config.ParseCron(cronExpr)
		if err != nil {
			fmt.Printf("  invalid: %v\n", err)
			continue
		}
		next := time.Now()
		fmt.Println("  next runs:")
		for i := 0; i < 3; i++ {
			next = sched.Next(next)
			fmt.Printf("    %s\n", next.Format("Mon 02 Jan 15:04"))
		}
		// Herdr runs every due automation; the scheduler never holds one back.
		// Saying so here is the only moment the cron is still up for debate.
		if clash := schedule.CollidesWith(cfg, cronExpr, time.Now()); len(clash) > 0 {
			fmt.Printf("  heads up: %s already due at the same time — they will all run at once\n",
				strings.Join(clash, ", "))
		}
		break
	}

	cwd, _ := os.Getwd()
	repo := ask(in, "Repo path", cwd)
	workspace := ask(in, "Workspace [worktree|root|existing]", "worktree")
	workspaceID := ""
	if config.Workspace(workspace) == config.WorkspaceExisting {
		workspaceID = ask(in, "Workspace ID (from herdr workspace list)", "")
		if workspaceID == "" {
			return fmt.Errorf("a workspace ID is required for existing mode")
		}
	}
	agent := ask(in, "Agent kind", "claude")
	// Asked every time rather than defaulted in the file: an automation that
	// does not say which model it uses is one that quietly picks whatever the
	// agent picks, which is how a nightly chore ends up on a flagship model.
	model := ""
	if config.KindAcceptsModel(agent) {
		model = ask(in, "Model (e.g. sonnet, opus — empty for the agent's own default)", "")
	}

	fmt.Print("Prompt (single line; leave empty to delegate to a herdr-workflows workflow):\n> ")
	prompt, _ := in.ReadString('\n')
	prompt = strings.TrimSpace(prompt)
	workflow := ""
	if prompt == "" {
		workflow = ask(in, "Workflow name (hwf run <name>)", "")
		if workflow == "" {
			return fmt.Errorf("either a prompt or a workflow is required")
		}
	}

	mcp := ask(in, "MCP config JSON path (optional)", "")
	timeoutStr := ask(in, "Timeout minutes", "60")
	timeout, err := strconv.Atoi(timeoutStr)
	if err != nil || timeout <= 0 {
		timeout = 60
	}

	if err := add(config.Automation{
		Name: name, Cron: cronExpr, Repo: repo,
		Workspace: config.Workspace(workspace), WorkspaceID: workspaceID,
		Agent: agent, Model: model,
		Prompt: prompt, Workflow: workflow,
		MCPConfig: mcp, TimeoutMinutes: timeout,
	}); err != nil {
		return err
	}
	fmt.Printf("\nSaved %q to %s\nThe daemon picks it up within 30s. Test it now with: herdr-automations run %s\n",
		name, config.Path(), name)
	return nil
}

// add appends a as a new entry. The file is read again under the lock, not
// taken from before the questions: another writer may have saved meanwhile,
// and writing the old copy back would undo that. The entry must load, or
// nothing is written.
func add(a config.Automation) error {
	pairs := []config.Pair{field("name", a.Name), field("cron", a.Cron), field("repo", a.Repo),
		field("workspace", string(a.Workspace))}
	optional := []config.Pair{field("workspace_id", a.WorkspaceID), field("agent", a.Agent),
		field("model", a.Model), field("prompt", a.Prompt), field("workflow", a.Workflow), field("mcp_config", a.MCPConfig)}
	for _, p := range optional {
		if p.Value.Value != "" {
			pairs = append(pairs, p)
		}
	}
	pairs = append(pairs, config.Pair{Key: "timeout_minutes",
		Value: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(a.TimeoutMinutes)}})
	text, err := config.NewEntryText(pairs)
	if err != nil {
		return err
	}
	want := map[string]any{}
	if err := yaml.Unmarshal([]byte(text), &want); err != nil {
		return err
	}
	_, _, err = config.Change("", false, func(f *config.File) (config.Edit, error) {
		// Declares, not Find: an existing entry that is currently broken is
		// still an existing entry, and a second one by the same name makes
		// both invalid.
		if f.Declares(a.Name) {
			return config.Edit{}, fmt.Errorf("automation %q already exists", a.Name)
		}
		raw, err := f.Append(text)
		return config.Edit{Raw: raw, Touched: []string{a.Name}, MustLoad: []string{a.Name}, Entry: a.Name, Want: want}, err
	})
	return err
}

func field(key, value string) config.Pair {
	return config.Pair{Key: key, Value: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}}
}

func ask(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}
