---
name: dispatch
description: The dispatcher that puts a worker agent on a ready task from the htask board. Use when asking for a worker on a task, checking what the dispatcher is driving, finding out why a dispatch refused, or working out where a worker owes its report. Trigger words - dispatch, worker, dispatcher, spawn, pane, hdis, HDIS_DISPATCHER_PANE.
---

# Dispatch

`hdis` is the dispatcher. It watches the htask board for ready work, splits a
Herdr pane for each ready task, delivers the task's goal, tracks the worker,
and hands off at review — where the board's own review gate takes over.

The board stays the ledger. This binary is execution policy and nothing else.

## dispatch does not wait

```sh
hdis dispatch 01M0N5HG0CCEXW1A0TVF2HZQTG
hdis status
```

`dispatch` reserves the task and returns at once. It **does not wait for a
worker**: bringing one up runs past three minutes in the worst case — the
pane's shell, the agent's startup, a trust dialog that may never come, and the
wait for the goal to show on screen — and no caller holds a call that long.

**A returning `dispatch` is a reservation, not a worker.** Read the outcome
with `status`, which is one row per binding: the task, the pane its worker
lives in, when the goal was delivered, how often, whether review was
announced, and the worker's `agent_status` as Herdr reports it.

It refuses with a name rather than a sentence to parse: `NOT_READY` when the
board will not hand the task out, `NOT_FOUND` when no board has it,
`AT_CAPACITY` when the fleet is already full, `ALREADY_DISPATCHED` when this
daemon is driving it, and `NO_BASE_PANE` when there is nowhere to put a
worker.

## Name the task by its id

A task crossing boards **must be the 26-character id**. A number is the task's
place on one board and is **only unique inside a project**, so it cannot
address a task anywhere else and the board answers `USAGE` when it is asked
to. Pass the number only for a task on the board you are standing on; pass the
id for anything else.

## What the dispatcher will not do for you

**The worker claims the task itself.** `hdis` prompts and reserves;
**nothing here claims on its behalf**. A goal that was delivered and never
claimed times out and is re-sent — the claim stays the worker's own act.

And it stops at review. This binary **never runs `task approve` or `task
reject`**, or any note verb: verification and approval belong to the board's
review gate, and a named test walks the source to keep it that way.

**Which agent kind, model, effort and args a worker launches with is this
binary's config** — `~/.config/hdis/hdis.json` — and is **not selectable per
call**. There is no argument for it, and the board carries no profile field,
for the same reason: which agent runs the work is execution policy, not a fact
about the task.

## HDIS_DISPATCHER_PANE, if you are the worker

Every pane the dispatcher splits is launched with

```
HDIS_DISPATCHER_PANE=<a Herdr pane id>
```

in its environment. If you find it there, it is **the address you owe your
report at** — the pane the task was created from when the board's row names
one, and the daemon's own pane when it does not. Answer there rather than at a
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
named by the verb alone — four of them: `doctor`, `dispatch`, `status`, `stop`.
Your client shows them under the server's own label, which is what tells you
whose `dispatch` you are calling. Nothing is reachable by shell alone, so a
harness with no terminal loses no verb.

`stop` is a brake on the WHOLE dispatcher, not on one task. Every worker it is
driving keeps running in its pane, and no new one comes up until a daemon is
started again — so confirm with the operator before calling it, the same way
you would before any act whose blast radius is everyone else's work.

## Everything else

```sh
hdis status --json                   hdis --help
```

`hdis --help` lists the same four verbs the door serves. Add `--json` to
any verb for one machine-readable document, and those bytes are the same
document the MCP tool hands its caller.
