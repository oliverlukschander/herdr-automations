# ADR 0007 — a failure raises a Herdr toast

**Status**: accepted · **Date**: 2026-09-01

## Context

Nothing in the plugin ever notified. A run that failed at 3am was only
discoverable by typing `history`. `grep -i notify` on the repo found nothing but
`signal.Notify`. A scheduler that records without telling is only half a
scheduler.

## Decision

`runner.record` — the only writer of run history — is also the only caller of
`internal/notify`, so the two cannot drift apart. `daemon.evaluate` calls
`notify.Tick` once per tick with everything missed and invalid that tick found.

| Status | Toast | `--sound` | Why |
|---|---|---|---|
| `failed` | yes, immediate | `request` | the reason this exists |
| `missed` | yes, grouped per tick | `none` | history.go already says a missing run is worse than a red one |
| `invalid` | yes, grouped per tick | `none` | a silently dead automation; only the daemon notices |
| `cancelled` | no | — | the user closed the workspace; they were there ([ADR 0006](0006-occurrences-are-dropped-not-queued.md), and the crash-vs-cancel fix that keeps this status honest) |
| `skipped` | no | — | ADR 0006 abandons it on purpose; toasting would fire on the system's most benign fact |
| `done` / `running` / `scheduled` | no | — | a toast per successful nightly run is how you train yourself to ignore the feature |

`missed` and `invalid` are grouped per tick rather than per item: a laptop
waking from a weekend can find twenty automations at once, and twenty stacked
panels is not a notification. The grouping gets its deduplication for free —
`schedule.Plan` already collapses lateness into one `Missed` per automation per
tick, and `daemon`'s `state.Invalid` already dedupes by entry — so no new
timer, map, or backoff was needed.

A pathological case is accepted rather than engineered around: a cron running
every minute that fails every time toasts every minute. The answer is to pause
the automation, consistent with "nothing happens that the file doesn't say."

`herdr notification show` was chosen over `osascript` / `notify-send` because
it is the one seam ADR 0004 already establishes — a single implementation for
macOS and Linux, going through the same client the rest of the plugin does,
rather than a second OS-specific adapter.

## Why

- **`record` is the chokepoint.** Every status a run can end in already passes
  through it to reach history. Making it the only caller of `notify` means a
  future status inherits its notification policy for free, and `runner` itself
  filters nothing — the decision lives in one package.
- **Only the daemon notifies.** The CLI's `run <name>` prints its error to a
  terminal someone is already reading; the board is an altscreen TUI that
  repaints the row red on its own two-second refresh. A toast over either would
  say the same thing to the same eyes.
- **The body is bounded and degrades silently.** `notify.Outcome` truncates to
  200 runes, first line only, cut on a rune boundary — the `hwf` adapter
  recovers its exit code by scraping a pane, so a "detail" can be a full screen
  of terminal output, and a byte-cut would mojibake a multi-byte character. A
  sink error is logged once per process (`sync.Once`) and then dropped, with no
  latch: notifications resume as soon as Herdr does. The accepted cost is up to
  three seconds per already-failed run while Herdr itself is down.
- **No delay is possible, by construction.** The only statuses that reach the
  sink are terminal ones, so a toast never fires before a run's actual work is
  done. That is what lets `Outcome` stay a synchronous call inside `record`
  instead of a goroutine the daemon would have to wait for at shutdown.

## Reservation

Verified in-session only: whether a Herdr toast persists past a few seconds,
and whether one raised while a session is detached is visible on reattach.
Both unverified means the transport's value is in doubt and is the only
condition under which reopening `osascript` / `notify-send` is worth it.

## Consequences

- `notify.New()` is constructed once per daemon process and threaded through
  `runner.NewWith`; `runner.New` / `runner.Default` keep a nil notifier, so the
  board and the CLI's manual `run` stay silent by design, not by accident.
- `min_herdr_version` stays at 0.8.0. `notification show` is verified on 0.8.2;
  on older Herdr the CLI call fails and falls into the same silent-degradation
  path as any other sink error. Notifications are not worth locking out a
  plugin that otherwise works.
