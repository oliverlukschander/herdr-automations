# ADR 0005 — a bad entry costs one automation, not all of them

**Status**: accepted · **Date**: 2026-08-25 · Supersedes the behaviour up to v0.4.2

## Context

`config.Load` returned one error for the whole file. Any invalid entry made the
daemon log "config error, leaving the schedule untouched" and stop scheduling
everything — including the entries that were fine.

## Decision

Loading is partial. An entry that fails validation becomes a
`config.Diagnostic` carrying its name, the line it starts on and what is wrong;
the rest load and run. Only a file that will not parse is still an error.

## Why

For a tool whose promise is "it ran while you slept", a silent absence is the
worst failure mode available. A typo in the seventh automation stopping the
other six, with the only trace in a log nobody reads at 09:00, is that failure
mode exactly.

Line numbers come from decoding through `yaml.Node`. They also retired
`config.LineOf` and its heuristic scan for `- name:`.

## Consequences

- Callers must render `cfg.Invalid` or the failure is invisible again. All four
  frontends do: `list` names them, the board gives each a red row, the daemon
  records one history entry per diagnostic (once, not every tick), `run <name>`
  says why rather than "no automation named x".
- A duplicate name keeps the first entry and diagnoses the second.
- `Config.Automations` is now "the ones that will run", which is what every
  caller wanted anyway.
