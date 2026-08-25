# ADR 0004 — driving Herdr sits behind a two-method seam

**Status**: accepted · **Date**: 2026-08-25

## Context

`internal/herdr` was twelve package-level functions, each one `exec.Command`
deep, called directly by `runner.Run`. Four of the ten releases up to v0.4.2
fixed sequencing bugs in that function — waiting for the pane's shell, verifying
the prompt landed, not waiting out a closed workspace — and none of the fixes
could be pinned by a test. There was no interface between the lifecycle and the
process spawn, so tests could only reach the retry helpers by handing them
closures, which left the sequencing itself uncovered.

## Decision

`internal/host` exposes two methods: `Provision` and `Do`. Everything about
driving Herdr lives behind them, including which error code means retry and
which means the agent is gone. The individual herdr calls are an internal seam
(`ops`) that the package's own tests script.

`runner` is the lifecycle only, and takes a `host.Host`.

## Why

Callers and tests should cross the same seam. With one, `runner.Run` is testable
end to end against a fake — the trail a run leaves in history, a cancellation
told apart from a failure, an overlapping occurrence skipped rather than queued,
the in-flight slot not leaking.

The prompt/workflow branch became two adapters at that seam rather than an `if`
in the runner, which is where the shell quoting and screen scraping now live.

## Consequences

- Adding a herdr call means touching `ops`, its production adapter and the fake.
  Deliberate friction: the alternative was a 12-function interface.
- `herdr.Focus` and `herdr.ErrGone` stay outside the seam. They're the board
  jumping to a workspace, not a run happening.
