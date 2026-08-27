# herdr-dispatch

The dispatcher that closes the loop [herdr-tasks](https://github.com/husniadil/herdr-tasks)
leaves open: it takes ready tasks from the `htask` board to review with no
human in the middle. One Go binary (`hdis`) that watches the board, brings a
worker pane up in [Herdr](https://herdr.dev) for each task, delivers the goal,
tracks the worker, and stops at review — where the board's own review gate
takes over.

## Commands

- `make test` — the fast loop, seconds: a `gofmt -l` check, then the pure
  decision core and the payload shapes under `-short`. Run it on every edit.
  Formatting is checked, never applied: the fix belongs in the commit that
  caused it.
- `make test-full` — **the gate**, and what CI runs: the above plus every case
  that shells out to a fake `htask` or a fake `herdr` on PATH, with `-race`
  and a cross-compile vet of both supported platforms. Run it before every
  commit.
- `make e2e` — layer 3, deliberately outside the gate: the built binary
  against a REAL `htask` from the sibling checkout. A machine without that
  checkout gets a loud skip naming what was missing.
- `make release-check` — the gate, the build, and that same layer 3 with the
  skip turned into a failure. What goes before a release tag.
- `make build` / `make install` — `./cmd/hdis`.

A green `make test` is not a green gate. Nothing is committed on it alone.

## The boundary with htask

htask is the ledger; this repo is execution policy. The line is not
negotiable in either direction:

- **Board facts live in htask** — task state, claims, leases, evidence,
  review authority, who may approve. The dispatcher READS them and never
  duplicates them in its own state. Its only memory of its own is the
  binding: which pane it prompted for which task, in which tab, from which
  checkout and on which branch, with which profile, when, how often, whether
  review was announced and whether the self-review shot has gone out — the one
  mapping that exists nowhere else until the worker claims. Everything else it
  might want to remember is derivable from the board plus Herdr, or wrong to
  remember.
- **Execution policy lives here** — which agent kind, model, effort and args
  a worker gets, how a goal is delivered, when a silent worker is re-prompted,
  when a pane is retired. None of that belongs in htask.
- The integration surface is `htask <verb> --json`, exactly as the htask
  README's "Driving htask from another program" section declares it: shell
  out, never open its socket; scope with `--project`/`--all-projects`; resume
  the event trail with `--since` or be handed the whole history; write as
  `--as plugin:hdis@<base pane>`, which is what tells one daemon's holds from
  a peer's. The board's verbs are flat — `htask get`, never `htask task get`.
- **Never reimplement lease release.** Pane-gone sweeps, the lease timer, and
  the startup reconciliation are htask's own. A second writer racing them is
  the bug, not a safety net.
- Herdr is driven the same way: shell out to `herdr <verb>` (`pane list`,
  `pane split`, `pane read`, `pane run`, `pane close`, `tab create`,
  `tab list`, `tab close`, `agent start`, `agent prompt`, `agent list`,
  `agent get`, `pane send-keys`, `api schema`, `notification show`). No socket
  and no PTY parsing.
  `agent_status` is Herdr's word about a worker and the only one this repo
  accepts for whether it is working; TWO things are read off a pane's own
  screen, because Herdr has no status for either: whether a `/goal`
  registered, and whether the client's trust-folder dialog is up.

## Principles

- **The decision core is pure.** Given a snapshot of facts (board rows, pane
  states, bindings, the clock), it returns the actions to take. It spawns
  nothing, reads nothing, and is tested without processes. All process
  spawning lives in thin adapters at the edge.
- **Claiming stays the worker's act.** The dispatcher prompts; the worker
  claims. A goal that was delivered but never claimed times out and is
  re-sent; the dispatcher never claims on a worker's behalf.
- **Spawn-per-task is policy, not a hard rule.** The default worker is a
  fresh pane per task; a rejected task re-prompts the pane that produced it,
  because rework benefits from context. The hard invariant is one live task
  per pane.
- **A worker never shares a human's tab.** It comes up in a tab this daemon
  created, in the workspace of the pane the report is addressed at, and a tab
  belongs to ONE task — the label is both the operator's signpost and the
  ownership evidence, and it is the only thing that earns a `tab close`. This
  is a correctness rule, not a tidiness one: every pane added to a tab narrows
  the others, and a pane narrow enough word-wraps the very phrase the screen
  match is looking for.
- **The dispatcher stops at review.** Verification and approval belong to the
  board's review gate. This binary never runs `htask approve`, `htask reject`,
  or any note verb. The one exemption is `hdis parked resolve --reject`, which
  closes a row in this binary's own document and never touches the board.
- **The verification lane lands in the worker's own pane.** When it is on, a
  submission earns ONE self-review shot — a second `/goal` prompted into the
  pane that produced the work. Nothing separate launches; `verify.profile` is
  refused at config parse by name, because an operator who set it believes a
  verifier pane is running.
- **Every event on the trail is this daemon acting, with one exception.**
  `dispatch.parked.resolved` is filed under the CALLING principal (§3.7), and
  carries `on_behalf_of_operator: true` in its `detail` when that principal is
  not `human`. A new event that is not the daemon's own act needs the same
  treatment written down beside it.
- **Fail loud, idle safe.** When `htask` or `herdr` is unreachable, say so
  and keep ticking; never guess at state, never queue writes for later.

## Conventions

- Test-first for every behavior in the decision core: the test exists and
  fails before the code that makes it pass.
- No `panic` in production code paths; errors carry what the operator needs
  to act.
- Dependency budget: two libraries. The official MCP go-sdk
  (`github.com/modelcontextprotocol/go-sdk`), which the MCP door is built on,
  and `github.com/spf13/cobra`, which the CLI is built on, because §6/§7 have
  the sibling binaries present one flag shape and the siblings are
  already cobra. Both are pinned to the versions the board plugin runs.
  Everything else is the standard library, and the next dependency earns its
  way in with the reason recorded in the README.
- Lowercase conventional commits, no emojis, no co-author lines.
- Everything committed is English. Tests, docs, comments, commit messages.
