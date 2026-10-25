# ADR 0011 — a kept run gives up its agent name

**Status**: accepted · **Date**: 2026-10-25

## Context

Outside `existing` mode a run's agent is named after its automation, and ADR 0009 counts on the previous run's
name being free because its workspace was closed. Since action reports, a run that needs action keeps its tab
on purpose, and so do a failed run and a run that reports no action but whose tab cannot be safely closed. Each
keeps an agent under the plain name, and Herdr refuses that name to the next run's `agent start`. Three hourly
or half-hourly automations failed 194 times in six weeks with `agent_name_taken`, each time because an earlier
tab was waiting for its human.

## Decision

When a `root` or `worktree` run ends with its agent started and its tab still open, the runner renames that
agent to the automation's name plus the run's start (`<name>-MMDD-HHMM`, the minute history shows). It does
this before the run gives back its in-flight slot. When that name is taken, it clears the agent's name instead.
The rename is best effort and never changes the run's outcome.

## Why

- Always prefixing, as `existing` mode does, would cost every run its readable name in the sidebar, even the
  ones whose tab closes a minute later.
- Falling back to a different name only when the next run finds the plain one taken gives the odd name to the
  wrong agent: the run that is working gets it, and the old tab nobody has read keeps the plain one.
- Renaming at the end puts the odd name on what is finished, and the start time in it says which run it was.

## Consequences

- A run that is working is always `<name>`. A kept tab is `<name>-MMDD-HHMM`, or nameless after a clash.
- A tab that was open before this change, or a daemon that dies between a run's end and the rename, still holds
  the plain name until someone closes that tab.
- Nothing finds a run's agent by name. Everything after `agent start` goes by pane ID, so the rename changes only
  what Herdr shows.
