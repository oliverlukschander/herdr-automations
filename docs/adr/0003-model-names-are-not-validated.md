# ADR 0003 — model names are passed through unvalidated

**Status**: accepted · **Date**: 2026-08-25

## Context

`model:` becomes `--model <value>` on the agent executable. An allowlist would
catch typos while the file is being written rather than at 3am.

## Decision

Any non-empty `model` loads. What *is* validated is whether the agent *kind*
takes a `--model` flag at all (`config.KindAcceptsModel`), because Herdr
forwards the flag verbatim and a kind that doesn't take it fails to start.

## Why

Model names move faster than any list this repo could keep. A stale allowlist
rejects a model that works, which is worse than passing through one that
doesn't: the agent refuses it immediately and the run is recorded as failed with
the agent's own message.

## Consequences

- A typo'd model surfaces as a failed run, not a diagnostic.
- `modelFlagKinds` still needs updating when a new agent kind appears.
- `modelFlagKinds` is a known-good allowlist, not a safety gate: `herdr agent
  start --kind` accepts far more kinds than this table lists (22, as of Herdr
  0.8.2), so a kind missing here is a false rejection of a valid config, not a
  guard catching a real mistake.
