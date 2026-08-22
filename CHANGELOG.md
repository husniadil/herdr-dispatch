# Changelog

What a consumer of `hdis` has to change between released versions. §13.3 of
the shared plugin contract makes the CLI, the MCP tool list, the JSON shapes
and the error codes stable within a minor and changeable between minors with an
entry here, so every entry says what moved and what a caller does about it.

## Unreleased

Every worker pane is launched with `FORCE_PROMPT_CACHING_5M=1`, so a worker
takes the 5-minute prompt-cache TTL instead of the 1-hour one a REPL main
thread is given. The operator's own sessions are untouched. Inert on the
`codex` path, where a relayed `cache_control` is not forwarded upstream.

Callers do nothing. No CLI, tool, JSON shape or error code changes.

The bindings are durable. They are written to
`${XDG_STATE_HOME:-~/.local/state}/hdis/hdis-bindings.json` on every change and
re-adopted at the next start, after each one is verified against the board and
Herdr. A prompted-but-unclaimed worker is no longer forgotten by a restart, and
a restart no longer dispatches a task into a second pane while the first is
still alive.

Callers do nothing. `doctor` gains two fields, `bindings` (where they live) and
`readopted` (how many came back at the last start); every other JSON shape,
tool name and error code is unchanged.

## 0.1.0 — 2026-08-21

First release. One binary, `hdis`, that is the daemon, the CLI and the stdio
MCP server. It watches the `htask` board, brings a worker pane up in Herdr for
each ready task, delivers the task's goal, tracks the worker, and stops at
review, where the board's own review gate takes over. Declares the shared
plugin contract at 0.4.0.
