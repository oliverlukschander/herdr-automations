// herdr-automations — the trigger layer for Herdr agents: cron-scheduled
// prompts (or herdr-workflows delegations) launched in fresh worktrees.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/api"
	"github.com/DnzzL/herdr-automations/internal/cleanup"
	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/daemon"
	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/pane"
	"github.com/DnzzL/herdr-automations/internal/report"
	"github.com/DnzzL/herdr-automations/internal/runner"
	"github.com/DnzzL/herdr-automations/internal/schedule"
	"github.com/DnzzL/herdr-automations/internal/skill"
	"github.com/DnzzL/herdr-automations/internal/wizard"
)

// Version is stamped by the release build; "dev" for local builds.
var Version = "dev"

const usage = `herdr-automations — cron for your Herdr agents

Usage:
  herdr-automations daemon           Run the scheduler (started by the plugin startup hook)
  herdr-automations add              Interactive wizard: create an automation
  herdr-automations list [--json]    List automations with schedule and last run
  herdr-automations run <name>       Trigger an automation now
  herdr-automations pause <name>     Stop scheduling an automation (--if-match SHA, --json)
  herdr-automations resume <name>    Resume a paused automation (--if-match SHA, --json)
  herdr-automations show <name>      Print an automation as written in automations.yaml
  herdr-automations apply            Create or replace one automation from stdin (YAML or JSON)
                                     [--dry-run] [--new] [--rename-from OLD] [--patch JSON]
                                     [--require-runnable] [--if-match SHA] [--json]
  herdr-automations delete <name>    Remove an automation (--if-match SHA, --json)
  herdr-automations history [name] [--limit N] [--run ID] [--json]
                                     Show recent runs and action reports
  herdr-automations report --file PATH --action yes|no --summary TEXT
  herdr-automations cleanup          Remove run worktrees whose work already landed
  herdr-automations pane             Interactive board (used by the Herdr pane)
  herdr-automations install-skill    Teach your coding agent to write automations
  herdr-automations version          Print the version

Config:  %s
History: %s

--json prints one JSON object for the Orchestrator app (schema in internal/api).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Printf(usage, config.Path(), config.StateDir())
		os.Exit(2)
	}
	api.Version = Version
	var err error
	switch os.Args[1] {
	case "daemon":
		err = daemon.Run()
	case "add":
		err = wizard.Run()
	case "list":
		err = listCmd(os.Args[2:])
	case "cleanup":
		err = cleanupCmd(os.Args[2:])
	case "report":
		err = reportCmd(os.Args[2:])
	case "run":
		err = runCmd(os.Args[2:])
	case "pause":
		err = pauseCmd(os.Args[2:], true)
	case "resume":
		err = pauseCmd(os.Args[2:], false)
	case "show":
		err = showCmd(os.Args[2:])
	case "apply":
		err = applyCmd(os.Args[2:])
	case "delete":
		err = deleteCmd(os.Args[2:])
	case "history":
		err = historyCmd(os.Args[2:])
	case "pane":
		err = pane.Run()
	case "install-skill":
		target := ""
		if len(os.Args) > 2 {
			target = os.Args[2]
		}
		err = skill.Install(target)
	case "version", "--version", "-v":
		fmt.Println(Version)
	default:
		fmt.Printf(usage, config.Path(), config.StateDir())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newFlags is a flag set for a subcommand that may be asked for --json. JSON
// mode is read off the raw arguments rather than the parsed flag: a parse
// error that stops before reaching --json is still owed to the app as JSON on
// stdout, and the flag package must not print its own complaint first.
func newFlags(name string, args []string) (*flag.FlagSet, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Bool("json", false, "print one JSON object for the Orchestrator app")
	asJSON := slices.Contains(args, "--json") || slices.Contains(args, "-json")
	if asJSON {
		fs.SetOutput(io.Discard)
	}
	return fs, asJSON
}

// parseArgs lets flags and positional arguments come in any order. The app
// builds these command lines; Go's flag package would otherwise stop at the
// first name and read every flag after it as another name.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// emit prints a --json response and exits 1 on failure, with nothing on
// stderr: the app reads stdout only.
func emit(res api.Result, err error) {
	if !api.Emit(os.Stdout, res, err) {
		os.Exit(1)
	}
}

func usageError(err error) error { return api.Fail(api.CodeUsage, "%v", err) }

func listCmd(args []string) error {
	fs, asJSON := newFlags("list", args)
	rest, err := parseArgs(fs, args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("list takes no arguments")
	}
	if asJSON {
		if err != nil {
			emit(nil, usageError(err))
		}
		emit(api.List(time.Now()))
		return nil
	}
	if err != nil {
		return err
	}
	return list()
}

func list() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Automations) == 0 && len(cfg.Invalid) == 0 {
		fmt.Printf("No automations. Create one with `herdr-automations add` or edit %s\n", config.Path())
		return nil
	}
	if len(cfg.Automations) == 0 {
		printDiagnostics(cfg)
		return nil
	}
	fmt.Printf("%-24s %-16s %-9s %-8s %-13s %s\n",
		"NAME", "CRON", "WORKSPACE", "AGENT", "MODEL", "LAST RUN")
	latest, _ := history.Latest()
	for _, a := range cfg.Automations {
		last := "never"
		if r, ok := latest[a.Name]; ok {
			last = fmt.Sprintf("%s (%s)", r.Status, r.At.Format("02 Jan 15:04"))
		}
		name := a.Name
		if a.Disabled {
			name += " (disabled)"
		}
		// An unset model is not blank space — it is a decision left to the
		// agent, and worth seeing next to the ones you made yourself.
		model := a.Model
		if model == "" {
			model = "agent default"
		}
		fmt.Printf("%-24s %-16s %-9s %-8s %-13s %s\n",
			name, a.Cron, a.Workspace, a.Agent, model, last)
	}
	printDiagnostics(cfg)
	printCollisions(cfg)
	printWorktrees(cfg)
	return nil
}

// printDiagnostics names the entries that did not load and the line to open.
// They are not scheduled, so nothing above mentions them — and an automation
// missing from a list is exactly the thing you do not notice.
func printDiagnostics(cfg *config.Config) {
	if len(cfg.Invalid) == 0 {
		return
	}
	subject := fmt.Sprintf("%d entries", len(cfg.Invalid))
	if len(cfg.Invalid) == 1 {
		subject = "1 entry"
	}
	fmt.Printf("\n%s did not load and will not run:\n", subject)
	for _, d := range cfg.Invalid {
		fmt.Printf("  %s\n", d)
	}
}

// printWorktrees says what state the run worktrees are in, and only mentions
// the command when there is something for it to do. It counts and tells;
// removing them stays an explicit act.
func printWorktrees(cfg *config.Config) {
	plan, err := cleanup.Scan(cfg)
	if err != nil || plan.Empty() {
		return
	}
	line := "\n" + plan.Summary()
	if len(plan.Removable) > 0 {
		line += " — herdr-automations cleanup"
	}
	fmt.Println(line)
}

// printCollisions surfaces schedules that come due together. The scheduler
// runs them all — this is a report, not a warning about something the plugin
// is about to do differently.
func printCollisions(cfg *config.Config) {
	clashes := schedule.Collisions(cfg, time.Now())
	if len(clashes) == 0 {
		return
	}
	fmt.Println("\nRunning together (Herdr starts them all in parallel):")
	for _, c := range clashes {
		fmt.Printf("  %s  %s\n", c.At.Format("Mon 02 Jan 15:04"), strings.Join(c.Names, ", "))
	}
}

// cleanupCmd reports what every run worktree is worth keeping for, then asks
// once before removing the ones whose work already landed. It is never
// scheduled: an open workspace is how you know a run still wants you.
func cleanupCmd(args []string) error {
	dryRun, assumeYes := false, false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--yes", "-y":
			assumeYes = true
		default:
			return fmt.Errorf("unknown flag %q; cleanup takes --dry-run and --yes", a)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	plan, err := cleanup.Scan(cfg)
	if err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Println("No run worktrees.")
		return nil
	}

	for _, c := range plan.All() {
		mark := "keep"
		if c.Verdict == cleanup.Removable {
			mark = "remove"
		}
		fmt.Printf("%-7s %-48s %s\n", mark, c.Branch, c.Verdict)
	}
	if len(plan.Removable) == 0 {
		fmt.Println("\n" + plan.Summary())
		return nil
	}

	if dryRun {
		fmt.Printf("\n%d would be removed. Drop --dry-run to do it.\n", len(plan.Removable))
		return nil
	}
	if !assumeYes {
		fmt.Printf("\nRemove %d worktree(s) and their branches? [y/N] ", len(plan.Removable))
		var answer string
		if _, err := fmt.Scanln(&answer); err != nil || !strings.EqualFold(answer, "y") {
			fmt.Println("Nothing removed.")
			return nil
		}
	}
	if _, err := plan.Apply(func(c cleanup.Candidate) { fmt.Println("removed", c.Branch) }); err != nil {
		return fmt.Errorf("kept: %w", err)
	}
	return nil
}

func runCmd(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "--pick" {
		// Herdr action context: no arg — pick via a numbered prompt.
		if len(cfg.Automations) == 0 {
			return fmt.Errorf("no automations configured")
		}
		for i, a := range cfg.Automations {
			fmt.Printf("%d) %s (%s)\n", i+1, a.Name, a.Cron)
		}
		fmt.Print("Run which one? ")
		var n int
		if _, err := fmt.Scanln(&n); err != nil || n < 1 || n > len(cfg.Automations) {
			return fmt.Errorf("invalid selection")
		}
		return runner.Default().Run(cfg.Automations[n-1], history.TriggerManual)
	}
	a := cfg.Find(args[0])
	if a == nil {
		if d := cfg.Diagnostic(args[0]); d != nil {
			return fmt.Errorf("%s did not load: %s", args[0], d)
		}
		return fmt.Errorf("no automation named %q", args[0])
	}
	return runner.Default().Run(*a, history.TriggerManual)
}

// pauseCmd toggles Disabled on a single automation and persists it. It shares
// the config's own guardrail: Save refuses while any entry in the file failed
// to load, so a broken sibling entry blocks this too.
// oneName parses a subcommand that takes exactly one automation name.
func oneName(cmd string, args []string, fs *flag.FlagSet) (string, error) {
	rest, err := parseArgs(fs, args)
	if err == nil && len(rest) != 1 {
		err = fmt.Errorf("usage: herdr-automations %s <name>", cmd)
	}
	if err != nil {
		return "", err
	}
	return rest[0], nil
}

// pauseCmd changes only the entry's disabled: line, so a hand-written file
// keeps its comments. A broken sibling entry no longer blocks it: other
// entries come out exactly as they went in, or nothing is written.
func pauseCmd(args []string, disabled bool) error {
	cmd := map[bool]string{true: "pause", false: "resume"}[disabled]
	fs, asJSON := newFlags(cmd, args)
	ifMatch := fs.String("if-match", "", "refuse unless automations.yaml still has this sha256")
	name, err := oneName(cmd, args, fs)
	if asJSON {
		if err != nil {
			emit(nil, usageError(err))
		}
		emit(api.SetPaused(name, disabled, *ifMatch, time.Now()))
		return nil
	}
	if err != nil {
		return err
	}
	changed, _, _, err := config.SetDisabled(name, disabled, *ifMatch)
	if err != nil {
		return err
	}
	verb := map[bool]string{true: "Paused", false: "Resumed"}[disabled]
	if !changed {
		verb = "Already " + strings.ToLower(verb)
	}
	fmt.Printf("%s %s\n", verb, name)
	return nil
}

func showCmd(args []string) error {
	fs, asJSON := newFlags("show", args)
	name, err := oneName("show", args, fs)
	if asJSON {
		if err != nil {
			emit(nil, usageError(err))
		}
		emit(api.Show(name, time.Now()))
		return nil
	}
	if err != nil {
		return err
	}
	res, err := api.Show(name, time.Now())
	if err != nil {
		return err
	}
	fmt.Print(res.YAML)
	return nil
}

func applyCmd(args []string) error {
	fs, asJSON := newFlags("apply", args)
	var o api.ApplyOptions
	fs.BoolVar(&o.DryRun, "dry-run", false, "validate and run the checks, write nothing")
	fs.BoolVar(&o.New, "new", false, "refuse if an automation by that name exists")
	fs.BoolVar(&o.RequireRunnable, "require-runnable", false, "refuse unless this machine passes the checks")
	fs.StringVar(&o.RenameFrom, "rename-from", "", "replace the automation of this name (the body may rename it)")
	fs.StringVar(&o.IfMatch, "if-match", "", "refuse unless automations.yaml still has this sha256")
	fs.StringVar(&o.Patch, "patch", "", "JSON object of keys to set on the body (null removes one)")
	rest, err := parseArgs(fs, args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("apply reads the automation from stdin and takes no names")
	}
	if asJSON {
		if err != nil {
			emit(nil, usageError(err))
		}
		body, readErr := io.ReadAll(os.Stdin)
		if readErr != nil {
			emit(nil, readErr)
		}
		emit(api.Apply(body, o, time.Now()))
		return nil
	}
	if err != nil {
		return err
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	res, err := api.Apply(body, o, time.Now())
	if err != nil {
		return err
	}
	verb := map[string]string{"created": "Created", "replaced": "Replaced", "renamed": "Renamed", "unchanged": "Unchanged"}[res.Action]
	if o.DryRun {
		verb = "Would be " + strings.ToLower(verb)
	}
	fmt.Printf("%s %s\n", verb, res.Automation.Name)
	for _, c := range res.Checks {
		mark := "ok  "
		if !c.OK {
			mark = map[string]string{"error": "FAIL", "warning": "warn"}[c.Level]
		}
		fmt.Printf("  %s %-18s %s\n", mark, c.ID, c.Message)
	}
	return nil
}

func deleteCmd(args []string) error {
	fs, asJSON := newFlags("delete", args)
	ifMatch := fs.String("if-match", "", "refuse unless automations.yaml still has this sha256")
	name, err := oneName("delete", args, fs)
	if asJSON {
		if err != nil {
			emit(nil, usageError(err))
		}
		emit(api.Delete(name, *ifMatch))
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := api.Delete(name, *ifMatch); err != nil {
		return err
	}
	fmt.Printf("Deleted %s\n", name)
	return nil
}

func historyCmd(args []string) error {
	fs, asJSON := newFlags("history", args)
	limit := fs.Int("limit", 30, "how many runs, newest first (0 = all)")
	runID := fs.String("run", "", "one run and every record of it")
	rest, err := parseArgs(fs, args)
	switch {
	case err != nil:
	case len(rest) > 1:
		err = fmt.Errorf("history takes at most one automation name")
	case *runID != "" && len(rest) > 0:
		err = fmt.Errorf("--run takes no automation name")
	case *limit < 0:
		err = fmt.Errorf("--limit must be 0 or more")
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	if asJSON {
		if err != nil {
			emit(nil, usageError(err))
		}
		if *runID != "" {
			emit(api.RunTimeline(*runID))
		} else {
			emit(api.History(name, *limit))
		}
		return nil
	}
	if err != nil {
		return err
	}
	if *runID != "" {
		recs, err := history.Timeline(*runID)
		if err != nil {
			return err
		}
		return printRuns(recs)
	}
	runs, err := history.Runs(name, *limit)
	if err != nil {
		return err
	}
	return printRuns(runs)
}

func printRuns(runs []history.Record) error {
	if len(runs) == 0 {
		fmt.Println("No runs yet.")
		return nil
	}
	fmt.Printf("%-20s %-24s %-9s %-7s %s\n", "AT", "AUTOMATION", "STATUS", "TRIGGER", "DETAIL")
	for _, r := range runs {
		detail := r.Error
		if r.UserActionRequired != nil {
			action := "NO"
			if *r.UserActionRequired {
				action = "YES"
			}
			detail = "Action: " + action + " — " + r.Summary
			if r.TabClosed {
				detail += " (tab closed)"
			}
		}
		if detail == "" {
			detail = r.WorkspaceID
		}
		fmt.Printf("%-20s %-24s %-9s %-7s %s\n",
			r.At.Format(time.DateTime), r.Automation, r.Status, r.Trigger, detail)
	}
	return nil
}

func reportCmd(args []string) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	path := flags.String("file", "", "completion report path supplied by the scheduler")
	action := flags.String("action", "", "yes or no")
	summary := flags.String("summary", "", "summary and any next action")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || (*action != "yes" && *action != "no") || flags.NArg() != 0 {
		return fmt.Errorf("use report --file PATH --action yes|no --summary TEXT")
	}
	required := *action == "yes"
	if err := report.Write(*path, report.Report{UserActionRequired: &required, Summary: *summary}); err != nil {
		return err
	}
	label := "NEIN"
	if required {
		label = "JA"
	}
	fmt.Printf("Aktion für Oliver: %s — %s\n", label, *summary)
	return nil
}
