#!/bin/sh
# Plugin entrypoint. Herdr passes its server's environment to plugin commands,
# and that can be a bare launchd PATH (seen 2026-09-30:
# ~/.cargo/bin:/usr/bin:/bin:/usr/sbin:/sbin). Workflow automations need hwf,
# which lives in ~/.local/bin, so put the usual user bin dirs first.
PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
export PATH
exec ./bin/herdr-automations "$@"
