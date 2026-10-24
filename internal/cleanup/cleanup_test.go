package cleanup

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/herdr"
)

// ── the production adapter, against a real repo ─────────────────────

// repoWithBranches builds a repo on main plus two run branches: one that never
// committed anything, one carrying work of its own.
func repoWithBranches(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "base"},
		{"branch", "auto/did-nothing"},
		{"checkout", "-b", "auto/did-work"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "work"},
		{"checkout", "main"},
	}
	// Through gitCommand: with a plain `git -C`, an inherited GIT_DIR (`git
	// rebase --exec` sets one) made this script re-initialise the real
	// repository (core.bare = true), branch in it and move its HEAD.
	for _, args := range script {
		if out, err := gitCommand(dir, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestVerdict(t *testing.T) {
	repo := repoWithBranches(t)
	r := gitRepos{}
	base, err := r.DefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" {
		t.Fatalf("expected the main fallback, got %q", base)
	}

	cases := []struct {
		name string
		wt   herdr.Worktree
		want Verdict
	}{
		{"produced nothing", herdr.Worktree{Branch: "auto/did-nothing"}, Removable},
		{"has its own commits", herdr.Worktree{Branch: "auto/did-work"}, KeptUnmerged},
		{
			// An open workspace outranks everything: it means nobody has read
			// the run yet, whatever git thinks of the branch.
			"still open",
			herdr.Worktree{Branch: "auto/did-nothing", OpenWorkspaceID: "w42"},
			KeptOpen,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := verdict(r, repo, c.wt, base); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A GIT_DIR inherited from a hook or a rebase must not redirect cleanup to
// another repository: it removes branches.
func TestGitActsOnTheRepoItIsGivenWhateverGitDirSays(t *testing.T) {
	repo := repoWithBranches(t)
	decoy := repoWithBranches(t)
	if out, err := gitCommand(decoy, "branch", "-D", "auto/did-nothing").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := gitCommand(repo, "worktree", "add", wt, "auto/did-nothing").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))

	r := gitRepos{}
	if !r.Merged(repo, "auto/did-nothing", "main") {
		t.Fatal("Merged looked somewhere other than repo")
	}
	if err := r.Remove(Candidate{Repo: repo, Branch: "auto/did-nothing", Path: wt}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if gitCommand(repo, "rev-parse", "--verify", "--quiet", "auto/did-nothing").Run() == nil {
		t.Error("the branch is still in repo")
	}
	if gitCommand(decoy, "rev-parse", "--verify", "--quiet", "auto/did-work").Run() != nil {
		t.Error("the decoy lost a branch")
	}
}

// ── the plan, against a fake ────────────────────────────────────────

// fakeRepos answers for whatever worktrees the test declares, with no repo on
// disk. merged names the branches git would call ancestors of the default one.
type fakeRepos struct {
	worktrees map[string][]herdr.Worktree
	merged    map[string]bool
	baseErr   error
	listErr   error
	removeErr map[string]error

	removed []string
	visits  int
}

func (f *fakeRepos) Worktrees(repo string) ([]herdr.Worktree, error) {
	f.visits++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.worktrees[repo], nil
}

func (f *fakeRepos) DefaultBranch(string) (string, error) {
	if f.baseErr != nil {
		return "", f.baseErr
	}
	return "main", nil
}

func (f *fakeRepos) Merged(_, branch, _ string) bool { return f.merged[branch] }

func (f *fakeRepos) Remove(c Candidate) error {
	if err := f.removeErr[c.Branch]; err != nil {
		return err
	}
	f.removed = append(f.removed, c.Branch)
	return nil
}

func cfgOf(repos ...string) *config.Config {
	cfg := &config.Config{}
	for i, r := range repos {
		cfg.Automations = append(cfg.Automations,
			config.Automation{Name: string(rune('a' + i)), Repo: r})
	}
	return cfg
}

func TestScanSortsWorktreesIntoRemovableAndKept(t *testing.T) {
	r := &fakeRepos{
		worktrees: map[string][]herdr.Worktree{"/repo": {
			{Branch: "main", Path: "/repo"},
			{Branch: "auto/landed", Path: "/wt/1"},
			{Branch: "auto/open", Path: "/wt/2", OpenWorkspaceID: "w1"},
			{Branch: "auto/unmerged", Path: "/wt/3"},
			{Branch: "feature/mine", Path: "/wt/4"},
		}},
		merged: map[string]bool{"auto/landed": true},
	}

	plan, err := scan(cfgOf("/repo"), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removable) != 1 || plan.Removable[0].Branch != "auto/landed" {
		t.Fatalf("removable = %+v, want just auto/landed", plan.Removable)
	}
	if len(plan.Kept) != 2 {
		t.Fatalf("kept = %+v, want the open one and the unmerged one", plan.Kept)
	}
	// The source checkout and somebody's own feature branch are never ours.
	for _, c := range plan.All() {
		if !strings.HasPrefix(c.Branch, BranchPrefix) {
			t.Errorf("%s is not a run worktree and must be left alone", c.Branch)
		}
	}
}

func TestScanVisitsARepoOnceEvenWhenAutomationsShareIt(t *testing.T) {
	r := &fakeRepos{worktrees: map[string][]herdr.Worktree{"/repo": nil}}
	if _, err := scan(cfgOf("/repo", "/repo", "/repo"), r); err != nil {
		t.Fatal(err)
	}
	if r.visits != 1 {
		t.Fatalf("visited %d times, want 1", r.visits)
	}
}

func TestScanFailsLoudlyWhenItCannotTellWhichBranchIsDefault(t *testing.T) {
	// Guessing here would classify unmerged work as removable.
	r := &fakeRepos{
		worktrees: map[string][]herdr.Worktree{"/repo": {{Branch: "auto/x"}}},
		baseErr:   errors.New("cannot tell which branch is the default one"),
	}
	if _, err := scan(cfgOf("/repo"), r); err == nil {
		t.Fatal("want an error rather than a guess")
	}
}

func TestSummaryTellsAnEmptyBoardApartFromOneWithNothingToRemove(t *testing.T) {
	// The distinction the board used to lose: "nothing to remove" reads as "no
	// run worktrees exist", and sends you hunting for worktrees that are right
	// there, held back for a reason.
	empty := Plan{}
	if got := empty.Summary(); got != "no run worktrees" {
		t.Errorf("empty summary = %q", got)
	}

	held := Plan{Kept: []Candidate{
		{Branch: "auto/a", Verdict: KeptOpen},
		{Branch: "auto/b", Verdict: KeptOpen},
		{Branch: "auto/c", Verdict: KeptUnmerged},
	}}
	got := held.Summary()
	if !strings.HasPrefix(got, "nothing to remove: ") {
		t.Fatalf("summary = %q", got)
	}
	if !strings.Contains(got, "2 workspace still open") ||
		!strings.Contains(got, "1 commits not in the default branch") {
		t.Errorf("summary = %q, want it to name what is being held and why", got)
	}
}

func TestSummaryCountsEverythingWhenSomethingCanGo(t *testing.T) {
	plan := Plan{
		Removable: []Candidate{{Branch: "auto/a", Verdict: Removable}},
		Kept:      []Candidate{{Branch: "auto/b", Verdict: KeptOpen}},
	}
	got := plan.Summary()
	if !strings.HasPrefix(got, "2 run worktrees: ") {
		t.Fatalf("summary = %q", got)
	}
	if !strings.Contains(got, "1 merged") {
		t.Errorf("summary = %q, want the removable ones named", got)
	}
}

func TestApplyRemovesOnlyWhatThePlanSaidItWould(t *testing.T) {
	r := &fakeRepos{
		worktrees: map[string][]herdr.Worktree{"/repo": {
			{Branch: "auto/landed", Path: "/wt/1"},
			{Branch: "auto/open", Path: "/wt/2", OpenWorkspaceID: "w1"},
		}},
		merged: map[string]bool{"auto/landed": true},
	}
	plan, err := scan(cfgOf("/repo"), r)
	if err != nil {
		t.Fatal(err)
	}

	var announced []string
	removed, err := plan.Apply(func(c Candidate) { announced = append(announced, c.Branch) })
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(r.removed) != 1 || r.removed[0] != "auto/landed" {
		t.Fatalf("removed %v, want just auto/landed", r.removed)
	}
	if len(announced) != 1 {
		t.Errorf("announced %v, want each removal reported once", announced)
	}
}

func TestApplyStopsAtTheFirstRefusal(t *testing.T) {
	// git declining a dirty checkout means the plan was wrong about something.
	// Ploughing on would turn one readable refusal into a tally.
	r := &fakeRepos{
		worktrees: map[string][]herdr.Worktree{"/repo": {
			{Branch: "auto/a", Path: "/wt/a"},
			{Branch: "auto/b", Path: "/wt/b"},
			{Branch: "auto/c", Path: "/wt/c"},
		}},
		merged:    map[string]bool{"auto/a": true, "auto/b": true, "auto/c": true},
		removeErr: map[string]error{"auto/b": errors.New("worktree contains modified files")},
	}
	plan, err := scan(cfgOf("/repo"), r)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := plan.Apply(nil)
	if err == nil {
		t.Fatal("want the refusal returned")
	}
	if removed != 1 {
		t.Errorf("removed = %d, want the one that went before the refusal", removed)
	}
	if len(r.removed) != 1 {
		t.Errorf("removed %v, want it to have stopped", r.removed)
	}
}
