# ADR 0009 — a run may share a workspace it did not create

**Status**: accepted · **Date**: 2026-09-14

## Context

Both provisioning modes open a workspace per run. For a daily chore that is the
point — ADR 0001 keeps the open workspace as the signal saying a run still wants
a human. For an hourly one it is twenty-four workspaces a day in the sidebar,
and the signal drowns in its own volume.

The obvious fix is the one ADR 0001 forbids: reap the old ones on a timer.

## Decision

A third mode, `workspace: existing`, with a required `workspace_id`. Each run
opens a fresh tab in that workspace, at `repo`, without focus. Several
automations may point at the same ID.

The ID is the user's: the plugin never creates the workspace, and a run whose
target is missing or closed fails. It does not open a replacement.

## Why

It moves the accumulation somewhere cheaper without anything deleting anything.
Tabs inside one workspace are the same inbox as workspaces in the sidebar, one
level down, and closing one is still how you say you are done with a run.

Requiring the ID rather than a label, and failing when it is gone, keeps the
file the only thing that decides where a run lands. A mode that re-created a
missing workspace would quietly outlive the decision to close it.

## Consequences

- Runs sharing a workspace share `repo` as their working directory and see each
  other's uncommitted changes, exactly like `root`. No worktree, no branch.
- `cleanup` has nothing to remove for these runs, and the board's `enter` lands
  on the workspace, which is shared — the run's own pane ID is in the history.
- Agent names have to carry the pane ID in this mode. Elsewhere the previous
  run's name is free because its workspace was closed; here earlier runs stay
  live on purpose. Pane IDs are case-sensitive and agent names are not, so the
  ID is case-marked before it is folded (`internal/host/host.go`).
- Nothing here may grow a "create it if it's missing" flag.
