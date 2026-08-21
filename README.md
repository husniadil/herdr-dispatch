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

This plugin satisfies **version 0.4.0** of the Herdr plugin contract.
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

Two keys sit at the top level beside `profiles`: `"proxy"` names the codex
provider's launcher, and `"pane"` names the base pane a daemon uses when it
was not started inside a Herdr pane and was given no `-pane`.

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
2. `herdr pane split` off the dispatcher's pane, in the task's own project.
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

## Restarting the dispatcher

The bindings — which pane was prompted for which task, when, and how often —
are the dispatcher's only state, and they live in the daemon's memory. That
mapping exists nowhere else until the worker claims, so restarting the daemon
loses it:

- A worker that **already claimed** its task is unaffected. The claim, the
  lease and the evidence are board facts, the pane is a Herdr fact, and both
  survive. `hdis` simply stops tracking that pane; nothing retires it, and the
  board's own lease timer governs it from then on.
- A worker that was **prompted but never claimed** is forgotten. Its task is
  still ready on the board, so the next tick dispatches it again into a fresh
  pane once the claim timeout has passed. The orphaned pane is left where it
  is — closing a pane the dispatcher no longer knows it opened is worse than
  leaving it for the operator.

Persisting the bindings would fix the second case and is deliberately not
done yet: it buys a restart edge case at the cost of a second store that can
disagree with the board. The daemon makes that window rarer rather than
shorter — one process holds the bindings for as long as it runs, instead of
each door holding a set of its own.

## The boundary

`htask` is the ledger and `herdr` is the terminal; this repo is only the
policy between them. Both are driven by shelling out to their CLIs — never by
opening their sockets — and neither is second-guessed. Herdr's `agent_status`
is the only truth about a worker this repo accepts, and lease release is
htask's own: pane-gone sweeps and the lease timer belong to the board, and a
second writer racing them is the bug, not a safety net.

The dispatcher stops at review. It never runs `task approve`, `task reject`,
or any note verb.

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
