# herdr-dispatch

The dispatcher for the [herdr-tasks](https://github.com/husniadil/herdr-tasks)
board: `hdis` watches for ready tasks, brings up a worker agent in a
[Herdr](https://herdr.dev) pane for each one, delivers the task's goal, tracks
the worker, and hands off at review — where the board's own review gate takes
over. Any agent can also ask it for a worker on demand, through its CLI or
over MCP. The board stays the ledger; this binary is only execution policy.

## Running it

```sh
make install
hdis daemon        # or `hdis run`, which is the same thing
```

One binary is the daemon and both doors. The daemon owns the tick and the
bindings; the CLI and the MCP server are thin clients of it and hold nothing
of their own. There is one daemon per user, elected by a lock at
`$XDG_STATE_HOME/dispatch/dispatch.lock`, answering on a private socket at
`$XDG_STATE_HOME/dispatch/dispatch.sock`. A second one refuses to start with
`CONFLICT: ALREADY_RUNNING` rather than driving the same board alongside the
first.

**The daemon opens its own log.** It appends to
`$XDG_STATE_HOME/dispatch/dispatch.log`, beside the socket, the lock and the
bindings, whatever the shell line that started it redirected. Every line goes
to stdout as well, so an operator running it in the foreground sees exactly
what the file gets; the one exception is a daemon a door started, whose
stdout IS that file already, where writing to both would double every line.
`-log` moves the file and defaults to that path, so a redirect stays possible
and is never required. A log that cannot be opened is said on stdout and the
daemon starts anyway — refusing to dispatch because a file will not open is
worse than dispatching where the lines can still be read. `hdis doctor` names
the file that was actually opened, and says `stdout only` when none was.

`hdis daemon -h` lists the knobs:

| Flag | What it sets |
| --- | --- |
| `-config <path>` | The config document. Defaults to `<config_dir>/dispatch.toml`. |
| `-log <path>` | The file the log is appended to. |
| `-interval <duration>` | How often to tick. |
| `-once` | Run one tick and exit, instead of listening. Nothing serves either door in this mode. |
| `-pane <id>` | The pane worker panes are split off. |
| `-max-workers <n>` | How many workers may be live at once; `0` means the config's `max_workers`. |
| `-claim-timeout <duration>` | How long a delivered goal may go unclaimed before a nudge. |
| `-max-prompts <n>` | How many times one task's goal may be delivered before giving up. |
| `-start-timeout <duration>` | How long herdr waits for a worker to become interactive. |
| `-confirm-ceiling <duration>` | How long to wait for a delivered goal to show on the worker's screen. |

Worker panes are splits of a base pane, so the daemon needs one: it takes
`HERDR_PANE_ID` when it was started inside a Herdr pane, `-pane` when it was
given one, and the config's `"pane"` key otherwise. Without any of the three
it still comes up and still answers both doors, but it does not tick and
every dispatch refuses with `UNSUPPORTED: NO_BASE_PANE`. Every spawn it could reach for
would fail on the same missing pane, once per interval forever, and a log of
one error repeated is a log nobody reads.

At startup it reads `htask doctor --json` and says what is unreachable. It
says it rather than obeying it: a board that is down comes back, and `doctor`
and `status` are exactly what an operator wants to ask while it is down.

## As a Herdr plugin

`herdr-plugin.toml` at the root is what Herdr installs: it compiles
`./bin/hdis`, starts the daemon through `scripts/start.sh` — which detaches it,
so it outlives the pane that opened it — and offers **Stop the dispatcher** and
**Restart the dispatcher** as workspace actions. Herdr has no shutdown hook, so
those two actions are the route to actually turning the plugin off; unlinking
it leaves the daemon running. Stopping writes nothing to the board.

The manifest's version is the version `hdis version` prints, and a named test
fails the gate when the two drift apart.

Then link the agent skill, which the install does not place for you:

```sh
root=$(herdr plugin list --plugin herdr-dispatch --json | jq -r '.result.plugins[0].plugin_root')
ln -s "$root/skills/dispatch" ~/.claude/skills/dispatch
```

Herdr keeps an installed plugin under `~/.config/herdr/plugins/github/`, in a
directory named for the plugin id and a hash of where it came from — ask for
`plugin_root` rather than writing that path out, because the hash is not
something you can predict.

To develop against a checkout:

```sh
make build && make test-full
herdr plugin link .
ln -s "$PWD/skills/dispatch" ~/.claude/skills/dispatch
```

The symlink is what puts the skill in front of an agent — nothing in the Herdr
manifest installs it, because the skill is read by the harness and not by
Herdr. Link it rather than copy it: the checkout stays the single source, and a
copy is a second version of the truth from the next commit onwards. The MCP
door carries the same facts in its instructions, but only for a client that
has the door wired in; the skill is what a harness without it ever reads.

This plugin satisfies **version 0.10.0** of the Herdr plugin contract, swept
section by section in [`docs/contract-notes.md`](docs/contract-notes.md).
`hdis doctor` says so in both its shapes, as its own top-level `contract`
field, distinct from the `board.contract` it relays from `htask`.

### Where the contract is written for a plugin this one is not

§3.4, §3.7 and §7.5 are one rule seen three times: who a call is attributed
to, and how the trail says so. **This plugin attributes nothing.** It holds no
ledger, records no actor, and has no verb whose authority is anyone's in
particular — the daemon's own README says it above: the caller's identity buys
nothing, because every caller here is the operator's own tooling reaching a
socket only the operator can open.

So §7.5's `--operator` is not implemented, and that is a gap in the contract
rather than one here. The flag exists so a door's principal is settled before
a call arrives, and §7.5 names what makes that visible: "`<name> doctor`
(§10.3) already prints the calling principal". §10.3 requires no such thing,
and this door has no principal for it to print. A flag read by nothing and
recorded nowhere would be conformance theatre — it would change no
attribution, because there is none to change.

The line to watch is the one place identity DOES matter: the board principal
this daemon writes with, `plugin:hdis@<its own pane>`, which is declared to
htask through `--as` and scrubbed of `HERDR_PANE_ID` first so the board can
tell a plugin from an agent. That is htask's rule, tested here, and it is the
whole of this plugin's stake in §3.

## The two doors

Both are generated from one verb table, and a parity test drives a live MCP
session against it: a verb on one door and not the other fails the gate. There
is no asymmetry left for the table to declare — every verb is on both doors,
which is what §7.3 asks for, and a verb reachable by shell alone would be
unreachable to a harness that has no shell.

| Verb                  | MCP tool         | What it does                                     |
| --------------------- | ---------------- | ------------------------------------------------ |
| `hdis doctor`         | `doctor`         | Why a dispatch would refuse, before one is tried |
| `hdis dispatch <task>`| `dispatch`       | Reserve one ready task for the next tick         |
| `hdis stop`           | `stop`           | Ask the running daemon to shut down              |
| `hdis status`         | `status`         | What the dispatcher is driving now               |
| `hdis dump`           | `dump`           | The whole store as JSON (§5.8)                   |
| `hdis parked list`    | `parked_list`    | Calls the policy gate deferred to the operator   |
| `hdis parked resolve <id>` | `parked_resolve` | Let one through, or close it unrun          |

`stop` is the one verb whose blast radius is not one task, and its description
on both doors says so. Every worker the dispatcher is driving keeps running in
its pane and no new one comes up until a daemon is started again, so a caller
confirms with the operator first — the duty §3.7 puts on an agent, taught where
the caller reads the verb rather than enforced by keeping it off a door where
withholding never refused anybody: `hdis stop` is a CLI subcommand, so any
agent with a shell could always run it. It answers `CONFLICT: NOT_RUNNING`
when nothing is listening: the other verbs start a daemon when none answers,
and starting one just to stop it is the opposite of what was asked. What it
does not do is write to the board — a worker mid-task keeps its claim and its
lease, and htask times those out itself.

Every verb takes `--json`, and those bytes are the same document the MCP tool
hands its caller.

```sh
hdis dispatch 7
hdis status --json
```

A door that finds no live socket starts the daemon and waits for it, bounded
at three seconds, rather than fail. A daemon started that way has no terminal
to write to, and its log goes where every daemon's does.

Wire the MCP door into any client that speaks stdio MCP:

```json
{ "command": "hdis", "args": ["mcp"] }
```

**`dispatch` does not wait for a worker.** Bringing one up runs past three
minutes in the worst case — the pane's shell, the agent's startup, a trust
dialog that may never come, and the wait for the goal to show on screen — and
no MCP client holds a tool call that long. So `dispatch` validates the task
against the board's own ready list, reserves it, and returns; the next tick
does the work. Read the outcome with `status`. The reservation is also what
keeps the watching loop and the dispatch verb from both taking one task.

It refuses with a name rather than a sentence to parse. The code is one of the
contract's own nine (§6.3), and the sub-reason this binary refused for is the
first word of the message, so nothing a caller could branch on before is lost:
`USAGE` when no task was named; `CONFLICT` as `NOT_READY` when the board will
not hand the task out, as `AT_CAPACITY` when `-max-workers` are already live
or reserved, or as `ALREADY_DISPATCHED` when this daemon is already driving
it; `UNSUPPORTED` as `NO_BASE_PANE` when there is nowhere to put a worker;
`NOT_FOUND` when the board has no such task; and `UNAVAILABLE` when the board
itself could not be read. The exit status is the one §6.3 fixes for the code —
2, 3, 4, 6, 7 and the rest — and with `--json` a failure is exactly one
`{"error":{"code":…,"message":…}}` document on stdout, the same envelope the
MCP door builds.

**Which profile a worker launches with is not selectable per call.** It is
decided by the config and nowhere else, for the same reason the board carries
no profile field: execution policy is this binary's business.

**The caller's identity buys nothing.** The daemon records the pane a caller
ran in, or `unknown` for a caller on another harness, and grants nothing for
it. Every caller here is the operator's own tooling reaching a socket only
the operator can open, and the board only ever hears from this binary as
`plugin:hdis` whoever asked.

## What this Herdr can do

At daemon start `herdr api schema --json` is read once, and what it says is
what the verbs decide on (§11.2). Both document shapes the section names are
accepted: the JSON Schema Herdr prints today, whose request methods are the
`const` values under `schemas.request.oneOf[].properties.method` and whose
event kinds are the enum at `schemas.event.$defs.EventKind`, and the flat
`{"requests":[…],"events":[…]}` form a simpler future one might print. The
protocol number is reported and decided on by nothing: §11.2 calls pinning one
a contract violation.

A capability this binary needs and Herdr does not list is `UNSUPPORTED` **at
the verb that needs it, naming it** — never a refusal to start, and never a
verb that runs anyway. `hdis doctor` says which are missing before a dispatch
is tried:

```
  herdr api   protocol 1, 14 requests, 1 events, MISSING tab.create: the verbs that need one refuse UNSUPPORTED
```

A Herdr that could not be asked is not a Herdr that offers nothing, so the
answer is not cached and the next verb asks again.

## Reading the store

`hdis dump --json` prints everything this daemon remembers across restarts in
one document (§5.8): the bindings, the reservations no tick has spawned for
yet, and the parked actions, decided ones included. It is the daemon's own
live set rather than a re-read of the file, so it is what the next save will
write, and the document names the file so a reader who wants it without this
binary knows where to look.

Every list is `[]` when it is empty rather than `null`: a reader has to be
able to tell "none" from "this daemon could not say". Nothing in it is a board
fact — task state, claims, leases and evidence are htask's, and `htask` is
where they are read.

## The policy gate

Every verb that changes the world passes one gate before anything happens
(§9.1). Here that is two verbs, and they are named to a policy in the form
§9.4 fixes:

| Gated verb          | The call it is asked about                       |
| ------------------- | ------------------------------------------------ |
| `dispatch.dispatch` | Reserving a ready task and bringing a worker up  |
| `dispatch.stop`     | Shutting the whole dispatcher down               |

The other verbs are not gated, and each says why rather than leaving it to be
inferred: `doctor` and `status` only read, and `parked resolve` is the answer
to a gate that has already spoken — gating it would let a gate park its own
resolution and strand every deferred call.

The gate is **configured, not built in** (§9.2). With no `gate` key the gate
allows everything, which is what most fleets want and what `hdis doctor` says
on its `gate` line. Set it to a command and every gated call runs it:

```toml
gate = ["/usr/local/bin/fleet-policy", "check"]
```

The command reads one JSON document on stdin — `{"subject","verb","target"}`,
where the subject is the pane the caller ran in (`agent:wM:p3`) or `unknown`,
and the target is the task the call named — and prints one back:

```json
{ "decision": "allow" }
{ "decision": "deny",  "reason": "not during a release freeze" }
{ "decision": "defer", "reason": "a human should look at this one" }
```

**The gate fails closed.** Unreachable, non-zero, malformed, oversized,
unknown decision, no answer within five seconds: every one of them is `deny`,
and the refusal carries which. A gate that cannot answer has not allowed
anything.

`defer` means park it (§9.3). The call is recorded and not performed, and the
caller is refused with `DENIED` whose envelope carries a `parked_id`:

```json
{ "error": { "code": "DENIED", "message": "the policy gate parked dispatch.dispatch for the operator: a human should look at this one", "parked_id": "pk-1774310400000-3f9a12c7" } }
```

The row waits in the same document the bindings live in, so it survives a
restart — an operator decides at their own pace, and a deferral that vanished
would be a call its caller was answered for and nobody can find.

```sh
hdis parked list                     # what is waiting, and why
hdis parked resolve pk-…             # let it through
hdis parked resolve pk-… --refuse    # close it, the verb never runs
```

Resolving **re-runs the verb under the subject the gate stopped**, never the
resolver's, and does not consult the gate again — the resolution is the
decision the gate deferred, and a second ask would park it forever. The row
records who resolved it, because otherwise the only trail names the caller
that was stopped and nobody who decided it could proceed. Only one resolve
wins; the second meets a row that has already been decided rather than a
dispatch that has already happened twice. And a row the operator let through
whose verb then errored is marked `failed` and stays in front of them, because
a call that did not happen must not read like one that did.

The switch is spelled `--refuse` where the sibling plugins spell it `--reject`.
This binary never rules on a board submission, and a guard here fails on the
board's review words appearing as arguments in its own source; keeping that
guard sharp is worth more than matching a sibling's spelling for a switch
nothing shares.

## Configuration

`hdis` reads `$XDG_CONFIG_HOME/dispatch/dispatch.toml`
(`~/.config/dispatch/dispatch.toml`), which is where §10.1 puts a plugin's
config under its SHORT NAME. `hdis` is the binary abbreviation §13.2 leaves to
each plugin, and it names the executable and nothing else — a policy gating
`dispatch.dispatch` and an operator opening `~/.config/dispatch/dispatch.toml`
are the same plugin under the same word.

It holds worker profiles — the launch preset a worker is assembled from — one
global default, and per-project overrides:

```toml
default = "worker"

[profiles.worker]
provider = "claude"

[profiles.routed]
provider = "codex"
agent = "claude"
model = "sonnet"
effort = "medium"
args = ["--add-dir", "/srv/shared"]

[projects]
"/Users/me/github.com/me/some-repo" = "routed"
```

The reader is a hand-written subset of TOML rather than a dependency: top-level
`key = value`, `[table]` and `[table.sub]` headers, and values that are quoted
strings, whole numbers, `true`/`false`, or one-line arrays of quoted strings.
A project path is a key, so it is quoted. Anything outside that subset — an
inline table, an array of tables, a multi-line array, an unquoted string, a key
set twice — is refused by line number rather than ignored, because a setting an
operator wrote and this binary silently dropped is the failure a config parser
exists to prevent.

| Field      | Meaning                                                                                                                    |
| ---------- | -------------------------------------------------------------------------------------------------------------------------- |
| `provider` | `claude` runs the plain binary. `codex` runs it through the proxy launcher named below, which supplies the routing. Required. |
| `agent`    | The `--agent` name. Defaults to the literal `claude`. Definitions belong to each project's `.claude/agents`; none ship here. |
| `model`    | A tier alias. Empty means the client's own default.                                                                          |
| `effort`   | Defaults to `low`.                                                                                                           |
| `args`     | Extra argv passed through to the worker.                                                                                     |

Six keys sit at the top level beside `default`, `profiles` and `projects`:
`proxy` names the codex provider's launcher, `pane` names the base pane a
daemon uses when it was not started inside a Herdr pane and was given no
`-pane`, `max_workers` is how many workers may be live at once, `gate` is the
§9 policy gate command, `[layout]` carries `min_pane_columns` and
`max_panes_per_tab`, and `[verify]` is the verification lane.

The lane is off unless the document turns it on. On, every task a worker of
this daemon's submits earns one self-review shot in that worker's OWN pane —
one shot the worker RECEIVES, which is not the same as one call made:

```toml
[verify]
enabled = true
```

It names no profile, because nothing separate launches: the shot lands in a
pane that is already up, running the profile its worker was launched from. A
document still carrying `verify.profile` is refused at parse with the field
named, rather than accepted as a no-op — an operator who set it believes a
verifier is running. `hdis doctor` reports whether the lane is on and says
what it buys. What the shot asks for, and the line it does not cross, is
under [The boundary](#the-boundary).

The `codex` provider's launcher is named by an optional top-level `"proxy"`
key, and defaults to the literal `proxenos`. It lives in the config rather
than in this binary because that binary has been renamed once already, and
the next rename should be one line of config:

```toml
proxy = "/opt/homebrew/bin/proxenos"
```

Which profile a project gets is decided here and nowhere else. The board
carries no profile field, deliberately: which agent kind and model a worker
runs as is execution policy, and execution policy does not belong in the
ledger.

### What `max_workers` bounds

`max_workers` reads two ways, and the number itself says neither. This is the
one the code implements: max_workers bounds how many worker panes may exist at
once, and not how many agents may be spending tokens at once.

A pane that has submitted and is awaiting review spends nothing and still
holds its slot. That is on purpose — a rejection puts the row back to `doing`
and the same pane carries on, because the conversation is there and nowhere
else — but it means the number is a screen and memory bound. Raising it buys
panes. It does not buy throughput on a board whose slots are all held by
panes waiting for a human.

So that the wait is not invisible, both `hdis status` and `hdis doctor` say
when a slot is held that way, in the same words:

```
$ hdis doctor
  workers     4 live (2 holding a slot while awaiting review), 0 reserved, max 4

$ hdis status
#24   wM:p4V     hdis-24  idle       hdis/task-24   prompted ... notified=true  submitted work  (holding a slot while awaiting review)
```

The JSON carries the same facts: `awaiting_review` is a count on the doctor
report and a boolean on each worker in `status`.

### Sharing the slots between boards

The dispatcher serves every board it can read, and the ready list arrives in
whatever order the boards are walked. Taken in that order, one board offering
more ready work than there are slots takes them all, and a second board waits
however long that takes.

So the ready list is dealt round-robin by project before anything is spawned:
each board gets its first task before any board gets its second. Projects keep
the order they first appeared in and each board's tasks keep the order it
offered them, so a machine serving a single board spawns in exactly the order
the board gave — the rule costs nothing where there is nothing to be fair
between.

There is no per-project cap. One global number, shared out fairly, is the
whole of it.

## What a spawn actually does

1. For a `codex` profile, `proxenos settings` first. A daemon that is
   down fails here, in the daemon's own words, rather than thirty seconds
   later as a startup timeout with the cause hidden in a pane.
2. `herdr tab create` in the filer's workspace, in the task's own checkout,
   with `--label "hdis task <n>"`, `--env HDIS_DISPATCHER_PANE=<the report
   address>` and `--env FORCE_PROMPT_CACHING_5M=1` — see
   [Where a worker comes up](#where-a-worker-comes-up),
   [The dispatcher's address](#the-dispatchers-address) and
   [The worker's prompt cache](#the-workers-prompt-cache).
3. For a `codex` profile, the routing arrives in two halves. The settings
   document goes to a private file and is spliced into the worker's argv as
   `--settings <path>`, because that argv is TYPED into the pane and the
   document inline was most of what made the line long enough to break.
   `eval "$(proxenos env)"` runs in the pane itself, so the worker inherits
   the environment half as a direct child of the shell.
4. `herdr agent start`, once herdr agrees the pane's shell is free to take it
   — `agent_pane_busy` is herdr's own refusal and the only signal worth
   retrying on, since the `eval` above is still running when the start would
   otherwise arrive. It carries the profile's argv and a short `/goal`
   condition after the separator. That condition is a **pointer** hdis
   composes — claim the task with `htask task claim <n>`, read its full
   criteria with `htask task get <n>`, and finish by submitting it for review
   with a report and evidence — and never the board's rendered goal document.
   The line is
   TYPED into the pane, character by character, and a long one intermittently
   arrives broken: two live runs came out with the condition cut mid-word and
   the command's own start typed over what followed, one at ~2.2k characters
   and one at ~1.4k, while the same text piped into a bare shell was clean to
   a megabyte. So the criteria stay on the board, where the worker reads them
   whole and no shell ever types them, the settings document travels as a
   path rather than inline, and what is left of the typed line is held under
   a budget (`spawn.TypedLineBudget`) that a named test measures.

   `htask task goal <id> --one-line` is no longer part of this pipeline. It
   remains the operator's paste-ready form for arming `/goal` in a pane by
   hand, where nothing is typed character by character.
5. If Claude Code's trust-folder dialog appears, exactly one Enter — and only
   if it is seen on screen. A dialog that never comes is never answered.
6. Delivery is confirmed from the pane, never from `agent start`'s exit. That
   return is inverted for this path: a goal that registers drives the worker
   past interactive readiness, so the command times out, and a goal that is
   refused leaves the worker idle, so the command succeeds. A spawn that
   cannot confirm the goal retires its own half-built pane.

## Where a worker comes up

**A worker never shares a human's tab.** It comes up in a tab this daemon
created with `herdr tab create`, and the operator's own tab is never split to
make room for it.

That is a correctness rule and not a tidiness one. Everything this daemon
knows about a worker it reads off the worker's SCREEN — `herdr pane read
--source detection`, matched against the phrases a startup dialog and a
registered goal leave behind. Every pane added to a tab narrows every other
pane in it, and a pane narrow enough word-wraps the very phrase the match is
looking for, so the match fails while the worker is perfectly fine. That is
the daemon believing the wrong thing about a worker it is driving.

**The workspace is the desk's.** The tab is created in the workspace of the
pane the report is addressed at, when Herdr is still holding that pane, and in
the daemon's own workspace otherwise. It is the SAME resolver behind both
answers — the pane the task was filed from, else a live pane sitting in the
task's project, else the daemon's own, resolved once per spawn from one read
of `herdr pane list`: see
[The dispatcher's address](#the-dispatchers-address). This daemon serves every
board on the machine, so the operator who started it is routinely not the
operator who filed the task, and a worker belongs on the screen of whoever
wanted it. Liveness is checked at the moment of the spawn: an address can fall
back lazily because it is only read on demand, a placement cannot. A pane of
origin the operator has since closed falls back rather than failing the spawn
— a task filed from a window that is gone is an ordinary task.

**A tab belongs to ONE task.** The label already carries the task number, so
placement is a comparison rather than new state: a worker goes in the tab
opened for ITS OWN task, or a new tab is opened for it. A tab this daemon
opened for a DIFFERENT task is not a candidate however much room it has,
because a tab holding two tasks names only one of them and the label stops
being the signpost half of its job.

**Inside that tab the panes make a grid.** Panes are added in generations,
each twice the size of the one before, and every pane in a generation splits
the pane one generation back with an explicit `--ratio 0.5`: the second goes
right off the first, the third DOWN off the first, the fourth down off the
second. Four panes are then four equal rectangles, a 2x2. Splitting off the
LAST pane instead — which is what shipped before — gives a column beside a
stack and a fourth pane a quarter of the width. The rule is
`config.GridSplit`, and five panes give a left column of three and a right
column of two, six give three and three.

A tab already holding `layout.max_panes_per_tab` panes takes no more: the next
worker opens another tab, in the same workspace. Because a tab holds one task,
that cap bounds the panes ONE task may have — one today — and it is the
readability floor guard and nothing else. It is not what keeps two tasks
apart.

**The cap is measured, not guessed.** See
[The readable width and height](#the-readable-width-and-height).

**A tab this daemon creates is a thing it owns and must give back.** It is
closed when its LAST worker leaves, its id is written on the binding so a
restart can still give it back, and a tab the operator made is NEVER closed
here — the label is the guard, exactly the way the worktree reap is bounded by
its own root and prefix. A worker the operator has dragged into a tab of their
own is retired as a pane, and their tab stays.

The label is written for two readers at once. `hdis task 41` on a tab is
something a person can pick out of a row of tabs, which is the one thing a tab
has that a pane does not, and `hdis status` prints it beside the pane. It is
also what tells this daemon the tab is its own.

## The readable width and height

`layout.min_pane_columns` is the narrowest pane whose detection text this
repo's own matches still read correctly, `config.MeasuredReadableRows` is the
shortest, and `layout.max_panes_per_tab` is what BOTH work out to as a count.
All three were MEASURED rather than guessed, and the measurements are recorded
beside them in `internal/config/config.go`.

Measured on 2026-08-23 against herdr 0.8.2 and claude 2.1.239, in a throwaway
workspace that was torn down afterwards: nine panes of measured width — 21,
23, 25, 27, 29, 31, 38, 43 and 52 columns, each read back with `stty size` —
a real Claude worker brought up in every one, and each given the real
`PointerGoal` condition.

- At **21 columns** only `goal set:` survived. `/goal active` was truncated by
  the status line, so one of the two markers was already unreadable.
- At **23 columns and above** both goal markers read whole, every time.
- Claude renders its dialog and status body at the pane's columns **minus
  three**, word-wrapped: in the 52-column pane the longest body line was 49
  characters.

The floor follows from that last rule and the LONGEST phrase the detector
matches, which is the 37-character trust-dialog marker: it needs 37 + 3 = 40
columns to land on one line, and a phrase that wraps is a phrase that never
matches. So **40 columns**, the widest requirement of any marker in use, and
not the 23 the goal markers alone would have allowed.

A whole tab measured 226 columns. Under the grid rule only the EVEN
generations split sideways, so a pane's width halves once every two
generations and the odd ones spend themselves on height: the widths are 226
for one pane, 113 for two through four, and 56 for five through sixteen. The
seventeenth starts the generation that halves 56 to 28, which is under the
floor. Taken alone that allows **16** panes. `min_pane_columns` may be raised
and never lowered below what was measured.

### The readable height

The height is the half the first derivation left out. `herdr pane read
--source detection` returns the BOTTOM of a pane's buffer, so a pane too short
does not wrap a marker — it scrolls the marker off the top and hands back a
snapshot that no longer holds it.

It cannot be derived from a phrase's LENGTH the way the column floor is: what
a marker costs in rows is where it sits in the block the dialog renders, not
how wide it is. So the coupling is PINNED instead. `config.MarkerRows` carries
a measured row cost for every marker in use, keyed by the phrase itself, and
`TestTheReadableRowFloorIsDerivedFromTheMarkerSets` fails when a marker is
added, removed or reworded without a height being measured for it, and when
the table names a phrase nothing matches on any more. A read matches an OR, so
it costs its CHEAPEST marker; the floor is the tallest of the reads.

Measured on 2026-08-23 against herdr 0.8.2 and claude 2.1.239, in a throwaway
workspace that was torn down afterwards: one tab per height, a git repo of its
own per tab so the trust dialog is raised rather than remembered, every pane
pinned to exactly **40 columns** (where the dialog wraps hardest of any width
a worker is allowed) and to a measured height, a real Claude brought up in
each with the real `PointerGoal` condition, each pane read back through `herdr
pane read --source detection`, then answered with an Enter and read again.
Heights probed at 40 columns: 67, 24, 22, 20, 18, 17, 16, 14, 12, 10, 8, 7, 6,
5, 4, 3 and 2 rows.

- `yes, i trust this folder` — the new dialog's selectable option, two lines
  above `Enter to confirm · Esc to cancel` and the last thing in the block to
  go. Read whole at **4 rows**; at 3 the snapshot held only `Enter to confirm ·
  Esc to cancel`. The Enter that answers the dialog landed at 4 rows too, so
  the whole answer-the-dialog step works there and not only the match.
- `do you trust the files in this folder` — the older build's phrase. Absent
  from all eighteen snapshots at every height, because claude 2.1.239 does not
  render that dialog at all.
- `/goal active` — the status line, pinned to the bottom row. Read at every
  height down to the **2 rows** that were the shortest pane herdr would give.
- `goal set:` — the echo of the condition. Survived at 30 and 67 rows and was
  already gone at 24, but that boundary moves with how much the worker printed
  rather than with the pane, so it is not a height to lean on.

The last two of those carry `config.RowsNotDependable` rather than a number: a
phrase this build never renders and a phrase that scrolls with the transcript
can neither of them be what makes a read work. The trust read then costs 4
rows and the goal read 2, so `config.MeasuredReadableRows` is **4**.

It was **17** for a day. While `TrustDialogMarkers` held only the older
build's phrase, the floor had to be measured against the dialog's own top
sentence — `Quick safety check: Is this a project you created or one you
trust?`, which read whole at 17 rows and was cut at 16. Matching the option
line instead moved the trust read to the bottom of the block. That is the
drift the pin above now catches.

A whole tab measured 69 rows — a different window from the 226-column one, and
each constant is honest about the window it was taken in. A down split also
costs chrome before it halves: a 69-row pane split downwards measured 33 and
32, and splitting the 33 again gave 16 and 15, so `config.SplitRowCost` takes
the larger of the two because this number guards a floor. The heights are then
69 rows for one and two panes, 32 for three through eight, 14 for nine through
sixteen, and 5 from thirty-three. Against a 4-row floor the rows do not give
out until **128 panes**, four whole generations past where the width does.

So the cap is **16**, the tighter of the two and the same number the column
axis alone gave. What changed is that it is now the answer to BOTH floors
rather than to one of them with the other unexamined.

`layout.max_panes_per_tab` is unaffected by an operator setting of `2`: at two
panes the grid rule and the old rule agree, and neither floor is near.

`TestTheMaxPanesPerTabDefaultIsTheMostBothMeasuredFloorsAllow` derives the cap
from both floors and pins what each one allows alone — 16 for the columns, 128
for the rows — so a derivation that drops either floor stops matching.
`TestShortestRowsFollowsTheGridRuleAndTheMeasuredSplitCost` pins the row
ladder itself.

One thing the measurement is NOT: it is not the reason a narrow pane loses a
typed line. At 25, 27 and 29 columns the condition arrived whole and only the
Enter was lost, which an explicit send-keys then delivered.

The floor is derived, never restated beside the markers it follows from:
`TestTheReadableColumnFloorIsDerivedFromTheLongestMarker` recomputes it from
`TrustDialogMarkers` and `GoalMarkers` and fails if the two drift apart.

## The trust-dialog wording

Claude rewords its trust-folder dialog between builds, so `TrustDialogMarkers`
is a SET, the way `GoalMarkers` already is:

```go
var TrustDialogMarkers = []string{
	"yes, i trust this folder",
	"do you trust the files in this folder",
}
```

claude 2.1.239 dropped the second phrase for a new one, which left the
detector matching nothing at any pane width and the Enter never pressed. Read
back on 2026-08-23 through `herdr pane read --source detection` in a
173-column pane under herdr 0.8.2, its dialog is:

```
 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review
 what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ 1. Yes, I trust this folder
   2. No, exit
```

Two choices in that set are deliberate. The newer phrase is the dialog's
selectable OPTION rather than its prose, because the option is what the dialog
is for and the sentence around it is what churns. The older phrase stays so an
operator still on a pre-2.1.239 claude is not broken by the fix;
`TestTheTrustDialogIsAnsweredInTodaysWordingAndTheOlderOne` drives the whole
pipeline against a recorded transcript of each and asserts exactly one Enter.

Adding the newer phrase did not move the 40-column floor: it is 24 characters
against the older phrase's 37, so the longest marker in use is unchanged.

The matcher earns its place rather than sitting there silently. Measured the
same day: `herdr agent start` into a fresh untrusted directory returns
`agent_not_ready` and leaves the dialog on screen unanswered — herdr does not
answer it, and nothing else does. Every worker worktree under
`<state_dir>/worktrees` is a fresh directory, so without this Enter the goal
is never delivered and the pane sits at the dialog until the ceiling ends the
spawn. Deleting the matcher was considered and rejected on that evidence.

## The dispatcher's address

Every pane this daemon opens is launched with

```
HDIS_DISPATCHER_PANE=<the report address>
```

in its environment, and the worker's condition tells the agent to answer there
rather than at a pane id written into its text. An agent that comes up in one of
these panes may read the variable to find out where to report; the text stays
valid whatever pane that turns out to be.

The address is the desk that owns the work, found in three rungs:

1. the pane the task was CREATED FROM, when the board's row names one.
2. a LIVE pane whose cwd is the task's project, when the row names none.
3. this daemon's own base pane, when there is no such pane either.

A report belongs to whoever wanted the work, and this daemon is not scoped to
one repository: it takes ready tasks off every project's board, so the
operator who started it is routinely not the operator who filed the task.

The middle rung exists because a task an operator filed at a terminal has no
pane of origin — nothing with a pane created it — and the first two rungs
alone sent every one of those reports to whoever happened to start the daemon.
The session already sitting in that repository is the desk that owns the work,
and `herdr pane list` says which one that is, so the evidence is Herdr's
rather than a field somebody has to remember to set. The last rung stays: a
machine with nothing live still answers somewhere.

Two rules make the middle rung a rule rather than a coin toss:

- **The lowest pane id wins** when several live panes sit in the project. It
  is the one answer that is stable across ticks, so reports for a repository
  do not wander between windows as panes come and go; most recently active
  would be a guess about which human is watching, and whatever a map iteration
  hands back first is no rule at all.
  `TestTheDeskAmongTwoLivePanesInTheProjectIsTheLowestPaneID` pins it from both
  list orders.
- **A checkout of this daemon's own is never the desk.** Every worker works in
  a worktree of its task's project, so without that bound the first worker for
  a project would become the desk and every later report for it would be
  delivered to a worker. The bound is `<state_dir>/worktrees`, the same one the
  reap is drawn at, and
  `TestAPaneInThisDaemonsOwnCheckoutsIsNeverTheAddress` pins it with the state
  dir relocated inside a project, where the path test alone would not catch it.

It is ONE resolver, not two. The workspace a worker's tab opens in is the same
question read the other way — which desk owns this work — so the desk is
resolved once per spawn, from one read of `herdr pane list`, and the report
address and the placement are both taken from it: see
[Where a worker comes up](#where-a-worker-comes-up).

It is an address and nothing more. It says where to answer — normally the pane
the task was filed from, and a desk found in the pane list or the daemon's own
pane when nothing with a pane filed it — never who the reader is: the sender stamped on any message the
agent then writes comes from the mail daemon's own reading of `HERDR_PANE_ID`,
which is Herdr's word about the pane it runs in, and never from this variable.
Publishing the address is the whole of hdis's part — nothing in this binary
imports, reads or runs herdr-mail, and a named test walks the source to keep it
that way.

## The worker's prompt cache

Every pane this daemon opens is also launched with

```
FORCE_PROMPT_CACHING_5M=1
```

which asks the client for the 5-minute prompt-cache TTL rather than the
1-hour one it would otherwise hand a REPL main thread. A worker is
short-lived, disposable, and rarely revisits its own prefix, so the long
entry costs more than the work can spend — a native subagent already gets
the short one for the same reason. The client reads this variable before
every other rule, so a worker pane settles the question on the way up.

It reaches worker panes and nothing else. The operator's own session keeps
the 1-hour TTL, which is why the variable is set on the tab rather than in
the launcher's environment, where the operator's sessions would read it too.
On the `codex` path it is inert: a relayed `cache_control` has no equivalent
upstream there and is not forwarded, so it changes only what a worker talking
to the real Anthropic endpoint writes.

## Restarting the dispatcher

The bindings — which pane was prompted for which task, when, how often, and
whether review was already announced — are the dispatcher's only state, and
they are the one thing about a worker that exists nowhere else until it
claims. They are written to `<state_dir>/dispatch-bindings.json` on every change
and taken back at the next start.

**What is persisted.** Only what is not derivable: the pane, the task id, the
time the goal was delivered, the prompt count, and whether review was
announced. Board facts — status, claim, lease, evidence — are read from the
board every tick, and pane facts from Herdr; neither is written here.

An on-demand dispatch's reservation is persisted beside them, and it carries
the daemon that made it. **The reservation is local and nothing else can see
it.** This binary has no `claim` verb and never writes a hold to the board, so
a reserved task stays on the board's own ready list until its worker claims
it; the reservation lives only in `<state_dir>/dispatch-bindings.json`, and all it
does is keep the watching loop and the `dispatch` verb from both taking the
same task inside one daemon. The owner it carries is that daemon's board
principal, `plugin:hdis@<its own pane>`, which is what lets a restart tell its
own stale record from one a peer daemon on this machine wrote.

The reservation also carries how often a spawn under it has failed. A spawn
that cannot succeed — a profile the config does not name, a checkout git
refuses to make — is given up on after three attempts, with a line in the log
saying so, and the task is left on the board rather than holding a worker slot
nothing can ever use.

**How it is written.** A JSON document, whole, to a temp file in the same
directory and then renamed over the old one. A reader sees the previous
document or the new one and never half of either, so a crash mid-write leaves
the last good set intact. A document that still cannot be read is reported and
the daemon starts with none of it rather than refusing to start.

The plugin contract's §5.1 store is SQLite, and this deliberately is not one.
The whole set is a handful of rows, rewritten in full on every change, read by
one process holding the daemon's own lock; there is no query, no schema and no
second reader for a database to earn. A SQLite driver is a large dependency
against this repo's standard-library budget, so the reason is recorded here
instead.

**What happens at start.** The restart rule is one sentence: for every live
pane this daemon opened, read the board row that pane is working, and ask what
the pane is and what still needs doing. Everything below is a consequence of
that question, not a case added to a list.

The two facts are read now, and neither is guessed at. Herdr says which panes
are alive and which agent name each one registered under, and this daemon's
names — `hdis-<task>` for a worker — carry the task number, so the name says a live pane is its own and which row to read.

**The name is a label and never the only ownership test**, because Herdr does
not keep it. Measured on wM:p4E, 2026-08-23: at 00:39 `herdr agent get`
answered `"name":"hdis-20"`, and at 00:57 the same pane, the same pid and the
same `agent_session` answered with no name field at all. A pane in that state
used to stop being this daemon's — nothing logged it, nothing adopted it,
nothing retired it, and it sat live with its task already finished.

The durable signal is the CHECKOUT. Every agent this dispatcher brings up is
given a directory of its own under `<state_dir>/worktrees`, named
`hdis-work-<task>-…`. Nothing else on the machine writes there, so a pane whose
cwd is one of those is a pane this daemon opened, and the directory's own name
says which task. It is
Herdr's word about the pane's CWD and never Herdr's word about an agent, which
is the field that was measured going missing. It is the exact mirror of the
reap, which already removes a checkout under that root that no binding names.

The tab label deliberately does NOT serve as identity. A tab holds several
workers and its label names only the task it was opened for, so reading a
pane's task off its tab would give every worker in that tab the first one's
number. The label is the close guard and the operator's signpost; the checkout
is the identity.

**When the two disagree, the worker is driven and the tab is given up.** An
operator can drag a worker into a tab of their own, and then the checkout says
the pane is this daemon's while the label says the tab is theirs. The pane is
still adopted and still retired: abandoning it is exactly the failure the
checkout signal exists to prevent, a live pane on a live task that nothing
adopts, nothing retires and nothing logs. What the daemon gives up is the TAB
— the binding names none, so the operator's tab can never be closed out from
under them, and the pane is retired as a pane. The operator is told, and
`TestAPaneWhoseAgentNameHerdrDroppedIsStillAdoptedAndStillRetired` pins both
halves. A number is unique only inside a project, so it is not a task's address
on its own: the project comes from the checkout the pane is working in, which
git names by way of the repository a worktree was cut from. A worker in its
worktree, and a pane opened before worktrees existed and still sitting in the
project, both answer the same way, and the row
is then read as `task get <number> --project <project>`. A read by ID stays
board-agnostic and keeps `--all-projects`, because an ID belongs to no
project; the board refuses a bare number across projects by design, and
nothing here asks it to stop. The persisted bindings are a hint on top of that, never the frame: they
carry what Herdr cannot — when the goal was delivered, how often, whether
review was announced, which checkout a pane was given, which TAB it was placed
in and which branch a worker's commits are on — and they cover the
seconds after a `tab create` in which the agent has not registered yet.

The answers, all of them consequences of the one question:

- The row is live and the pane is its own: the pane is adopted. A binding that
  survived is taken back whole, and a pane with no binding — a worker that
  already CLAIMED, so the board's holder is the worker's own pane and no hold
  under this daemon's principal names it — is bound from what is read now.
- The row is done or cancelled: the pane is retired. Nothing else will ever
  close a pane this daemon opened for work that is over.
- The row is claimed by a pane that is not this one: the pane is let go
  unbound and left alone. Whose worker the task is now is the board's answer,
  and not a restart's to act on.
- The board has no such row, or cannot answer for it: a pane with a binding is
  held, exactly as a tick holds it — a board that is down is not evidence that
  a task moved on — and a pane with no binding is left as it is. So is a pane
  whose checkout names no repository, since nothing can then say which board
  its number belongs to.
- A binding whose pane Herdr no longer lists names nothing to reconcile, and
  is dropped with a line in the log.
- If **Herdr** cannot be reached at all, nothing is adopted, the failure is
  loud, and the store is left where it is for the next start. Adopting on that
  guess is how a live worker's task ends up in a second pane, which is the
  split this exists to prevent. Nothing is spawned while Herdr is down either,
  so the wait costs nothing.

**What a restart hands back.** Once every pane is reconciled, a reservation in
`<state_dir>/dispatch-bindings.json` that no adopted pane is working is stale by
construction — a daemon went down between reserving a task and bringing a pane
up for it — and it is dropped, so the task is dispatchable again instead of
sitting reserved forever. Nothing has to be said to the board about it,
because nothing was ever said to the board: the task never left the ready
list. And a checkout under
`<state_dir>/worktrees` that no binding names is removed: the binding is the
only record of where a checkout
is, so one lost while the daemon was down leaves the directory with nothing to
remove it. A worker's commits are on its branch, and the branch outlives the
directory, so reaping one strands no work. Every one is
logged with the reason.

**What a restart will never touch.** A pane this daemon did not open — one
whose agent name is not its own and which none of its bindings names — is
never adopted and never closed. A reservation record naming another daemon's
principal is left for that daemon rather than acted on. Anything outside `<state_dir>/worktrees` is never removed, and inside it
only entries carrying the `hdis-` prefix hdis names its own with,
`hdis-work-` for a worker's checkout.
Lease release stays htask's own — a single stale hold this daemon itself is
named on is handed back, and the pane-gone sweep and the lease timer are never
reimplemented here.

`hdis doctor` reports the file and how many bindings came back at the last
start.

**The window that is left.** A pane becomes this daemon's own to Herdr when
the agent in it registers, and the binding is written after the spawn returns.
A crash in between the `tab create` and the registration loses both: the pane
is alive, nothing names it, and the task is dispatched again into a fresh one
with the old pane left behind. Closing that window would mean writing a
binding before the pane exists to bind to, which trades a rare orphan for
routine bindings to panes that never came up.

Two smaller truths about a restart:

- A worker that **already claimed** its task is unaffected either way. The
  claim, the lease and the evidence are board facts and the pane is a Herdr
  fact; a re-adopted binding just means `hdis` keeps following it.
- The settings file a spawn wrote for a pane is remembered in memory only. A
  re-adopted pane retired after a restart is closed, and its settings file is
  left in the temp dir for the operating system to clear.

## The boundary

`htask` is the ledger and `herdr` is the terminal; this repo is only the
policy between them. Both are driven by shelling out to their CLIs — never by
opening their sockets — and neither is second-guessed. Herdr's `agent_status`
is the only truth about a worker this repo accepts, and lease release is
htask's own: pane-gone sweeps and the lease timer belong to the board, and a
second writer racing them is the bug, not a safety net.

The dispatcher stops at review. It never runs `task approve`, `task reject`,
or any note verb.

The verification lane does not move that line, it works up against it. With
`"verify"` on, a task one of this dispatcher's own workers submitted earns one
SELF-REVIEW SHOT: a second condition prompted into the worker's own pane. No
pane is opened for it and no agent launches, so it costs a prompt.

One shot means one the worker receives. §11.4 forbids reading a successful
`agent prompt` as delivery — Herdr accepting the text says it was accepted and
nothing more, an agent TUI can collapse a paste, and a pane can exit between
the call and the agent's next turn. So the shot is sent again while Herdr
still calls that worker idle, bounded by the same `max_prompts` and claim
timeout the unclaimed nudge uses. A worker that got the condition is working
and never meets a second copy; one whose prompt reached nothing is asked again
instead of losing its only check in silence with the board still green.

The shot lands on a warm prefix. Task 33 put every worker on
`FORCE_PROMPT_CACHING_5M=1`, so a worker's cache TTL is five minutes, and the
gap between a submission and the next tick is seconds — the one moment in a
worker's life when its accumulated context is cheap to reuse. An independent
verifier spent its whole budget rebuilding what that prefix already held.

**What the condition asks for is the point.** It does not say "review your
work". Five rejections in a row — tasks 42, 48, 51, 73 and the earlier 40 —
were the same shape: a guard the report documented with no test behind it, and
both the worker and an independent verifier read past all five. What caught
them every time was a compiling mutation: delete the guard, run the named
tests, read the exit code. That is mechanical, and believing the work is
finished does not change an exit code, which is why a reader who knows the
work is not disqualified from running it. So the condition asks for exactly
that — a mutation per claimed guard, refusal or invariant, the tests the
report names, the failure confirmed, each mutation reverted — and then for the
worker's own reading of every mutation that did NOT bite, because that case is
ambiguous between a missing test and bad aim and the operator needs the
worker's judgment of it rather than silence.

**And it names where the findings go**, because asking for a report and naming
no route is how a lane built to catch undelivered claims makes one of its own.
`htask` refuses `task submit` on a row that is not `doing`, so a worker whose
task is in review cannot amend the report it already sent; findings with
nowhere to go die in the pane while the board stays green. The route is the
mail door at `$HDIS_DISPATCHER_PANE`, the desk that owns the work. It beats a
board note on three counts: the address is already in the pane's environment
and already named by the condition the worker booted with, so nothing new is
introduced to look at; `htask` has no task-scoped note verb, so a note would
land on the notes board detached from the row under review; and durability,
the one real argument for the note, is already the mail store's, since `inbox`
lists what was sent whether or not the pane marker arrived. It names the DOOR
and never `hmail` — a dispatched pane works in a worktree where a plugin
binary kept in a project's own `bin/` is not on PATH, measured live on
2026-08-23 with `hmail` answering `command not found` while the door answered.

**And one pass is not aimed at the report at all.** Everything above is scoped
to what the report CLAIMS, so a defect nobody claimed passes the whole
mechanical half structurally. So the condition also sends the worker to the
diff itself, saying in so many words that it is not the report, and reads it
against a third frame as well — the task's own acceptance criteria. The report
is the worker's frame and the criteria are the operator's, and neither
contains the other: a change can be internally consistent, survive every
mutation above, and still leave a criterion with nothing implementing it. That
pass keeps a floor of its own, because an open invitation has none: every
observation is either proved with a mutation or a run, or labelled plainly as
a suspicion that could not be proved. An unproven suspicion is worth sending;
one dressed as a finding costs the operator the round this lane was built to
save.

**The shot is armed as a `/goal`, not sent as a plain prompt.** A plain prompt
fires once, so a shallow pass at the mutations ends the check — nothing asks
again. A `/goal` condition is evaluated after every turn, so the worker's own
loop is what refuses to stop on a half-done pass. What bounds it is the
client's prompt box rather than the shell `TypedLineBudget` was measured
against, so it has a second ceiling of its own: `spawn.PromptedGoalBudget`,
1023, the operator's measurement recorded beside the constant.
`TestThePromptedSelfReviewGoalFitsItsOwnBudget` pins the whole delivered text
under it, `TestThePromptedGoalBudgetIsTheOperatorsMeasurement` pins the number
itself so a condition that overruns is trimmed rather than the ceiling raised,
and `TestOnlyTheSelfReviewShotIsArmedAsAGoal` pins that the ordinary
nudges are not armed this way — a nudge is one instruction for one turn, and a
standing condition would have a worker re-satisfying it forever.

`TestTheSelfReviewConditionAsksForAMutationPerClaim` pins each of those asks,
and `TestARereadRequestIsNotASelfReviewCondition` is what makes the pin worth
having — including a near-miss with every mechanical step intact and no
destination, which is the shape this lane was rejected for once.

One submission earns one shot, and a re-submission after a rejection earns
another: `Binding.Verified` remembers it and `Rearm` clears it when the task
leaves review. The shot produces no verdict — the task stays in review and the
operator still approves or rejects — so recusal is untouched: it is work done
before a verdict, not a verdict. `TestTheBoardAdapterCarriesNoReviewVerb` and
`TestNoSourceFilePassesAReviewVerbAsAnArgument` pin that from both sides.

The fix after an operator rejection stays a prompt into the same pane, and
that prefix IS cold by then. Note 30 weighed retiring at submit and kept the
warm pane on purpose, because a rejection needs the conversation.

Every pane this dispatcher opens works in a checkout of its own, and never in
the project directory. It is made under `<state_dir>/worktrees` and removed
when the binding that owns it is dropped: a `git worktree` on a branch named
for the task, `hdis/task-<seq>`, created at the project's current HEAD. A
worker commits, so it needs somewhere its commits can live. Removing the
directory later leaves the branch and every commit on it reachable from the
project, which is what makes reaping a checkout safe.

**hdis integrates nothing.** It creates a branch and it removes checkouts.
Bringing the work home — fast-forward, merge, cherry-pick, push — and deleting
the branch afterwards are the operator's own acts, after review, on their own
judgment. `TestNoSourceFilePassesAMergePushOrBranchDeleteAsAnArgument` pins
that the way the review-verb guard pins the board boundary.

The split is not tidiness. It has bitten twice. Two workers sharing the
project directory is how one task's commit swept up another task's
uncommitted prose: nothing was lost that night only because both changes were
wanted, and the next collision would have been two workers editing one file.
And the verification lane's first live run, back when it opened a pane of its
own, had that pane, the worker and the operator all mutating one tree: it
restored the tree from HEAD, destroyed the operator's uncommitted work, and
then reported a gate result it had measured over that debris rather than over
the commit under review. A gate run means nothing when the tree is not the
commit. So the worktree is a precondition rather than a convenience: when it cannot be made
— the project is not a git repository, or git refuses — nothing is spawned at
all, the reason is logged, and the task simply stays where it is for a tick
that can hand out a checkout. Working in the shared tree is worse than not
working. `TestTheWorkerIsGivenAWorktreeOfItsOwnOnItsOwnBranch`,
`TestAWorkerIsSpawnedInItsOwnWorktreeNeverTheProjectDirectory`,
`TestWithoutAWorktreeNothingIsSpawned` and `TestARunLeavesNoWorktreeBehind`
pin each half.

**What the operator now does by hand.** A worker's commits land on
`hdis/task-<seq>` and nowhere else, so approving a task no longer leaves the
work on the project's own branch. Merging that branch, and deleting it
afterwards, is a step that did not exist before. `hdis status` names the
branch beside the pane so it can be found without reading the bindings file.

**A branch can go behind while its worker is still running.** A worker's
branch is cut from the project's HEAD at SPAWN time, and with several workers
on one repository the ordinary case is that another task lands first and
moves that HEAD on. The second branch then refuses a fast-forward, and before
this the operator found that out at merge time, with the worker long gone and
the recovery — a scratch checkout, a rebase, a full uncached gate on the
REBASED commit rather than the reviewed one — theirs to improvise.

So `hdis status` says it. A branch prints as `hdis/task-7 (behind)` when the
project's HEAD is no longer reachable from it, and the JSON carries the same
fact as a `behind` boolean on each worker row.

Two decisions are worth naming, because either could reasonably have gone the
other way:

- **Behind is measured against the project's CURRENT HEAD**, not against the
  commit the branch was cut from. HEAD is what the operator merges into and it
  is the only side of the comparison that moves after a spawn, so
  `git merge-base --is-ancestor HEAD <branch>` failing is exactly the state
  `git merge --ff-only <branch>` refuses. Nothing here reads a remote: the
  question is about the local project, which is what a local merge uses.
- **It is measured when status is asked, not on every tick.** A tick is a
  per-pane loop and a git call per binding on it would be paid whether or not
  anyone was looking; status is asked by an operator who is looking. The cost
  is one `rev-parse` and one `merge-base` per worker branch, on the status
  call only, taken outside the loop's mutex so a tick never waits behind a
  process spawn. A git that cannot answer leaves the fact unsaid and logs why,
  because a report that guesses is a report that is wrong.

**A git that cannot answer is not the answer "behind".** Only git's exit 1
means "not an ancestor"; every other exit is git refusing, and reading a
refusal as behind would mark every worker behind at once. The operator's
response to behind is to rebase, so that failure would rebase branches that
were never behind, replacing each reviewed commit with one nobody reviewed —
the very incident this exists to prevent. So a failure is an error, the row
stays unmarked, and the reason goes to the log.

`TestStatusSaysABranchIsBehindOnlyOnceTheProjectHeadHasMovedPastIt`,
`TestTheJSONStatusCarriesBehindAsAField`,
`TestBehindIsTrueOnlyOnceTheProjectHeadHasMovedPastTheBranch`,
`TestBehindRefusesABranchTheRepositoryDoesNotHave`,
`TestBehindFailsRatherThanAnsweringWhenGitCannotTellUs` and
`TestAGitThatCannotAnswerLeavesTheRowUnmarkedAndSaysWhy` pin it, in every
direction: a check that always reports and a check that never reports each
fail the first of them, and reading any git failure as behind fails the last
two. The failure cases use a stub git that exits 2 at `merge-base` and is
real git everywhere else, so the call reaches the exit code rather than
failing on the way to it — the unknown-branch case is refused one line
earlier, by the `rev-parse`, and covers a different thing.

**hdis still does not rebase.** Saying "behind" is the whole of it. Having a
worker rebase before it submits is the shape that removes the problem rather
than showing it, and it needs a decision about when a worker may touch git;
that boundary is clean and this did not spend it. Nor is dispatch serialised
per repository. The recovery stays the operator's: rebase the branch on the
current HEAD, run the FULL gate on the rebased commit — it is not the commit
that was reviewed — and then fast-forward.

## Building and testing

```sh
make test        # the fast loop: the pure decision core and the payload shapes
make test-full   # the gate: the above, plus every case that shells out to a
                 # fake htask or a fake herdr on PATH, with -race and a
                 # cross-compile vet of the other supported platform
make build       # bin/hdis
```

No test reaches a real board, a real Herdr server or a real proxy daemon:
every case that shells out answers its own calls with a stand-in binary on
`PATH`. A green `make test` is not a green gate.

## Dependencies

The standard library, and one thing that earned its way in:

- **`github.com/modelcontextprotocol/go-sdk`** — the MCP door serves a wire
  protocol with a specified handshake, tool schemas and error envelope, and
  the version of it a client speaks is not ours to guess. Reimplementing that
  is substantial work whose only reward is being subtly incompatible with the
  callers the door exists for. It is pinned to the version the board plugin
  already runs, so one machine holds one copy.

Anything else that earns its way in gets its reason recorded here too.

## License

MIT.
