# ADR 0002 — the scheduler reads the wall clock

**Status**: accepted · **Date**: 2026-08-25 (decision made in b94280e)

## Context

The natural way to schedule a 9am Monday run is to compute the delay and arm a
timer for it.

## Decision

`schedule.Plan` is called every 30 seconds with `time.Now()` and compares
against the last occurrence it recorded. No timers are armed for future
occurrences.

## Why

macOS suspends the monotonic clock while the laptop sleeps. A timer armed for
"in 15 hours" fires 15 hours of *awake* time later, which is how a 9am Monday
run silently never happened. Wall-clock arithmetic notices the occurrence on
wake instead, which is also what makes catch-up expressible at all.

## Consequences

- A run resumes within one tick of the machine waking, not instantly.
- `schedule.State` has to be persisted, or a restart replays the morning.
- Every occurrence between two ticks is visible, so "how many did we skip" is
  answerable — it is what the `missed` count reports.
