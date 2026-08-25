package host

import (
	"os/exec"
	"time"

	"github.com/DnzzL/herdr-automations/internal/herdr"
)

// ops is this package's internal seam: the individual Herdr calls, one method
// each. It is unexported on purpose — callers of Host must not have to know
// that starting an agent can answer "the pane is not a shell yet", only that
// the work either happened or didn't. The tests script it.
type ops interface {
	WorktreeCreate(repo, branch, label string) (workspaceID, paneID string, err error)
	WorkspaceCreate(cwd, label string) (workspaceID, paneID string, err error)

	AgentStart(name, kind, paneID string, extraArgs []string) error
	AgentSubmit(target, text string) error
	AgentSubmitPending(paneID string) error
	AgentStatus(target string) (string, error)
	AgentWait(target string, timeout time.Duration) error

	PaneRun(paneID string, command ...string) error
	PaneRead(paneID string, lines int) (string, error)

	// HasCode reports whether err is a Herdr API error with the given code.
	// It travels with the ops so a fake can answer for its own errors.
	HasCode(err error, code string) bool
	// LookPath reports whether a binary the delegation needs is installed.
	LookPath(file string) error
}

// herdrOps is the production ops: the herdr CLI client, unchanged.
type herdrOps struct{}

func (herdrOps) WorktreeCreate(repo, branch, label string) (string, string, error) {
	return herdr.WorktreeCreate(repo, branch, label)
}

func (herdrOps) WorkspaceCreate(cwd, label string) (string, string, error) {
	return herdr.WorkspaceCreate(cwd, label)
}

func (herdrOps) AgentStart(name, kind, paneID string, extraArgs []string) error {
	return herdr.AgentStart(name, kind, paneID, extraArgs)
}

func (herdrOps) AgentSubmit(target, text string) error {
	return herdr.AgentSubmit(target, text)
}

func (herdrOps) AgentSubmitPending(paneID string) error {
	return herdr.AgentSubmitPending(paneID)
}

func (herdrOps) AgentStatus(target string) (string, error) {
	return herdr.AgentStatus(target)
}

func (herdrOps) AgentWait(target string, timeout time.Duration) error {
	return herdr.AgentWait(target, timeout)
}

func (herdrOps) PaneRun(paneID string, command ...string) error {
	return herdr.PaneRun(paneID, command...)
}

func (herdrOps) PaneRead(paneID string, lines int) (string, error) {
	return herdr.PaneRead(paneID, lines)
}

func (herdrOps) HasCode(err error, code string) bool { return herdr.HasCode(err, code) }

func (herdrOps) LookPath(file string) error {
	_, err := exec.LookPath(file)
	return err
}
