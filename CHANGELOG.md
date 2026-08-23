# Changelog

What a consumer of `hdis` has to change between released versions. §13.3 of
the shared plugin contract makes the CLI, the MCP tool list, the JSON shapes
and the error codes stable within a minor and changeable between minors with an
entry here, so every entry says what moved and what a caller does about it.

## Unreleased

**`make e2e` is layer 3**, out of the gate on purpose: the built binary
against a REAL `htask` compiled from the sibling `herdr-tasks` checkout, over
a real socket, with a fake `herdr`. It skips loudly on a machine with no such
checkout. Nothing a consumer reads changes; it is where the `htask <verb>
--json` integration surface is proved against the real binary rather than
against a script written to agree with this code.

**Board calls no longer carry `HERDR_PLUGIN_CONTEXT_JSON`.** htask resolves
which project it is scoped to from that document before falling back to the
working directory, and Herdr fills it in for the commands it spawns itself —
this plugin's `[[startup]]` among them. A daemon started that way handed it to
every board call, so every call was silently scoped to whatever the operator
was focused on when Herdr started the plugin, and a board scoped elsewhere
reads exactly like a board with nothing ready. It is scrubbed alongside the
three pane names. Nothing a caller passes changes.

**`hdis dump --json` / the `dump` MCP tool** print the whole store in one
document (§5.8): the bindings, the reservations, and the parked actions
including the decided ones, with the file they are written to named. It is a
read; nothing about it changes what the dispatcher does.

**Herdr is feature-detected at daemon start (§11.2).** `herdr api schema
--json` is read once, in either of the two document shapes the section names,
and `tab create`, `pane run`, `pane read` and `agent get` refuse with
`UNSUPPORTED` naming the capability when this Herdr does not list it — instead
of calling it and reading whatever came back. `hdis doctor` gains a `herdr`
object: whether the schema was read, the protocol number (reported and decided
on by nothing), how many requests and events were listed, and every capability
this binary needs that was missing. Nothing changes against a Herdr that
offers everything, which is every released Herdr this plugin supports.

**Breaking: the config, the directories and the environment prefix moved to
the plugin's short name, and the config is TOML.** §10.1 puts a plugin's
config at `<config_dir>/<name>.toml` under its SHORT NAME, which §13.2 fixes
at `dispatch`; `hdis` is the binary abbreviation and names the executable
alone. There is no migration shim, because the only consumer is the operator,
and a shim that silently read the old file would keep the seam it exists to
close. Move your file:

| Was                                      | Is now                                             |
| ---------------------------------------- | -------------------------------------------------- |
| `~/.config/hdis/hdis.json`               | `~/.config/dispatch/dispatch.toml`                  |
| `~/.local/state/hdis/`                   | `~/.local/state/dispatch/`                          |
| `~/.local/state/hdis/hdis.sock`          | `~/.local/state/dispatch/dispatch.sock`             |
| `~/.local/state/hdis/hdis.lock`          | `~/.local/state/dispatch/dispatch.lock`             |
| `~/.local/state/hdis/hdis.log`           | `~/.local/state/dispatch/dispatch.log`              |
| `~/.local/state/hdis/hdis-bindings.json` | `~/.local/state/dispatch/dispatch-bindings.json`    |
| `HDIS_CONFIG_DIR`, `HDIS_STATE_DIR`      | `DISPATCH_CONFIG_DIR`, `DISPATCH_STATE_DIR`         |

The document itself is now TOML, read by a hand-written subset rather than a
new dependency: top-level `key = value`, `[table]` and `[table.sub]` headers,
and values that are quoted strings, whole numbers, `true`/`false`, or one-line
arrays of quoted strings. Every field name, default and refusal is the one the
JSON document already had — only the syntax moved. Anything outside the subset
is refused by line number rather than ignored. The README carries the whole
document in its new form.

`HDIS_DISPATCHER_PANE` does **not** move. It is the variable a worker receives
in its own environment naming the pane it owes its report at — part of the
worker protocol, not this plugin's config prefix — and renaming it would break
every worker already running and every skill that names it.

**There is a §9 policy gate, and two new verbs came with it.** `dispatch` and
`stop` now pass one `gate()` before doing anything, named to a policy as
`dispatch.dispatch` and `dispatch.stop`. With no `gate` key in the config the
gate allows everything and nothing a caller does changes — which is every
fleet that has not configured one. Where one IS configured, a gated call can
come back `DENIED` with the gate's reason, or `DENIED` with a `parked_id`
naming a row the operator resolves; `parked_id` is a new optional field in the
`--json` failure envelope and on the MCP tool error, and a caller that ignores
it reads the envelope exactly as before. The gate fails closed: unreachable,
non-zero, malformed, oversized or slow is `deny`.

`hdis parked list` / `parked_list` and `hdis parked resolve <id>` /
`parked_resolve` are the two new verbs, on both doors, and `hdis doctor` gains
a `gate` object saying whether one is configured, which verbs pass it, and how
many calls are waiting on the operator. The bindings document gains an
optional `parked` array; a document written by a binary without the gate still
reads, and one written with it stays readable to a binary that predates it.

The refuse switch is `hdis parked resolve <id> --refuse`, where the sibling
plugins spell the same switch `--reject`. This binary never rules on a board
submission and a guard here fails on the board's review words appearing as
arguments in its own source.

`hdis version` prints the plugin name and the contract revision beside the
version, and takes `--json` for the same three facts, which is the shape both
siblings already had. A caller parsing the bare line gets more on it than
before; one that wants a field reads `--json`.

**The declared contract revision is now 0.10.0**, up from 0.6.0. It is the
value `hdis doctor` reports as its own top-level `contract`, distinct from the
`board.contract` it relays from htask, and a caller reads it to decide which
contract's rules this daemon answers to. Nothing a caller passes or parses
moves with it.

The number moved because the sweep behind it was run rather than because the
code changed under it: `docs/contract-notes.md` now names, for every MUST that
reaches this plugin, the test that fails when the behaviour is removed — each
verified by removing it. It also records the sections that do NOT reach a
plugin holding no ledger and attributing no call, §3.4, §3.7 and §7.5 among
them, so an absence there is a decision rather than an oversight.

A self-review shot is no longer spent by the call that sent it. §11.4 forbids
reading a successful `agent prompt` as delivery, and the lane was doing exactly
that: the binding was marked the moment Herdr accepted the text, and only a
task LEAVING review cleared the mark — so a prompt that was accepted and seen
by nobody burned the submission's one check with the board still green and
nothing anywhere saying so. The shot is now re-sent while Herdr still calls
that worker idle, bounded by the same `max_prompts` and claim timeout the
unclaimed nudge already uses. A worker that received the condition is working
and never meets a second copy.

Nothing a caller reads changes: no verb, no argument, no `--json` field and no
error code. What moves is how many times a worker that never saw its condition
is asked for it.

The verification lane is now a self-review shot in the worker's OWN pane. A
task reaching review no longer earns a VERIFIER: no second pane, no second
agent, no throwaway checkout. It earns one prompt into the pane that produced
the work, carrying a second condition. `verify.enabled` keeps its meaning — a
submission earns a verification pass — and now buys that shot.

`verify.profile` is GONE, and a config document still carrying it is refused
at parse with the field named. There is nothing left for it to name: the shot
lands in a pane already running the worker's own profile. Callers remove the
field.

`doctor`'s `verify` block loses `profile` and keeps `enabled`; its prose line
now says the lane is a self-review shot in the worker's own pane. `status`
keeps `kind` on each worker row, and `worker` is the only value it takes — the
`verifier` value is gone with the lane. A bindings document naming a verifier
pane still loads, and those records are dropped as debris rather than driven.

What the second condition asks for is the whole point of the change. Not
"review your work": the blind spot that produced the work survives rereading
it, and an independent verifier read past the same five rejections the worker
did. It asks for the mechanical thing — for every guard, refusal or invariant
the report claims, write a COMPILING mutation that removes it, run the tests
the report names, confirm they FAIL, revert — and then for which mutations bit,
which did not, and whether the worker reads each miss as a missing test or bad
aim. It names where that goes: the mail door at `$HDIS_DISPATCHER_PANE`. A
worker whose task is in review cannot amend its own report, because `htask`
refuses a submit on a row that is not `doing`, so findings with no route named
die in the pane.

Nothing about review authority moves. The shot produces no verdict: the task
stays in review and the operator still approves or rejects.

The pane cap is now derived from a measured HEIGHT as well as a measured
width. `max_panes_per_tab` stays 16, but for the first time that number is an
answer to both axes rather than to one of them with the other unexamined. Only
the EVEN generations of the grid rule split sideways, so reaching sixteen
panes spends two generations on rows, and nothing had ever measured what a
worker's detection text needs in rows. `herdr pane read --source detection`
returns the BOTTOM of a pane's buffer, so a pane too short does not wrap a
marker — it scrolls the marker off the top and hands back a snapshot that no
longer holds it.

The row floor cannot be derived from a phrase's length the way
`MeasuredReadableColumns` is, because what a marker costs in rows is where it
sits in the block the dialog renders. So the coupling is pinned instead.
`config.MarkerRows` carries a measured row cost for every marker in use, keyed
by the phrase, and `TestTheReadableRowFloorIsDerivedFromTheMarkerSets` fails
when a marker is added, removed or reworded without a height measured for it,
and when the table names a phrase nothing matches on any more. A read matches
an OR, so it costs its cheapest marker; the floor is the tallest of the reads.

Measured against herdr 0.8.2 and claude 2.1.239, one tab per height at exactly
40 columns with a real Claude in each, eighteen heights from 67 rows down to
2: `yes, i trust this folder` reads whole at 4 rows and is gone at 3, and the
Enter that answers the dialog lands at 4 too; `/goal active` reads at every
height down to 2. `do you trust the files in this folder` renders at no height
on this build and `goal set:` scrolls with the transcript rather than the
pane, so both carry `config.RowsNotDependable` and neither can lower a floor.
`config.MeasuredReadableRows` is 4.

A whole tab measured 69 rows, and a down split costs measured chrome before it
halves (69 gave 33 and 32; 33 gave 16 and 15), so `config.SplitRowCost` is 4
and `config.ShortestRows` runs 69 rows for one and two panes, 32 for three
through eight, 14 for nine through sixteen, and 5 from thirty-three. Against a
4-row floor the rows do not give out until 128 panes, four generations past
where the width does.

`config.MaxPanesClearing` takes both floors as arguments and
`DefaultMaxPanesPerTab` is what it returns, so the number now says which floor
decided it. The test pins what each floor allows alone — 16 for the columns,
128 for the rows — and a derivation that consults only one of them stops
matching.

The report address gained a middle rung. `HDIS_DISPATCHER_PANE` was the task's
pane of origin, else the daemon's base pane, and a task an operator filed at a
terminal names no pane — so every report for one went to whoever started the
daemon, even while another live session was sitting in that very repository
and was relayed the report by hand. Between the two rungs the daemon now takes
a LIVE pane whose cwd is the task's project, read from `herdr pane list`.
Among several, the lowest pane id wins, because that is the answer that stays
the same across ticks; a pane sitting under this daemon's own checkout root is
never chosen, so a worker for that project does not become the address. The
base pane stays as the last rung, so a machine with nothing live still answers
somewhere. The workspace a worker's tab opens in comes from the same resolved
desk rather than a second rule of its own, and the pane list is read once per
spawn instead of twice. Nothing a caller passes changes: the CLI, the MCP tool
list, the JSON shapes and the error codes are untouched, and the board stores
nothing new.

The trust-folder dialog is answered again. `TrustDialogMarkers` held one
phrase, `"do you trust the files in this folder"`, and claude 2.1.239 reworded
that dialog, so `answerStartupDialog` matched nothing at any pane width and
never pressed the Enter it exists to press. Since every worker worktree is a
fresh untrusted directory and `herdr agent start` there returns
`agent_not_ready` with the dialog still on screen, that Enter is the only
thing that lets a goal be delivered. The markers are now a SET, matching the
new dialog's selectable option `"yes, i trust this folder"` alongside the
older phrase, so an operator on either claude build is answered. Nothing a
caller passes changes: the CLI, the MCP tool list, the JSON shapes and the
error codes are untouched. `config.MeasuredReadableColumns` stays 40 — the new
phrase is 24 characters against the older one's 37, so the longest marker the
floor derives from did not move — and a test now derives that floor from the
marker set instead of restating it.

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
never on the tick. A git that cannot answer is an error rather than the answer
"behind": the row stays unmarked and the reason goes to the log, because
marking every worker behind would send the operator to rebase branches that
were never behind. Nothing here rebases, merges or serialises dispatch: the recovery is
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
narrowest pane whose detection text still reads correctly, and 16 is what the
grid rule holds at that floor and at the row floor below it.
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
file. A record written before the field existed carries none, and the line
reads `detached` for it.

The bindings file gains a `branch` on each record. A binding written by an
older hdis simply has none, and both files stay readable to both binaries. The
restart reap covers every checkout this daemon made: it is bounded to
`<state_dir>/worktrees` and to entries carrying the `hdis-` prefix this binary
names its own with.

Declares the shared plugin contract at 0.6.0, up from the 0.4.0 of the first
release. §5.1/§10.1 forbid resolving a store from the Herdr-injected plugin
dirs, which this binary has no store to resolve and never read; §11.4 states
that prompt delivery is best-effort with the authoritative record elsewhere,
which here is the board; the 0.6.0 amendment reshapes §6.6 into recusal by
session, and this plugin performs no reviews. Nothing in the binary moved for
any of the three.

Callers read the new value in `doctor`'s `contract` field. Nothing else to do.

There is a verification lane, and it is off unless the config document turns it
on. With it on, a task in review that this daemon's own worker submitted earns
a SELF-REVIEW SHOT: one `/goal` armed in the worker's own pane, asking it to
write a compiling mutation against every guard its report claims, run the tests
the report names, confirm they fail, revert each one, and say which mutations
bit. It never approves and never rejects, and neither does this binary — the
board adapter carries no review verb, and no source file passes one as an
argument. One submission earns one shot; a task leaving review rearms the lane,
so a re-submission after a rejection earns another.

Callers turn it on with a `verify` object in the config document carrying
`enabled` and nothing else. The lane no longer launches a pane of its own, so
there is no profile to name: a document that still carries `verify.profile` is
refused at parse, by name, rather than starting a daemon whose operator
believes a separate verifier is running. Left out, the lane is off and nothing
changes.

`doctor`'s JSON gains a `verify` block carrying `enabled`, and nothing beside
it: the shot lands in a pane that is already up, so there is nothing else to
name. Its prose output names the lane too, before the board line, so an
unreachable board does not hide the dispatcher's own configuration. `status`'s
JSON gains `kind` on each worker, which is always `worker` — there is one
lane, and the field says so rather than leaving a caller to infer it.

Callers that parse `doctor` or `status` see two added fields and no removed
ones. A parser that ignores unknown fields does nothing.

The bindings are durable, and a restart reconciles what it left behind. They
are written to `${XDG_STATE_HOME:-~/.local/state}/hdis/hdis-bindings.json` on
every change, whole and atomically. At the next start the daemon asks one
question of every live pane it opened — what is this, and what still needs
doing — answering it from Herdr's agent list and the board rows rather than
from the bindings, which stay as a hint. A pane still working its task is
adopted, a reservation no live pane is working is dropped, a task that reached a
terminal state or left review takes its pane with it, and every
`hdis-` checkout under this daemon's own worktree root that no binding
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
