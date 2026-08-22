# Changelog

What a consumer of `hdis` has to change between released versions. §13.3 of
the shared plugin contract makes the CLI, the MCP tool list, the JSON shapes
and the error codes stable within a minor and changeable between minors with an
entry here, so every entry says what moved and what a caller does about it.

## Unreleased

Declares the shared plugin contract at 0.6.0, up from the 0.4.0 of the first
release. §5.1/§10.1 forbid resolving a store from the Herdr-injected plugin
dirs, which this binary has no store to resolve and never read; §11.4 states
that prompt delivery is best-effort with the authoritative record elsewhere,
which here is the board; the 0.6.0 amendment reshapes §6.6 into recusal by
session, and this plugin performs no reviews. Nothing in the binary moved for
any of the three.

Callers read the new value in `doctor`'s `contract` field. Nothing else to do.

There is a verification lane, and it is off unless the config document turns it
on and names a profile. With it on, a task in review that this daemon's own
worker submitted earns a VERIFIER: a fresh pane on the same spawn path, opened
in a detached git worktree of the project at the commit under review, never the
shared tree. It reads the board's report through an MCP door, runs the
project's own uncached gate, checks two claims against the code and one
compiling mutation, and sends its findings by mail. It never approves and never
rejects, and neither does this binary — the board adapter carries three read
verbs, and no source file passes a review verb as an argument. One submission
earns one verifier; a task leaving review rearms the lane, so a re-submission
after a rejection earns another. When the worktree cannot be made, no verifier
is spawned at all.

Callers turn it on with a `verify` object in the config document, `enabled`
plus a `profile` naming one of the defined profiles. A profile that is not
defined is refused at parse rather than at spawn. Left out, the lane is off and
nothing changes.

`doctor`'s JSON gains a `verify` block: `enabled` always, and `profile` when
the lane is on. Its prose output names the lane too, before the board line, so
an unreachable board does not hide the dispatcher's own configuration.
`status`'s JSON gains `kind` on each worker, `worker` or `verifier`, because a
task in review holds two panes.

Callers that parse `doctor` or `status` see two added fields and no removed
ones. A parser that ignores unknown fields does nothing.

The bindings are durable, and a restart reconciles what it left behind. They
are written to `${XDG_STATE_HOME:-~/.local/state}/hdis/hdis-bindings.json` on
every change, whole and atomically. At the next start the daemon asks one
question of every live pane it opened — what is this, and what still needs
doing — answering it from Herdr's agent list and the board rows rather than
from the bindings, which stay as a hint. A pane still working its task is
adopted, a hold no live pane is working is released, a task that reached a
terminal state or left review takes its pane with it, and every
`hdis-verify-` checkout under this daemon's own worktree root that no binding
names is removed. Nothing outside that root, and nothing in it this daemon did
not create, is touched. A prompted-but-unclaimed worker is no longer forgotten,
and a restart no longer dispatches a task into a second pane while the first is
still alive.

The board principal is now `plugin:hdis@<pane>`, so a hold the board keeps
names the daemon that took it and a peer's hold is not in the answer to
`task list --mine`.

Callers do nothing. `doctor`'s JSON gains `bindings` (where they live) and
`readopted` (how many came back at the last start). The bindings document
itself gains a `reservations` array beside `bindings`, and each binding gains a
`worktree` field naming a verifier's checkout — both omitted when empty, so a
document this binary writes stays readable to one that predates them. Anything
reading that file by hand should expect the two.

Every pane this dispatcher splits carries `HDIS_DISPATCHER_PANE`, the address
of the daemon that spawned it, set on the split itself so it costs nothing on
the line Herdr types into the pane and does not go stale when the daemon moves
pane. It is an address only: the sender of anything a worker writes is stamped
by the mail daemon from `HERDR_PANE_ID`.

An agent in a dispatched pane reads that variable to answer the dispatcher. The
name is the whole contract and is pinned by test against what README documents.

Every worker pane is also launched with `FORCE_PROMPT_CACHING_5M=1`, so a
worker takes the 5-minute prompt-cache TTL instead of the 1-hour one a REPL
main thread is given. A worker is short-lived and rarely revisits its prefix,
so the long entry costs more than the work can spend. The operator's own
sessions are untouched. Inert on the `codex` path, where a relayed
`cache_control` is not forwarded upstream.

Callers do nothing.

`dispatch` resolves a task id across every board, the same scope
`task list --ready` already has. A task filed on another project's board could
be dispatched from the ready list but never read back, so every tick logged
that it was holding the binding and the pane was never retired.

Callers can now name a task from any board. Nothing that worked before
changes.

A board door that could not answer is no longer reported as the board saying
there is no such task. Only the board's own `NOT_FOUND` becomes ours; any
other refusal reaches the caller as `UNAVAILABLE` in the door's own words.

Callers that branch on `NOT_FOUND` to mean the task does not exist can now
trust it. A caller that treated every `dispatch` failure as absence should
handle `UNAVAILABLE` as a retryable one.

## 0.1.0 — 2026-08-21

First release. One binary, `hdis`, that is the daemon, the CLI and the stdio
MCP server. It watches the `htask` board, brings a worker pane up in Herdr for
each ready task, delivers the task's goal, tracks the worker, and stops at
review, where the board's own review gate takes over.
