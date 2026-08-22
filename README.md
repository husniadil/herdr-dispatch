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
`$XDG_STATE_HOME/hdis/hdis.lock`, answering on a private socket at
`$XDG_STATE_HOME/hdis/hdis.sock`. A second one refuses to start with
`ALREADY_RUNNING` rather than driving the same board alongside the first.

`hdis daemon -h` lists the knobs — tick interval, how many workers may be
live at once, how long a delivered goal may go unclaimed, and how many times
one task's goal is re-delivered before the worker is given up on.

Worker panes are splits of a base pane, so the daemon needs one: it takes
`HERDR_PANE_ID` when it was started inside a Herdr pane, `-pane` when it was
given one, and the config's `"pane"` key otherwise. Without any of the three
it still comes up and still answers both doors, but it does not tick and
every dispatch refuses with `NO_BASE_PANE`. Every spawn it could reach for
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

This plugin satisfies **version 0.6.0** of the Herdr plugin contract.
`hdis doctor` says so in both its shapes, as its own top-level `contract`
field, distinct from the `board.contract` it relays from `htask`.

## The two doors

Both are generated from one verb table, and a parity test drives a live MCP
session against it: a verb on one door and not the other fails the gate,
unless the table declares the asymmetry itself.

| Verb                  | MCP tool         | What it does                                     |
| --------------------- | ---------------- | ------------------------------------------------ |
| `hdis doctor`         | `doctor`         | Why a dispatch would refuse, before one is tried |
| `hdis dispatch <task>`| `dispatch`       | Reserve one ready task for the next tick         |
| `hdis stop`           | none, on purpose | Ask the running daemon to shut down              |
| `hdis status`         | `status`         | What the dispatcher is driving now               |

`stop` is the one CLI-only verb. Every other verb is about one task, and an
MCP door is spawned once per client session, so an agent holding one would be
able to take the dispatcher away from every other worker it is driving.
Stopping it is the operator's act, at a terminal. It answers `NOT_RUNNING`
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
to write to, so its log goes to `$XDG_STATE_HOME/hdis/hdis.log`.

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

It refuses with a name rather than a sentence to parse: `NOT_READY` when the
board will not hand the task out, `NOT_FOUND` when the board has no such
task, `AT_CAPACITY` when `-max-workers` are already live or reserved,
`ALREADY_DISPATCHED` when this daemon is already driving it, and
`NO_BASE_PANE` when there is nowhere to put a worker.

**Which profile a worker launches with is not selectable per call.** It is
decided by the config and nowhere else, for the same reason the board carries
no profile field: execution policy is this binary's business.

**The caller's identity buys nothing.** The daemon records the pane a caller
ran in, or `unknown` for a caller on another harness, and grants nothing for
it. Every caller here is the operator's own tooling reaching a socket only
the operator can open, and the board only ever hears from this binary as
`plugin:hdis` whoever asked.

## Configuration

`hdis` reads `$XDG_CONFIG_HOME/hdis/hdis.json` (`~/.config/hdis/hdis.json`).
It holds worker profiles — the launch preset a worker is assembled from — one
global default, and per-project overrides:

```json
{
  "default": "worker",
  "profiles": {
    "worker": { "provider": "claude" },
    "routed": {
      "provider": "codex",
      "agent": "claude",
      "model": "sonnet",
      "effort": "medium",
      "args": ["--add-dir", "/srv/shared"]
    }
  },
  "projects": {
    "/Users/me/github.com/me/some-repo": "routed"
  }
}
```

| Field      | Meaning                                                                                                                    |
| ---------- | -------------------------------------------------------------------------------------------------------------------------- |
| `provider` | `claude` runs the plain binary. `codex` runs it through the proxy launcher named below, which supplies the routing. Required. |
| `agent`    | The `--agent` name. Defaults to the literal `claude`. Definitions belong to each project's `.claude/agents`; none ship here. |
| `model`    | A tier alias. Empty means the client's own default.                                                                          |
| `effort`   | Defaults to `low`.                                                                                                           |
| `args`     | Extra argv passed through to the worker.                                                                                     |

Three keys sit at the top level beside `profiles`: `"proxy"` names the codex
provider's launcher, `"pane"` names the base pane a daemon uses when it was
not started inside a Herdr pane and was given no `-pane`, and `"verify"` is
the verification lane.

The lane is off unless the document turns it on. On, it names one of the
profiles above, and every task a worker of this daemon's submits earns a
verifier launched from it:

```json
{
  "verify": { "enabled": true, "profile": "checker" }
}
```

A lane that is on and names no defined profile is refused at parse, not at
the first review that would have needed it. `hdis doctor` reports whether the
lane is on and which profile it uses, and `hdis status` marks each pane
`worker` or `verifier`. What a verifier is for, and the line it does not
cross, is under [The boundary](#the-boundary).

The `codex` provider's launcher is named by an optional top-level `"proxy"`
key, and defaults to the literal `proxenos`. It lives in the config rather
than in this binary because that binary has been renamed once already, and
the next rename should be one line of JSON:

```json
{ "proxy": "/opt/homebrew/bin/proxenos" }
```

Which profile a project gets is decided here and nowhere else. The board
carries no profile field, deliberately: which agent kind and model a worker
runs as is execution policy, and execution policy does not belong in the
ledger.

## What a spawn actually does

1. For a `codex` profile, `proxenos settings` first. A daemon that is
   down fails here, in the daemon's own words, rather than thirty seconds
   later as a startup timeout with the cause hidden in a pane.
2. `herdr pane split` off the dispatcher's pane, in the task's own project,
   with `--env HDIS_DISPATCHER_PANE=<the report address>` and `--env
   FORCE_PROMPT_CACHING_5M=1` — see
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

## The dispatcher's address

Every pane this daemon splits — worker and verifier alike — is launched with

```
HDIS_DISPATCHER_PANE=<the report address>
```

in its environment, and both conditions tell the agent to answer there rather
than at a pane id written into their text. An agent that comes up in one of
these panes may read the variable to find out where to report; the text stays
valid whatever pane that turns out to be.

The address is the pane the task was CREATED FROM when the board's row names
one, and the daemon's own base pane only when it does not. A report belongs to
whoever wanted the work, and this daemon is not scoped to one repository: it
takes ready tasks off every project's board, so the operator who started it is
routinely not the operator who filed the task. The verification lane follows
the same rule, which also decides whose tokens a verifier spends. A task an
operator filed at a terminal legitimately has no pane of origin — nothing with
a pane created it — and for those the daemon's own pane is the only address
there is. What does NOT move is the pane a worker is split off: that stays the
base pane in both branches, because this daemon has only its own pane to split
from.

It is an address and nothing more. It says where to answer — normally the pane
the task was filed from, and the daemon's own pane only when nothing with a
pane filed it — never who the reader is: the sender stamped on any message the
agent then writes comes from the mail daemon's own reading of `HERDR_PANE_ID`,
which is Herdr's word about the pane it runs in, and never from this variable.
Publishing the address is the whole of hdis's part — nothing in this binary
imports, reads or runs herdr-mail, and a named test walks the source to keep it
that way.

## The worker's prompt cache

Every pane this daemon splits is also launched with

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
the 1-hour TTL, which is why the variable is set on the split rather than in
the launcher's environment, where the operator's sessions would read it too.
On the `codex` path it is inert: a relayed `cache_control` has no equivalent
upstream there and is not forwarded, so it changes only what a worker talking
to the real Anthropic endpoint writes.

## Restarting the dispatcher

The bindings — which pane was prompted for which task, when, how often, and
whether review was already announced — are the dispatcher's only state, and
they are the one thing about a worker that exists nowhere else until it
claims. They are written to `<state_dir>/hdis-bindings.json` on every change
and taken back at the next start.

**What is persisted.** Only what is not derivable: the pane, the task id, the
time the goal was delivered, the prompt count, and whether review was
announced. Board facts — status, claim, lease, evidence — are read from the
board every tick, and pane facts from Herdr; neither is written here.

An on-demand dispatch's reservation is persisted beside them, and it carries
the daemon that made it. The daemon's board principal is
`plugin:hdis@<its own pane>`, so a hold the board is keeping names the daemon
that took it: a restart reads its own pane in that principal and knows the
hold is its own to resolve, and a hold carrying another pane belongs to a peer
that may well still be running.

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
names — `hdis-<task>` for a worker, `hdis-v-<task>` for a verifier — carry the
task number, so the name is what says a live pane is its own and which row to
read. The persisted bindings are a hint on top of that, never the frame: they
carry what Herdr cannot — when the goal was delivered, how often, whether
review was announced, which checkout a pane was given and which branch a
worker's commits are on — and they cover the
seconds after a `pane split` in which the agent has not registered yet.

The answers, all of them consequences of the one question:

- The row is live and the pane is its own: the pane is adopted. A binding that
  survived is taken back whole, and a pane with no binding — a worker that
  already CLAIMED, so the board's holder is the worker's own pane and no hold
  under this daemon's principal names it — is bound from what is read now.
- The row is done or cancelled: the pane is retired. Nothing else will ever
  close a pane this daemon opened for work that is over.
- The pane is a verifier and the row has left review: the pane is retired.
  A rejection is enough; the submission it was reading is what it was for. The
  retire is also what keeps the worktree reap safe, since an unbound checkout
  would otherwise be removed out from under a running verifier.
- The row is claimed by a pane that is not this one: the pane is let go
  unbound and left alone. Whose worker the task is now is the board's answer,
  and not a restart's to act on.
- The board has no such row, or cannot answer for it: a pane with a binding is
  held, exactly as a tick holds it — a board that is down is not evidence that
  a task moved on — and a pane with no binding is left as it is.
- A binding whose pane Herdr no longer lists names nothing to reconcile, and
  is dropped with a line in the log.
- If **Herdr** cannot be reached at all, nothing is adopted, the failure is
  loud, and the store is left where it is for the next start. Adopting on that
  guess is how a live worker's task ends up in a second pane, which is the
  split this exists to prevent. Nothing is spawned while Herdr is down either,
  so the wait costs nothing.

**What a restart hands back.** Once every pane is reconciled, a hold the board
is still keeping for this daemon that no adopted pane is working is stale by
construction: it is handed back with `task release` and a note saying the
dispatcher went down before a worker came up, so the task returns to the ready
list instead of sitting reserved forever. And a checkout under
`<state_dir>/worktrees` that no binding names is removed, a worker's as
readily as a verifier's: the binding is the only record of where a checkout
is, so one lost while the daemon was down leaves the directory with nothing to
remove it. A worker's commits are on its branch, and the branch outlives the
directory, so reaping one strands no work. Every one is
logged with the reason.

**What a restart will never touch.** A pane this daemon did not open — one
whose agent name is not its own and which none of its bindings names — is
never adopted and never closed. A hold carrying another daemon's principal is
never released: `task list --mine` is scoped to the principal, so a peer's row
is not even in the answer, and a reservation record naming a peer is left for
it. Anything outside `<state_dir>/worktrees` is never removed, and inside it
only entries carrying the `hdis-` prefix hdis names its own with —
`hdis-work-` for a worker's checkout, `hdis-verify-` for a verifier's.
Lease release stays htask's own — a single stale hold this daemon itself is
named on is handed back, and the pane-gone sweep and the lease timer are never
reimplemented here.

`hdis doctor` reports the file and how many bindings came back at the last
start.

**The window that is left.** A pane becomes this daemon's own to Herdr when
the agent in it registers, and the binding is written after the spawn returns.
A crash in between the `pane split` and the registration loses both: the pane
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
`"verify"` on, a task one of this dispatcher's own workers submitted earns a
VERIFIER worker: a fresh pane, the same spawn path, a binding of its own with
a verifier kind on it. Its condition tells it to reread the report through
the board's `get` MCP door, run the gate with nothing cached, check the
report's claims against the code, prove the gate still bites, and send what it
found through the mail MCP door, or the board's note tool when that door is
not there — and never to run `task approve` or `task reject`. Nothing of ours
that the condition names is a command to be looked up on PATH: the pane is
opened in a worktree where a plugin binary kept in the project's own `bin/` is
not found, which is how the first live verifier lost `hmail`, and a board read
resolved from PATH is the same shape of bet. The one command it does name as a
command is the project's own gate, which is the project's toolchain rather
than one of our plugins.
`TestEveryFleetInstructionInTheVerifierConditionNamesADoor` pins the whole
condition, not only the routes findings leave by. One
submission earns one verifier; a re-submission after a rejection earns
another. Verification is delegated. Judgment is not: a verifier reports, the
operator decides, and the board's review gate is still the only thing that
moves the task. `TestTheBoardAdapterCarriesNoReviewVerb` and
`TestNoSourceFilePassesAReviewVerbAsAnArgument` pin that from both sides.

Every pane this dispatcher opens works in a checkout of its own, and never in
the project directory. Both are made under `<state_dir>/worktrees` and removed
when the binding that owns them is dropped, and they differ in what they are
for:

- A **worker** gets a `git worktree` on a branch named for its task,
  `hdis/task-<seq>`, created at the project's current HEAD. It commits, so it
  needs somewhere its commits can live. Removing the directory later leaves
  the branch and every commit on it reachable from the project, which is what
  makes reaping a worker's checkout safe.
- A **verifier** gets a detached checkout at the commit that was SUBMITTED,
  which is the tip of the branch its worker committed on. The project's own
  HEAD is not that commit now that a worker no longer commits to it, and a
  gate run means nothing when the tree is not the commit under review.

**hdis integrates nothing.** It creates a branch and it removes checkouts.
Bringing the work home — fast-forward, merge, cherry-pick, push — and deleting
the branch afterwards are the operator's own acts, after review, on their own
judgment. `TestNoSourceFilePassesAMergePushOrBranchDeleteAsAnArgument` pins
that the way the review-verb guard pins the board boundary.

The split is not tidiness. It has bitten twice. Two workers sharing the
project directory is how one task's commit swept up another task's
uncommitted prose: nothing was lost that night only because both changes were
wanted, and the next collision would have been two workers editing one file.
And the verification lane's first live run had the verifier, the worker and
the operator all mutating one tree: the verifier restored it from
HEAD and destroyed the operator's uncommitted work, then reported a gate
result it had measured over that debris rather than over the commit under
review. A gate run means nothing when the tree is not the commit. So the
worktree is a precondition rather than a convenience: when it cannot be made
— the project is not a git repository, or git refuses — nothing is spawned at
all, the reason is logged, and the task simply stays where it is for a tick
that can hand out a checkout. Working in the shared tree is worse than not
working. `TestAVerifierIsGivenAWorktreeAndNeverTheProjectDirectory`,
`TestTheWorkerIsGivenAWorktreeOfItsOwnOnItsOwnBranch`,
`TestAWorkerIsSpawnedInItsOwnWorktreeNeverTheProjectDirectory`,
`TestAVerifierDetachesAtTheCommitItIsGivenNotTheProjectsHead`,
`TestWithoutAWorktreeNothingIsSpawned` and `TestARunLeavesNoWorktreeBehind`
pin each half.

**What the operator now does by hand.** A worker's commits land on
`hdis/task-<seq>` and nowhere else, so approving a task no longer leaves the
work on the project's own branch. Merging that branch, and deleting it
afterwards, is a step that did not exist before. `hdis status` names the
branch beside the pane so it can be found without reading the bindings file.

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
