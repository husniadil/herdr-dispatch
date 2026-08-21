# herdr-dispatch

The dispatcher for the [herdr-tasks](https://github.com/husniadil/herdr-tasks)
board: `hdis` watches for ready tasks, brings up a worker agent in a
[Herdr](https://herdr.dev) pane for each one, delivers the task's goal, tracks
the worker, and hands off at review — where the board's own review gate takes
over. The board stays the ledger; this binary is only execution policy.

## Running it

```sh
make install
hdis run
```

`hdis run` must run inside a Herdr pane: worker panes are splits of the
dispatcher's own, and `HERDR_PANE_ID` is where it starts from. `hdis run -h`
lists the knobs — tick interval, how many workers may be live at once, how
long a delivered goal may go unclaimed, and how many times one task's goal is
re-delivered before the worker is given up on.

At startup it reads `htask doctor --json` and refuses to run when the board's
daemon is not answering or when the board cannot reach Herdr. After that, a
tick that fails is reported and the next one still runs.

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
3. For a `codex` profile, `eval "$(proxenos env)"` in that pane: the
   worker inherits the routing environment as a direct child of the shell.
4. `herdr agent start`, with the profile's argv and a short `/goal` condition
   after the separator. That condition is a **pointer** hdis composes — claim
   the task with `htask task claim <n>`, read its full criteria with
   `htask task get <n>`, and finish by submitting it for review with a report
   and evidence — and never the board's rendered goal document. The line is
   TYPED into the pane, character by character, and a long one intermittently
   arrives broken: two live runs came out with the condition cut mid-word and
   the command's own start typed over what followed, one at ~2.2k characters
   and one at ~1.4k, while the same text piped into a bare shell was clean to
   a megabyte. So the criteria stay on the board, where the worker reads them
   whole and no shell ever types them, and the whole typed line is held under
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
are the dispatcher's only state, and they live in memory. That mapping exists
nowhere else until the worker claims, so restarting `hdis` loses it:

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
disagree with the board.

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

The standard library, and nothing else. A dependency that earns its way in
gets its reason recorded here.

## License

MIT.
