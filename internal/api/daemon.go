package api

import (
	"time"

	"github.com/DnzzL/herdr-automations/internal/daemon"
	"github.com/DnzzL/herdr-automations/internal/herdr"
	"github.com/DnzzL/herdr-automations/internal/hostpath"
)

// StartResult is `daemon start --json`.
type StartResult struct {
	Head
	Started bool   `json:"started"`
	Daemon  Daemon `json:"daemon"`
}

// startScheduler asks Herdr to run the plugin's start-scheduler action: the
// server spawns `run.sh daemon` with the plugin's environment, the way the
// startup hook does, and not as a child of the SSH session asking.
var startScheduler = func() error {
	return herdr.Client{}.InvokeAction(hostpath.PluginID + ".start-scheduler")
}

// How long, and how often, StartDaemon looks for the daemon to come up.
var (
	startWait = 10 * time.Second
	startPoll = 200 * time.Millisecond
)

// StartDaemon answers `daemon start --json`. It only ever starts a scheduler
// that is not running: a live one re-reads automations.yaml every 30 s and
// re-execs itself on upgrade, so there is nothing a restart would add.
func StartDaemon() (*StartResult, error) {
	if st := daemon.Probe(); st.Running {
		return &StartResult{Started: false, Daemon: daemonOf(st)}, nil
	}
	if err := startScheduler(); err != nil {
		return nil, Fail(CodeDaemonNotRunning, "could not ask Herdr to start the scheduler: %v", err)
	}
	for deadline := time.Now().Add(startWait); time.Now().Before(deadline); time.Sleep(startPoll) {
		if st := daemon.Probe(); st.Running {
			return &StartResult{Started: true, Daemon: daemonOf(st)}, nil
		}
	}
	return nil, Fail(CodeDaemonNotRunning,
		"Herdr started the scheduler but it was not running %s later; see `herdr plugin log list --plugin %s`",
		startWait, hostpath.PluginID)
}
