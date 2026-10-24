# ADR 0010 — run-now over SSH is a request to the daemon

**Status**: accepted · **Date**: 2026-10-24

## Context

The Orchestrator app (GAI-1075) manages automations on every machine over SSH, "run now" included. `run <name>`
did the run itself, in the calling process, for as long as the agent took:

- an SSH session that hung up killed it mid-run, and the run stayed `running` in the history for good;
- it ran in the SSH session's environment, not Herdr's: another PATH, and on macOS outside the user's
  bootstrap namespace;
- its in-flight set was its own, so it could not see a cron run of the same automation in the daemon, and the
  two would overlap instead of one being skipped (ADR 0006).

## Decision

`run <name> --detach` writes a **request** (run ID, automation, time) into `requests/` in the state dir and sends
SIGUSR1 to the daemon. The daemon takes requests on that signal and on every tick, and runs each one through
its own runner, under the run ID the CLI already handed back.

- A request that waited more than two minutes is dropped and recorded `missed`, under its ID.
- No daemon means an error and nothing written. A request with nobody to take it would fire whenever a daemon
  next started, which is a queue by another name.
- The PID comes from the pidfile whose flock a daemon holds (`Probe`). The pidfile's second line, `requests`,
  says the daemon takes requests. One from before this ADR ignores SIGUSR1 (a Go program drops signals it does
  not listen for) and never reads `requests/`. A request for it would only ever end as `missed`, so the CLI
  answers `daemon_not_running` ("try again after the upgrade") instead of writing one.
- The daemon installs its SIGUSR1 handler before it takes the lock. Until then nobody can see it as running, so
  no wake-up is lost. (Corrected 2026-10-24 after review: an earlier version of this ADR said an old daemon
  would die of the signal. It would not.)

`run <name>` without `--detach` is unchanged, for a terminal that stays open.

Every record the runner writes now carries `runner_pid`. A run whose last record says scheduled or running, while
that process is gone, was **interrupted**:
- the daemon writes that down when it starts, with a toast like a failure's (it is one nobody decided on);
- the JSON views show it at once.

## Why not a detached CLI process

`setsid` would have survived the hang-up, but kept the SSH environment and the separate in-flight set. Those
were two of the three problems.

## The tension with ADR 0008

ADR 0008 counted "a queue on disk and a SIGUSR1 to the daemon" as a cost of event triggers. The difference is
volume and source:
- here a person asks for one run at a time;
- nothing fires requests on its own, so there is no storm to break;
- nothing waits longer than two minutes.

The queue is a mailbox with a short memory, and occurrences stay dropped, not queued.

## Consequences

- ADR 0007's table gains a row: `interrupted` toasts immediately, like `failed`.
- A CLI or board run that dies while the daemon keeps running is recorded as interrupted only at the daemon's
  next start; until then only the views say so.
- A reused PID can make a dead run look alive. It never makes a live one look dead.
