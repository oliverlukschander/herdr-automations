# ADR 0008 — no event triggers

**Status**: accepted · **Date**: 2026-09-01

## Context

The README used to say event triggers (`on push`, `on PR`, `on
worktree.created`) were planned, because Herdr's plugin manifest already
supports `[[events]]`. Checked against Herdr 0.8.2's actual event list, that
promise does not hold up.

The events a plugin can subscribe to are exhaustively:

```
worktree.created, worktree.removed
workspace.created, workspace.closed, workspace.focused, workspace.renamed, workspace.updated
pane.created, pane.closed, pane.exited, pane.focused, pane.updated
tab.created, tab.closed, tab.focused, tab.renamed
layout.updated
```

Seventeen events, all Herdr workspace lifecycle. None of them is a plausible
prompt trigger: nobody schedules an agent on a focus change, and the only
even-remotely-credible one — `worktree.created` → "install the deps" — is a
git hook's or a `Makefile`'s job, not a scheduler's. There is no git event, no
push, no pull request, and no webhook endpoint anywhere in the list.

## Decision

No event triggers. Not planned.

## Why

The measured cost of building it anyway, for a use case that does not exist in
this event list:

- A queue on disk and a `SIGUSR1` to the daemon, because the event-process's
  `inFlight` set starts empty each time and would double-fire without one.
- A git-root resolver to recognise which worktree an event belongs to.
- An `auto/` prefix heuristic to break the loop the plugin already creates for
  itself: every cron run creates a worktree, which is itself a
  `worktree.created` event.
- A storm circuit-breaker, because a fast lifecycle event (`pane.updated`, for
  instance) firing a scheduled prompt would be its own denial-of-service.
- `min_herdr_version` raised to 0.8.2, so automations written against it fail
  to load — not run silently — on the 0.8.0 baseline this plugin currently
  supports.

Five new pieces of infrastructure for a trigger surface that, per the list
above, cannot express "on push" or "on PR" anyway — the two triggers anyone
actually asks for. Turning a GitHub push into a local run needs something
hosted in the middle holding your repo names and listening for the webhook,
which is the opposite of "the scheduler is a file, not a platform"
([README](../../README.md), Why).

If you want work to happen on push, your forge's CI already runs there. If you
want it to happen locally, a cron automation that polls and does nothing when
there is nothing new (`gh pr list`, `git fetch`) is one entry in
`automations.yaml` — no new mechanism required.

## What else stays out, and why

Event triggers are the one gap worth an ADR of its own. The rest of what Open
Run has and this plugin doesn't is the same line drawn again:

| Missing | Decision | Why |
|---|---|---|
| `env:` per automation | out of scope, upstream issue | `--env` exists on `workspace create` but not on `worktree create`, and `workspace: worktree` is the default. A field that works on one path and not the other is worse than no field. Filed against Herdr as a `worktree create --env` request; until then the agent inherits the session's env, and `mcp_config` already covers the MCP case |
| File-by-file diff viewer | out of scope | [ADR 0001](0001-cleanup-is-never-scheduled.md) and "the worktrees accumulate on purpose" (README FAQ): the Herdr sidebar is the inbox, and `enter` on the board already does better |
| GitHub/Jira/Linear webhooks | out of scope | requires a relay, which requires an account, which requires a service — the boundary this ADR exists to hold |
| Mobile approval app | out of scope | same reason |
| Retries / a run queue | out of scope | [ADR 0006](0006-occurrences-are-dropped-not-queued.md): an occurrence is abandoned, not queued, on purpose |

## Consequences

- The README's Event triggers FAQ entry states this as a decision, not a gap.
- `skills/creating-automations/SKILL.md` tells an agent asked for one to
  propose a polling cron instead of promising a feature that does not exist.
- If Herdr ever adds a genuine repo-level event (a push, a PR opened), this
  ADR is the place to revisit — the event list above is what would have
  changed.
