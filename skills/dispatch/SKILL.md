---
name: dispatch
description: The dispatcher that puts a worker agent on a ready task from the htask board. Use when asking for a worker on a task, checking what the dispatcher is driving, finding out why a dispatch refused, or working out where a worker owes its report. Trigger words - dispatch, worker, dispatcher, spawn, pane, hdis, HDIS_DISPATCHER_PANE.
---

# Dispatch

`hdis` is the dispatcher. It watches the htask board for ready work, opens a
Herdr tab for each ready task and brings a worker up in it, delivers the
task's goal, tracks the worker, and hands off at review — where the board's own
review gate takes over.

The board stays the ledger. This binary is execution policy and nothing else.

## Reach for the tools, not the shell

Every verb is an MCP tool on the `herdr-dispatch` server AND an `hdis`
subcommand. **Use the tools.** They take typed arguments and answer with a
document, and nothing about them depends on `hdis` being on your PATH, which
it may not be. The CLI is there when you have a shell and want one; neither
surface grants anything the other does not.

## dispatch does not wait

```
dispatch { "task": "01M0N5HG0CCEXW1A0TVF2HZQTG" }
status   { }
```

`dispatch` reserves the task and returns at once. It **does not wait for a
worker**: bringing one up runs past three minutes in the worst case — the
pane's shell, the agent's startup, a trust dialog that may never come, and the
wait for the goal to show on screen — and no caller holds a call that long.

**A returning `dispatch` is a reservation, not a worker.** Read the outcome
with `status`, which is one row per binding: the task, the pane its worker
lives in, the tab that pane is in, the worker's `agent_status` as Herdr reports
it, the branch its commits are on — marked `(behind)` when the project's HEAD
has moved past it, which is what makes a fast-forward merge refuse — when the
goal was delivered, how often, and whether review was announced.

It refuses with a name rather than a sentence to parse. The `code` is one of
the shared contract's nine, and the sub-reason is the first word of the
`message`: `CONFLICT` as `NOT_READY` when the board will not hand the task
out, as `AT_CAPACITY` when the fleet is already full, or as
`ALREADY_DISPATCHED` when this daemon is driving it; `UNSUPPORTED` as
`NO_BASE_PANE` when there is nowhere to put a worker; `NOT_FOUND` when no
board has it; `USAGE` when no task was named; and `UNAVAILABLE` when the board
itself could not be read.

## Name the task by its id

A task crossing boards **must be the 26-character id**. A number is the task's
place on one board and is **only unique inside a project**, so it cannot
address a task anywhere else. `hdis` resolves a number against the ready list
alone and never widens the question, so a number belonging to another board
comes back as `CONFLICT: NOT_READY` saying it is not among the tasks on offer
— not as a word about the task, because nothing here can look it up. Pass the
number only for a task on the board you are standing on; pass the id for
anything else.

## What the dispatcher will not do for you

**The worker claims the task itself.** `hdis` prompts and reserves;
**nothing here claims on its behalf**. A goal that was delivered and never
claimed times out and is re-sent — the claim stays the worker's own act.

And it stops at review. This binary **never runs `task approve` or `task
reject`**, or any note verb: verification and approval belong to the board's
review gate, and a named test walks the source to keep it that way.

**Which agent kind, model, effort and args a worker launches with is this
binary's config** — `~/.config/dispatch/dispatch.toml` — and is **not selectable per
call**. There is no argument for it, and the board carries no profile field,
for the same reason: which agent runs the work is execution policy, not a fact
about the task.

## HDIS_DISPATCHER_PANE, if you are the worker

Every pane the dispatcher opens is launched with

```
HDIS_DISPATCHER_PANE=<a Herdr pane id>
```

in its environment. If you find it there, it is **the address you owe your
report at**, resolved in three rungs: the pane the board's row says the task
was created from; failing that, a live pane already sitting in the task's own
project; failing that, the daemon's own pane. Answer there rather than at a
pane id somebody wrote into your prompt: the dispatcher moves panes between
restarts, and the variable is read fresh.

It is an address and nothing more. It **never says who you are**: the sender
on anything you write is stamped by the mail daemon from `HERDR_PANE_ID`,
which is Herdr's word about the pane you run in, and there is no argument that
sets it.

## When a dispatch refuses

```sh
hdis doctor
```

`doctor` **says why a dispatch would refuse** before one is tried: the
daemon's version, the base pane it splits workers off, whether the board and
Herdr are reachable, and whether the verification lane is on — a submission
earning one self-review shot in the worker's own pane, re-sent while your pane
reads idle, because a prompt Herdr accepted is not a prompt you saw. Run it
first — a refusal is usually one of those four.

## Through MCP instead of the CLI

Every verb this binary has is one of the `herdr-dispatch` server's MCP tools,
named by the verb alone — seven of them: `doctor`, `dispatch`, `status`,
`stop`, `dump`, `parked_list`, `parked_resolve`.
Your client shows them under the server's own label, which is what tells you
whose `dispatch` you are calling. Nothing is reachable by shell alone, so a
harness with no terminal loses no verb.

`stop` is a brake on the WHOLE dispatcher, not on one task. Every worker it is
driving keeps running in its pane, and no new one comes up until a daemon is
started again — so confirm with the operator before calling it, the same way
you would before any act whose blast radius is everyone else's work.

## When the policy gate answers

`dispatch` and `stop` are the two verbs that change the world here, so both
pass the operator's policy gate before anything happens. Most fleets configure
none, and an unconfigured gate allows everything — you will never see it.

When one is configured it can answer three ways. Allow is silent. **Deny** is
`DENIED`, and the message carries the gate's own reason; that is final, so
read the reason and do not retry. **Defer** is also `DENIED`, and the envelope
carries a `parked_id`: your call was recorded rather than performed, and it is
waiting for the operator.

```sh
hdis parked list                     hdis parked resolve <id>
hdis parked list --json              hdis parked resolve <id> --refuse
```

A parked call is **the operator's to decide**, not yours. Tell them the id and
what you were trying to do; resolve one yourself only when they have said to.
Resolving re-runs the verb as whoever the gate stopped, never as you, and the
row records that you were the one who let it through.

## Everything else

```sh
hdis status --json                   hdis --help
hdis dump --json
```

`dump` prints everything the dispatcher remembers across restarts — the
bindings, the reservations, the parked actions — in one document. It is a
debugging read, not a source of board facts: task state, claims and leases are
htask's, and `htask` is where you read them.

`hdis --help` lists the same seven verbs the door serves, for a caller with a
shell. Add `--json` to
any verb for one machine-readable document, and those bytes are the same
document the MCP tool hands its caller.
