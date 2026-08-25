# ADR 0001 — cleanup is never scheduled

**Status**: accepted · **Date**: 2026-08-25

## Context

Runs leave worktrees behind. Left alone they accumulate, and the obvious fix is
to reap them on a timer.

## Decision

Nothing in `internal/cleanup` runs on a schedule. Removal is always an explicit
act — `herdr-automations cleanup`, or `c` on the board — and the default is to
keep.

## Why

A run whose workspace is still open is a run nobody has read. That makes Herdr's
sidebar the inbox for unattended work, and the open workspace the only durable
signal saying which runs still want a human. A reaper that tidied on its own
would delete exactly that signal.

`Plan` reports counts anyway, so the accumulation stays visible without anything
acting on it.

## Consequences

- Worktrees pile up on a machine whose owner never runs cleanup. Accepted: the
  pile is the point.
- Nothing here may ever be wired into the daemon's tick.
