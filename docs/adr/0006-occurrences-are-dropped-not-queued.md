# ADR 0006 — overlapping occurrences are dropped, never queued

**Status**: accepted · **Date**: 2026-08-25

## Context

If the 09:00 run of an automation is still working at 10:00, the 10:00
occurrence has to go somewhere.

## Decision

It is dropped and recorded as `skipped`. There is no queue. The in-flight set is
per automation, so a long-running nightly job never blocks the morning triage.

Automations due at the same minute all start: nothing serialises across
automations either. `list` and the wizard report the overlap while the cron is
still yours to change.

## Why

Queueing unattended agent runs means an overnight backlog all starting at once
on wake. Herdr is a multi-agent runtime and honours the cron literally; a queue
here would be this plugin quietly disagreeing with it.

Dropping is only acceptable because it is recorded — `skipped` in the history is
what makes the drop visible rather than a hole.

## Consequences

- An automation slower than its own period runs less often than its cron says,
  and the history shows why.
- `runner.Busy()` is what the daemon checks before re-execing on an upgrade.
