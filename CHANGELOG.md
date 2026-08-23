# Changelog

What a consumer of `hdis` has to change between released versions. §13.3 of
the shared plugin contract makes the CLI, the MCP tool list, the JSON shapes
and the error codes stable within a minor and changeable between minors with an
entry here, so every entry says what moved and what a caller does about it.

## Unreleased

The daemon now opens its own log at `$XDG_STATE_HOME/hdis/hdis.log` and
appends to it, instead of leaving the log to whatever the shell line that
started it redirected — a restart that dropped the redirect used to throw the
log away silently, and it was only ever noticed once the log was needed. Every
line still goes to stdout, so a foreground operator loses nothing; a daemon a
door started, whose stdout is already that file, gets the file alone rather
than each line twice. The new `-log` flag moves the file and defaults to that
path. A log that cannot be opened is reported on stdout and the daemon starts
anyway. `hdis doctor` gained a `log` field naming the file that was opened,
omitted when none could be.

`hdis status` now says when a worker's branch has gone behind the project. A
worker's branch is cut from the project's HEAD at spawn time, so a task that
lands first moves that HEAD on and the next branch can no longer be
fast-forwarded — a fact the operator used to meet at merge time. The CLI
prints the branch as `hdis/task-7 (behind)` and the JSON status gained a
`behind` boolean on every worker row; a verifier, which works detached and has
no branch, is always `false`. Behind is measured against the project's current
HEAD with `git merge-base --is-ancestor`, and only when `status` is called,
never on the tick. A git that cannot answer logs the reason and leaves the row
unmarked. Nothing here rebases, merges or serialises dispatch: the recovery is
still the operator's.

A worker now comes up in a TAB of its own rather than as a split of the
operator's pane, and the tab is created in the workspace of the pane the task
was FILED from. `herdr tab create --workspace/--cwd/--label/--env --no-focus`
replaces `herdr pane split --pane <base>` as the placement call. The operator's
tab is never split; a worker shares only the tab opened for ITS OWN task,
compared against the tab's whole label and not its `hdis ` prefix, and a
verifier joins the tab of the task it verifies. Panes inside a tab make a
grid — the second splits right off the first, the third DOWN off the first,
the fourth down off the second — capped at `layout.max_panes_per_tab`, and a
full tab overflows into another tab in the same workspace.

Placement now follows the same rule the report address already did: the task's
pane of origin when the board names one AND that pane is still alive, the
daemon's own otherwise. Liveness is checked at the spawn, because an address
can fall back lazily and a placement cannot. `hdis status` gained a `tab`
column and the JSON gained a `tab` field. `hdis doctor` gained
`min_pane_columns` and `max_panes_per_tab`, in the JSON and in the prose.
Bindings gained a `tab` key; an older document without one
still loads, and its pane is retired as a pane.

Config gained a `layout` object with `min_pane_columns` (default 40) and
`max_panes_per_tab` (default 16). Both are measured numbers with the
measurement recorded beside them in the source and in the README: 40 is the
narrowest pane whose detection text still reads correctly, and 16 is what a
measured 226-column window holds at that floor under the grid rule. The
default was 5 while the split was always taken off the last pane; a grid
narrows a pane far more slowly, so the same measurements now work out to 16.
The cap bounds the panes ONE task may have, because a tab holds one task, and
it is not what keeps two tasks apart. A document naming a
`min_pane_columns` BELOW 40 is refused, because under it the dispatcher cannot
trust what it reads off a worker.

Config gained a top-level `max_workers` (default 2), and the daemon's
`-max-workers` flag now defaults to `0` and overrides it only when passed. The
operator's worker count used to exist nowhere but the shell line that started
the daemon, so any restart that omitted the flag silently dropped back to 2.

Ownership of a pane no longer depends on the Herdr agent name, which Herdr was
measured dropping while the pane and its work were still live. A pane whose cwd
is a checkout under this daemon's own `<state_dir>/worktrees` is recognised,
adopted and retired with no name at all. The name remains a label.

`codes.NO_BASE_PANE` is unchanged. `tab create --workspace` needs no pane, so
this release makes a paneless dispatch possible, but nothing here takes it.

A restart can now read the row of a pane no binding names, which is the pane
the restart rule exists for. A pane names its task by number, and a by-ID read
is board-agnostic — so the board refused the number, by design, and every such
pane was logged as "left as it is". The number is now asked with the project it
is unique in: the repository the pane's checkout belongs to, which git names
through the common directory a worktree shares with the repository it was cut
from. A worker in its worktree, a verifier in its detached one, and a pane
opened before worktrees existed all resolve the same way. A pane whose checkout
names no repository is left alone and logged, rather than guessed at. The
board's refusal of a bare number across projects is untouched, and reads by ID
still pass `--all-projects`.

**`hdis dispatch <number>` refuses differently when the board is not offering
that task.** The reply is now `NOT_READY`, saying the number is not among the
tasks the board is offering and to name the task by ID to be told what it is.
It used to be `UNAVAILABLE` quoting the board's `USAGE` refusal of a bare
number, which reads as a broken door rather than as a task that is not on
offer. Dispatch by ID is unchanged, and so is dispatch of a number the board
IS offering.

A worker now works in a git worktree of its own, on a branch named for its
task, rather than in the project directory. The branch is `hdis/task-<seq>`,
created at the project's current HEAD; the checkout is made under
`<state_dir>/worktrees` and removed when the binding that owns it is dropped,
which leaves the branch and every commit on it reachable. Until now only a
verifier got a checkout of its own, so every worker this daemon opened edited,
staged and committed in the tree the operator sits in — and one task's commit
swept up another task's uncommitted work the first time two ran at once. When
a checkout cannot be made, nothing is spawned at all and the reason is logged.

A verifier now detaches at the commit that was SUBMITTED, the tip of its
worker's branch, rather than at the project's HEAD. HEAD stopped being the
commit under review the moment a worker stopped committing to it.

**Operators have a step they did not have before.** A worker's commits land on
`hdis/task-<seq>` and nowhere else, so approving a task no longer leaves the
work on the project's own branch: merging that branch, and deleting it
afterwards, is the operator's own act. hdis creates a branch and removes
checkouts; it merges nothing, pushes nothing and deletes no branch, and a
source-walking test fails on any of the three.

`status` gains a `branch` field on each worker, and its prose line names the
branch beside the pane, so the work can be found without reading the bindings
file. A verifier works detached and has none, so the field is omitted for one
and the line reads `detached`.

The bindings file gains a `branch` on each record. A binding written by an
older hdis simply has none, and both files stay readable to both binaries. The
restart reap now covers worker checkouts as well as verifier ones: it is still
bounded to `<state_dir>/worktrees` and to entries carrying the `hdis-` prefix
this binary names its own with.

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

Every pane this dispatcher splits carries `HDIS_DISPATCHER_PANE`, the report
address for the task that pane was brought up for: the pane the task was
CREATED FROM when the board's row names one, and the daemon's own base pane
only when it does not. A task an operator filed at a terminal has no pane of
origin at all — nothing with a pane created it — and the daemon's pane is then
the only address there is, which is the rule working, not a gap in the row. It
is set on the split itself so it costs nothing on the line Herdr types into the
pane and does not go stale when the daemon moves pane. It is an address only:
the sender of anything a worker writes is stamped by the mail daemon from
`HERDR_PANE_ID`.

An agent in a dispatched pane reads that variable to find out where its report
goes, and answers there rather than at any pane written into its text. The
name is the whole contract and is pinned by test against what README documents.

Callers read the variable and answer there, as before, but must stop treating
its value as one fixed pane. Two panes this same daemon opened can now hold
different addresses, because they are answering to different filers; the
verification lane follows the same rule. Anything that cached the value
across tasks, or hard-coded the daemon's pane in its place, is wrong now and
should read the variable in the pane it is running in.

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
