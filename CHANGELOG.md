# Changelog

What a consumer of `hdis` has to change between released versions. §13.3 of
the shared plugin contract makes the CLI, the MCP tool list, the JSON shapes
and the error codes stable within a minor and changeable between minors with an
entry here, so every entry says what moved and what a caller does about it.

## Unreleased

**Changed: `doctor`'s `gate.parked` counts the board the call named, and
`gate.parked_everywhere` counts the daemon (§10.3).** 0.7.1 narrowed
`parked list` to the board it was called on and left the doctor count alone as
the daemon's own health, and the two then disagreed in front of the operator:
`hdis doctor --project <path>` said two parked while `hdis parked list
--project <path>` beside it listed one, and the figure an operator acts on is
the one they are about to read. `gate.parked` now applies the same scope as
the list — a call naming a board counts that board's rows, a row parked by a
call that named no board belongs to no board and is counted under none, and a
call that names no board is this daemon's every-board default and counts
everything, so an unscoped `hdis doctor` is unchanged. The daemon-wide figure
is not lost: `gate.parked_everywhere` is always present and always the whole
daemon, because one daemon serves every board and an operator standing on one
project still has to see that another has a decision waiting. The plain-text
`gate` line prints both when they differ, and says `none parked on this board`
when this board is clear and another is not. A consumer reading `gate.parked`
off a scoped `doctor` sees it narrow to that board and should read
`gate.parked_everywhere` for the fleet figure it used to get.

## 0.7.1 — 2026-08-27

**Fixed: `parked list` answers the board it was called on (§4.4).** A parked
row has always carried the scope its call was made with, and the list threw it
away: every deferral came back whatever board was named, so an operator on one
project was handed another project's decision to make, and the daemon
contradicted the tool description and the CLI, which both said the verb was
project-scoped. `hdis parked list --project <path>`, and `parked_list` with
`project`, now list that board's rows and no others. A row parked by a call
that named no board belongs to no board: it is never listed under a project,
and since `--all-projects` is refused here, the one reading that lists it is a
call that names no board either — which is this daemon's every-board default,
so `hdis parked list` with no scope is unchanged and still shows everything. A
consumer that read `parked list --project` and got the fleet's rows sees it
narrow; one that named no board sees the same list as before. `hdis doctor`'s
`parked` count is deliberately untouched: it is the daemon's own health, and
the daemon serves every board.

## 0.7.0 — 2026-08-27

**Added: `[worker] mcp_config`, and the doors a worker is launched with.** A
worker's argv now carries `--mcp-config <path> --strict-mcp-config`, so one
document is every MCP door it has. Until now a worker discovered its doors
through `~/.mcp.json` in HOME, which is the OPERATOR's file: an operator who
keeps only the Agamemnon hub connector in it handed every worker a pane with
the hub's doors and none of the local ones, and the local doors are what derive
a worker's principal from its pane (contract §3.1). `[worker] mcp_config` names
the document fleet-wide, `mcp_config` on a profile overrides it for that
profile's workers, and a path that is configured and not there is refused when
the spawn is assembled — naming the path, before a pane or a checkout is made —
as well as reported by `hdis doctor`. When neither names one, `hdis` writes
`<state_dir>/worker.mcp.json` once, at the first spawn that needs it, holding
exactly the four plugin doors (`htask mcp`, `hmail mcp`, `hdis mcp`,
`hsched mcp`) with each command resolved to the absolute path PATH gave at that
moment, and the bare name where PATH gave nothing; it is never rewritten, so an
edited file stays edited and a deleted one comes back as the default.
`hdis doctor` grows a `worker` object — `mcp_config`, `mcp_config_configured`,
`mcp_config_exists` and `profile_mcp_configs` — and a `worker` line in the
plain-text report. A consumer pinning the worker's argv sees two new flags at
the front of it, ahead of the profile's own arguments and, on a `codex`
profile, behind the `--settings` the launcher's half still leads with.

## 0.6.0 — 2026-08-27

**Added: `account` on a worker profile, and a `doctor` finding for one the
proxy does not hold.** A `provider = "codex"` profile may now name a stored
account of the proxy launcher, and its workers run as that account: `hdis`
exports the launcher's own per-session tag,
`ANTHROPIC_AUTH_TOKEN=proxenos-account:<name>`, into the worker's pane after
the `eval "$(proxenos env)"` that supplies the routing. The key is optional
and a document without it is unchanged in every byte, down to the shell line
the pane is given. `account` on a profile of any other provider is refused
when the document is read, naming the profile, and so is a name outside
letters, digits, `.`, `_` and `-`, because the name is written bare onto that
shell line. `hdis doctor` checks every configured account against
`proxenos accounts list --json`: `proxy.missing_accounts` is a list of
`{profile, account}` and `proxy.accounts_error` says why the store could not
be read at all, with the plain-text report carrying an `accounts` line when
either has something to say. A missing name is a finding and not a failure of
`doctor`. Caveat: the tier aliases `proxenos env` sets up (`opus`, `sonnet`,
`haiku`) follow the launcher daemon's own tier config rather than the account,
so a profile pinned to an Anthropic account must name a real Anthropic model.

## 0.5.0 — 2026-08-27

**Added: `dispatch.parked.failed`, filed under the resolver (§3.7).** A parked
action the operator let through whose verb then errored now writes its own
event, and it is the other outcome of the same decision: the actor is the
principal that resolved the row rather than this daemon, and when that
principal is not `human` its `detail` carries `on_behalf_of_operator: true`,
exactly as `dispatch.parked.resolved` does. `detail.resolved_by` is there too,
beside `subject`, `verb`, `target`, `state` and the `error` the parked row
already showed. Nothing else moved: the row is still marked `failed` and still
stays in front of the operator. A consumer watching the trail for a decision
that did not take effect no longer has to infer it from `parked list`.

## 0.4.1 — 2026-08-27

**Changed: `parked list` refuses `--all-projects` (§4.4).** The flag selected
nothing there — a parked row here belongs to no board — and was accepted and
ignored; `hdis parked list --all-projects`, and `parked_list` with
`all_projects`, now answer `USAGE`. Drop the flag: the list is unchanged
without it, and every other verb still takes it.

**Changed: `dispatch.parked.resolved` names the caller, not this daemon
(§3.7).** Resolving a deferral is the operator's authority, so the event
for it is now filed under the principal that decided it — `agent:wM:p1`,
`human`, or the `none` of §3.7 — where it carried this daemon's own
`plugin:hdis@<pane>` before. Every other event on the trail is unchanged and
still this daemon acting. A consumer grouping the trail by `actor` sees the
resolutions move off the daemon's own name; one reading `detail.resolved_by`
is unaffected, because that field stays exactly where it was and says the same
thing.

**Added: an operator verb an agent performed labels itself.** When the
principal resolving a parked action is not `human`, the event's `detail`
carries `on_behalf_of_operator: true` — the key both sibling plugins
write, so an operator reading three trails matches one word. The operator's
own resolution carries no such key. Nothing refuses on it: §3.7 makes an
operator verb advice an agent confirms with the user rather than a refusal a
door makes, and the trail is what carries the accountability instead.

**The declared contract revision is now 0.10.1**, up from 0.10.0. It is the
value `hdis doctor` reports as its own top-level `contract`, distinct from the
`board.contract` it relays from htask. 0.10.1 is the revision that catches the
text up with what the four plugins already do; where it reaches this plugin
it lands as a closed entry in `docs/contract-notes.md` rather than as work —
§5.4 now names the id shape a JSON-document store mints, which is the shape
this plugin has always minted. The two changes above are the work,
and they are §3.7's, which 0.10.0 already required.

## 0.4.0 — 2026-08-27

**Added: `hdis doctor` prints the calling principal.** `principal` is a new
top-level field on the doctor report and a new line in its text output,
carrying the principal this daemon recorded for that very call — the same
answer the §9 gate is asked about and a parked row is filed under, so the
three cannot disagree. It is what §7.5 leans on: a doctor call through a door
started with `--operator` answers `human` and one through an undeclared door
does not, which is how an operator checks which of their registrations speak
for them. A caller reading the report by field name is unaffected; one
matching the text output line by line gains a line after `contract`.

**Changed: a caller with no pane and no declaration is `none`, not
`unknown` (§3.7).** The contract spells the no-principal case `none` and the
sibling plugins say `none`, so a fleet-wide gate script matching one word had
to match two. A gate script matching the subject `unknown` must match `none`
instead. The same string is what `hdis doctor` prints as `principal` and what
a parked row carries as its subject and resolver. Nothing migrates: the
principal on a parked row is free-form and no code path compares it to a
literal, so a row parked before this change still reads, lists and resolves
under the `unknown` it was written with.

**Changed: a paneless CLI call is the operator (§3.6).** The CLI door now
sends the human act its own argv is, so `hdis doctor` outside a Herdr pane
answers `human` where it answered `unknown`, and the §9 gate is asked about
`human` rather than `unknown` for the same call. A gate script matching the
subject `unknown` for CLI calls has to match `human` instead. A call from
inside a pane and a call with `--as` are unchanged: the pane still wins, and
so does the declaration.

**Added: `hdis mcp --project <path>` sets the door's default board.** The
root's persistent `--project` parsed on the door's own command line took
effect nowhere; it now names the board every tool call through that door
defaults to. A call that passes its own `project` is still answered on that
one, and `all_projects` still gets every board — the explicit argument wins.
A door started without the flag defaults to every board exactly as before.

**Fixed: an idle worker on a doing row is nudged once per timeout, not every
tick.** An idle worker holding a claimed task was re-prompted on every tick
with no cooldown, unlike an unclaimed one; both now wait out the claim
timeout between nudges. The typed-line budget is also checked before a line
is typed into a pane, so a long profile or temp path is refused instead of
reaching the shell as the corrupted line the budget exists to prevent.

**Added: `hdis mcp --operator` declares the door speaks for the operator.**
§7.5's declaration is read once from how the server was started, so a door in
no pane — a desktop harness resolving a parked row — is the operator to the
policy gate and to `parked resolve` rather than an unknown caller. A pane
still wins, and a door started with the flag inside one refuses to come up at
all with `FORBIDDEN` naming the pane, rather than running with two disagreeing
answers about who it is. The declaration may not be carried per call: a tool
call with an `operator` argument is refused `USAGE` naming `hdis mcp
--operator` instead. A door started without the flag behaves exactly as it did.

## 0.3.0 — 2026-08-25

**Changed: a worker is told which checkout is its own, and a second one is
refused after an escape.** The spawn condition now says that the working
directory is the only writable checkout, that everything else including a
sibling repository is read-only, and that a task needing a sibling changed is
filed on that sibling's board. It grew by 127 characters, so
`spawn.TypedLineBudget` moved from 512 to 640, still under half the ~1.4k
line that broke on a live shell. Separately, `spawn()` now refuses a task
whose branch was already handed to a worker and never moved while the
project's HEAD moved past it: that pair is the signature of work that landed
outside its own checkout, and it is logged and left on the board rather than
given a second pane. Nothing in the CLI, the MCP tool list, the JSON shapes
or the error codes changed.

**Added: a task's priority routes it to a profile.** Named `[[route]]` blocks
in `dispatch.toml` pair a minimum priority with a profile name; the highest
matching minimum wins, and a task below every minimum launches with the
profile its project would have given it anyway. A document with no routes
behaves exactly as it did. A route naming a profile that is not defined is
refused when the config is read, at startup. `hdis status` gained a `profile`
per worker, in the JSON and on the terminal line, recording what the worker
was LAUNCHED with rather than what the document says now. The board is
untouched: priority is a fact it already keeps, and what a priority earns is
this binary's config.

**Changed: `[[route]]` is readable config.** The TOML subset accepted arrays
of tables nowhere and refused them by line; it now reads them into a list.
Every other refusal is unchanged.

**Changed: the `proxy` config key is a table, not a string.** `proxy =
"/opt/homebrew/bin/proxenos"` is refused at parse, naming the new spelling:
write `[proxy]` with `bin = "..."` under it. The table is where the quota
policy lives too, so the launcher and what may be spent through it sit
together. A document that never named a launcher is unaffected: `bin` still
defaults to the literal `proxenos`.

**Added: a codex spawn asks the proxy what the account has spent first.**
Before a `codex` worker is brought up, by a tick or by `hdis dispatch`, the
dispatcher reads `proxenos usage --json` — the cheap default, never
`--refresh`, once per tick — and refuses when the serving account reports
`limit_reached`, or when its fullest window is at or past `[proxy]
max_used_percent`. The threshold is unset by default, which is no threshold.
The refusal is `CONFLICT` as `AT_QUOTA` and names the account and both
figures. Only the `codex` lane is gated: a `claude` worker never routes
through the proxy, and a `claude` task queued behind a gated one still takes
its slot. An unknown quota — a metered key, or a proxy that could not be
reached — gates nothing and is logged. `hdis doctor` gains a `quota` line, and
`proxy.quota` in the `--json` shape, carrying `known`, `limit_reached`,
`used_percent`, `max_used_percent`, `account`, `plan` and the `refusal` a
spawn would meet now.

**Changed: a daemon with no base pane now adopts one instead of refusing for
good.** `HERDR_PANE_ID`, `--pane` and the config's `"pane"` key are unchanged
and still win. What is new is the answer when none of the three gives one: the
daemon asks Herdr once per interval, and again on any dispatch, and takes the
lowest live pane id that is neither one of its own workers nor in a tab it
opened. It opens nothing. This is what a daemon Herdr's plugin manager starts
at boot needs — it has no `HERDR_PANE_ID`, and naming a pane in the config is
wrong because pane ids are not durable across Herdr restarts. `NO_BASE_PANE`
is unchanged as the refusal while there is still nothing to adopt; `doctor`
now says `base pane none yet` rather than `none` there.

**Changed: `internal/fake` is `internal/testenv`.** Internal test package, no
consumer surface; the name is the one herdr-tasks and herdr-mail already give
the same job of putting stand-in binaries on PATH for a test, and
`docs/repo-standard.md` now names it in the package table so a third spelling
is recognisable as one.

**Added: every MCP tool takes `project` and `all_projects`.** The scope the
CLI has carried since the cobra move reaches the MCP door: each tool's schema
now publishes the two properties, a relative `project` is resolved to the
canonical §4.1 path in the door rather than against the daemon's working
directory, and naming both is refused rather than ranked. A caller that names
neither is answered exactly as before, because every board is still this
daemon's default on both doors.

**Changed: the socket verbs `parked_list` and `parked_resolve` are now
`parked.list` and `parked.resolve`.** The MCP tool names do NOT move: they are
`parked_list` and `parked_resolve`, as released. What changed is where the
tool name comes from. `Verb` carries an `MCP` field holding it, so the
namespaced verb is dotted on the socket the way both sibling plugins spell
theirs, and an absence from the agent surface is a decision written beside the
verb rather than a transformation applied at the door. A caller on the CLI or
the MCP door changes nothing. A caller writing socket requests by hand sends
`"verb":"parked.list"` instead of `"verb":"parked_list"`.

**Changed: `internal/herdr` is `internal/herdrclient`.** Internal package, no
consumer surface; the name is the one herdr-tasks and herdr-mail give the same
`herdr <verb>` adapter.

**Added: an event trail, `hdis events`, and the `on_event` hook (§8.1, §8.2,
§8.3).** Every state change this dispatcher owns is now recorded: a task
reserved or given back, a worker spawned, adopted, prompted, retired or gone,
review announced, a call the policy gate parked or the operator decided. The
names are `dispatch.<entity>.<kind>` and the payload is §8.1's. Board facts
are NOT on it — a task claimed, submitted or approved is htask's own trail —
because a second ledger of facts this plugin does not own is what the boundary
with htask forbids.

`events` is on both doors. `hdis events` reads from the beginning of what is
held, oldest first, with `--since <id|ms>` to resume, `--limit <n>` for one
page, and `--json`. `--follow` is the CLI's alone: a tool call answers once,
so the MCP `events` tool takes `since` and `limit` and no `follow`. The trail
is bounded to the newest 1000 events, and a `--since` id that has rotated past
that bound is refused with `USAGE` rather than answered with the whole window
again, which a resuming consumer would take for the tail of its own stream.

The hook is one config key, `on_event = ["cmd", "args"...]`, run detached with
all three stdio closed for every event, carrying `HDIS_EVENT`, `HDIS_ENTITY`,
`HDIS_ID`, `HDIS_PROJECT`, `HDIS_ACTOR`, `HDIS_KIND`, `HDIS_AT` and
`HDIS_DETAIL`. A hook that fails does not fail the write that caused it.
`hdis doctor` gained an `events` block naming the hook and how much trail is
held, and `hdis dump` carries the trail, because §5.8 is the whole store.

Nothing a caller had before moved: no verb, argument, JSON field or code
changed shape. The store document gained an `events` array, and a document
written without one still loads.

The three sibling plugins — `hdis`, `htask`, `hmail` — are now maintained
against one written standard,
[`docs/repo-standard.md`](docs/repo-standard.md). One operator-facing name
moves here to match it: the §9.2 policy gate command is configured as
`gate_command = [...]` and no longer as `gate = [...]`. That is what
herdr-tasks and herdr-mail have always called it, and a config still carrying
`gate` is refused by name rather than accepted with the gate quietly off.

`DISPATCH_E2E_REQUIRED=1` is new and turns layer 3's loud skip into a failure.
`make release-check` sets it and is the target herdr-tasks spells the same
way: a release must not be cut on a suite that silently did not run.

**The CLI is cobra, and takes the four globals the contract fixes.** `hdis`
was the odd one out: stdlib `flag` with one hand-written usage block, where
`htask` and `hmail` are both cobra. §6 and §7 have the three binaries present
one flag shape, and this one could not. What a caller gets now: `--project`,
`--all-projects` and `--as` beside the `--json` that was already there;
`hdis <verb> --help` for every verb, carrying the same description the MCP
tool does, instead of one block for the whole binary; and
`hdis completion <shell>`, which there was no way to offer before.

Nothing a caller wrote before means something else now, with one exception for
anyone scripting the daemon: **its flags are `--flag`, not `-flag`.**
`hdis daemon -interval 1h` is `hdis daemon --interval 1h`, and the same for
`-config`, `-log`, `-once`, `-pane`, `-max-workers`, `-claim-timeout`,
`-max-prompts`, `-start-timeout` and `-confirm-ceiling`. The verbs themselves
never took a single-dash flag. `--json` still reads wherever in the line it is
written, which it needed a special case to do before and now simply does.

`--project <path>` narrows a call to one board, resolved in the door to §4.1's
canonical project. It is what makes a bare number dispatchable: a number is
unique only inside a project, so `hdis dispatch 7` with no board named can
only match a task the ready list already carries, where
`hdis --project . dispatch 7` looks on one board. Every call still defaults to
every board, which is what a daemon driving the whole fleet has to do, and
`--all-projects` is the explicit spelling of that default; naming both is
refused rather than ranked. `--as` declares a `cron:`, `trigger:` or `plugin:`
principal (§3.2) and refuses `agent` and `human`, which are derived from the
calling process.

One failure was misreported before and is fixed with this: an unknown flag or
an unknown subcommand now answers `USAGE` and exits 2, where the old CLI gave
the flag package's own message and exit 1, and where an unnamed error would
otherwise fall through to `UNAVAILABLE`.

This adds `github.com/spf13/cobra` as this binary's second dependency, pinned
to the version both siblings already run. The reason is in the README's
dependency section.

## 0.2.0 — 2026-08-24

The release that makes `hdis` refuse the way the contract says a plugin
refuses, and it breaks two things a consumer may have built on. The seven
names this binary answered with — `NOT_READY`, `AT_CAPACITY`, `NO_BASE_PANE`
and the rest — are no longer top-level codes but sub-reasons inside the
message, so a program matching one as the `code` matches nothing now; and
every failure used to exit 1, where each now exits the status §6.3 fixes for
its code. The operator's own move is the config: `~/.config/hdis/hdis.json` is
`~/.config/dispatch/dispatch.toml`, the state directory and the environment
prefix moved with it, the document is TOML rather than JSON, and nothing reads
the old file. What is added on top of that: a `--json` failure is one envelope
on stdout, a §9 policy gate with parking and the `parked list` / `parked
resolve` verbs behind it, `dump` on both doors, Herdr feature-detected at
start so a missing capability refuses instead of being called, `HERDR_BIN_PATH`
honoured, and a third test layer that drives the built binary against a real
`htask`.

**Breaking: the error codes, the exit statuses and the `--json` failure
envelope are the contract's.** The seven names this binary refused with were
top-level codes §6.3 does not have, and every failure exited 1 whatever it
said. They are sub-reasons now, under the contract code each belongs to and
kept as the first word of the message, so nothing a caller could read is gone
and the exit status is the one §6.3 fixes: `USAGE` 2, `NOT_FOUND` 3,
`UNAVAILABLE` 4, `TIMEOUT` 5, `CONFLICT` 6, `UNSUPPORTED` 7, `FORBIDDEN` 8,
`DENIED` 9, `UNEXPECTED` 1. With `--json` the failure is one envelope on
stdout, the same document the MCP door builds, and `--json` is read wherever
the caller wrote it. A caller branching on `code` reads the nine words every
sibling plugin answers with; one that branched on `NOT_READY`, `AT_CAPACITY`,
`ALREADY_DISPATCHED`, `ALREADY_RUNNING`, `NOT_RUNNING`, `NO_BASE_PANE` or
`INVALID` as a top-level code must read the sub-reason at the front of the
message instead.

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

The reject switch is `hdis parked resolve --reject <id>`, spelled as the
sibling plugins spell the same operator verdict. It closes a parked action of
this daemon's and never a board submission — no task moves and the board is
not called — so the guard here that fails on the board's review words
appearing as arguments in its own source carries a named exemption for this
one switch, rather than a caller having to remember a spelling only this
plugin uses. The switch comes before the id, as every switch on this CLI
does.

`hdis version` prints the plugin name and the contract revision beside the
version, and takes `--json` for the same three facts, which is the shape both
siblings already had. A caller parsing the bare line gets more on it than
before; one that wants a field reads `--json`.

`herdr` is now run from `HERDR_BIN_PATH` when the environment names it. §11.1
names the variable and nothing here read it, so a host that installs Herdr off
PATH — which is what the variable is for — had every call fail on a binary
that was not missing. It is read once when the client is built, never inside
the call, so a test's fake on PATH is not bypassed by the operator's own
environment. §10.3's two directories are on `hdis doctor` beside it, so an
operator whose override is not taking effect can ask the running daemon which
pair it resolved.

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

There is a verification lane, and it is off unless the config document turns
it on. With it on, a task in review that this daemon's own worker submitted
earns a SELF-REVIEW SHOT: one `/goal` armed in the worker's OWN pane — no
second pane, no second agent, no throwaway checkout — carrying a second
condition. It never approves and never rejects, and neither does this binary:
the board adapter carries no review verb, and no source file passes one as an
argument. One submission earns one shot; a task leaving review rearms the
lane, so a re-submission after a rejection earns another.

What the second condition asks for is the whole point of the lane. Not "review
your work": the blind spot that produced the work survives rereading it, and
an independent verifier reads past the same five rejections the worker did. It
asks for the mechanical thing — for every guard, refusal or invariant the
report claims, write a COMPILING mutation that removes it, run the tests the
report names, confirm they FAIL, revert — and then for which mutations bit,
which did not, and whether the worker reads each miss as a missing test or bad
aim. It names where that goes: the mail door at `$HDIS_DISPATCHER_PANE`. A
worker whose task is in review cannot amend its own report, because `htask`
refuses a submit on a row that is not `doing`, so findings with no route named
die in the pane.

The shot is not spent by the call that sent it. §11.4 forbids reading a
successful `agent prompt` as delivery, and the lane was doing exactly that:
the binding was marked the moment Herdr accepted the text, and only a task
LEAVING review cleared the mark — so a prompt that was accepted and seen by
nobody burned the submission's one check with the board still green and
nothing anywhere saying so. The shot is re-sent while Herdr still calls that
worker idle, bounded by the same `max_prompts` and claim timeout the unclaimed
nudge already uses. A worker that received the condition is working and never
meets a second copy.

Nothing about review authority moves. The shot produces no verdict: the task
stays in review and the operator still approves or rejects.

Callers turn the lane on with a `verify` object in the config document
carrying `enabled` and nothing else. The shot lands in a pane already running
the worker's own profile, so there is no profile to name: a document carrying
`verify.profile` is refused at parse, by name, rather than starting a daemon
whose operator believes a separate verifier is running. Left out, the lane is
off and nothing changes.

`doctor`'s JSON gains a `verify` block carrying `enabled`, and nothing beside
it. Its prose output names the lane too, before the board line, so an
unreachable board does not hide the dispatcher's own configuration. `status`'s
JSON gains `kind` on each worker, and `worker` is the only value it takes —
there is one lane, and the field says so rather than leaving a caller to infer
it. Callers that parse `doctor` or `status` see added fields and no removed
ones; a parser that ignores unknown fields does nothing.

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

The report address has three rungs. `HDIS_DISPATCHER_PANE` is the task's pane
of origin when the board names one and that pane is alive; else a LIVE pane
whose cwd is the task's project, read from `herdr pane list`; else the
daemon's own base pane. A task an operator filed at a terminal names no pane —
nothing with a pane created it — and every report for one used to go to
whoever started the daemon, even while another live session was sitting in
that very repository and was relayed the report by hand. Among several
candidates the lowest pane id wins, because that is the answer that stays the
same across ticks; a pane sitting under this daemon's own checkout root is
never chosen, so a worker for that project does not become the address. The
base pane stays as the last rung, so a machine with nothing live still answers
somewhere. The workspace a worker's tab opens in comes from the same resolved
desk rather than a second rule of its own, and the pane list is read once per
spawn instead of twice.

An agent in a dispatched pane reads that variable to find out where its report
goes, and answers there rather than at any pane written into its text. The
name is the whole contract and is pinned by test against what the README
documents. It is set on the pane itself, so it costs nothing on the line Herdr
types into the pane and does not go stale when the daemon moves pane. It is an
address only: the sender of anything a worker writes is stamped by the mail
daemon from `HERDR_PANE_ID`.

Callers read the variable and answer there, as before, but must stop treating
its value as one fixed pane. Two panes this same daemon opened can hold
different addresses, because they are answering to different filers. Anything
that cached the value across tasks, or hard-coded the daemon's pane in its
place, is wrong now and should read the variable in the pane it is running in.

The trust-folder dialog is answered again. `TrustDialogMarkers` held one
phrase, `"do you trust the files in this folder"`, and claude 2.1.239 reworded
that dialog, so `answerStartupDialog` matched nothing at any pane width and
never pressed the Enter it exists to press. Since every worker worktree is a
fresh untrusted directory and `herdr agent start` there returns
`agent_not_ready` with the dialog still on screen, that Enter is the only
thing that lets a goal be delivered. The markers are now a SET, matching the
new dialog's selectable option `"yes, i trust this folder"` alongside the
older phrase, so an operator on either claude build is answered.
`config.MeasuredReadableColumns` stays 40 — the new phrase is 24 characters
against the older one's 37, so the longest marker the floor derives from did
not move — and a test now derives that floor from the marker set instead of
restating it.

The daemon now opens its own log at `<state_dir>/dispatch.log` and appends to
it, instead of leaving the log to whatever the shell line that started it
redirected — a restart that dropped the redirect used to throw the log away
silently, and it was only ever noticed once the log was needed. Every line
still goes to stdout, so a foreground operator loses nothing; a daemon a door
started, whose stdout is already that file, gets the file alone rather than
each line twice. The new `-log` flag moves the file and defaults to that path.
A log that cannot be opened is reported on stdout and the daemon starts
anyway. `hdis doctor` gained a `log` field naming the file that was opened,
omitted when none could be.

`hdis status` now says when a worker's branch has gone behind the project. A
worker's branch is cut from the project's HEAD at spawn time, so a task that
lands first moves that HEAD on and the next branch can no longer be
fast-forwarded — a fact the operator used to meet at merge time. The CLI
prints the branch as `hdis/task-7 (behind)` and the JSON status gained a
`behind` boolean on every worker row. Behind is measured against the project's
current HEAD with `git merge-base --is-ancestor`, and only when `status` is
called, never on the tick. A git that cannot answer is an error rather than
the answer "behind": the row stays unmarked and the reason goes to the log,
because marking every worker behind would send the operator to rebase branches
that were never behind. Nothing here rebases, merges or serialises dispatch:
the recovery is still the operator's.

A worker now comes up in a TAB of its own rather than as a split of the
operator's pane, and the tab is created in the workspace of the pane the task
was FILED from. `herdr tab create --workspace/--cwd/--label/--env --no-focus`
replaces `herdr pane split --pane <base>` as the placement call. The operator's
tab is never split; a worker shares only the tab opened for ITS OWN task,
compared against the tab's whole label and not its `hdis ` prefix. Panes
inside a tab make a grid — the second splits right off the first, the third
DOWN off the first, the fourth down off the second — capped at
`layout.max_panes_per_tab`, and a full tab overflows into another tab in the
same workspace.

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

`NO_BASE_PANE` is unchanged as a refusal. `tab create --workspace` needs no
pane, so this release makes a paneless dispatch possible, but nothing here
takes it.

A restart can now read the row of a pane no binding names, which is the pane
the restart rule exists for. A pane names its task by number, and a by-ID read
is board-agnostic — so the board refused the number, by design, and every such
pane was logged as "left as it is". The number is now asked with the project it
is unique in: the repository the pane's checkout belongs to, which git names
through the common directory a worktree shares with the repository it was cut
from. A worker in its worktree and a pane opened before worktrees existed
resolve the same way. A pane whose checkout names no repository is left alone
and logged, rather than guessed at. The board's refusal of a bare number
across projects is untouched, and reads by ID still pass `--all-projects`.

**`hdis dispatch <number>` refuses differently when the board is not offering
that task.** The reply is now `NOT_READY`, a sub-reason under `CONFLICT`,
saying the number is not among the tasks the board is offering and to name the
task by ID to be told what it is. It used to be `UNAVAILABLE` quoting the
board's `USAGE` refusal of a bare number, which reads as a broken door rather
than as a task that is not on offer. Dispatch by ID is unchanged, and so is
dispatch of a number the board IS offering.

A worker now works in a git worktree of its own, on a branch named for its
task, rather than in the project directory. The branch is `hdis/task-<seq>`,
created at the project's current HEAD; the checkout is made under
`<state_dir>/worktrees` and removed when the binding that owns it is dropped,
which leaves the branch and every commit on it reachable. Until now every
worker this daemon opened edited, staged and committed in the tree the
operator sits in — and one task's commit swept up another task's uncommitted
work the first time two ran at once. When a checkout cannot be made, nothing
is spawned at all and the reason is logged.

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

The bindings are durable, and a restart reconciles what it left behind. They
are written to `<state_dir>/dispatch-bindings.json` on every change, whole and
atomically. At the next start the daemon asks one question of every live pane
it opened — what is this, and what still needs doing — answering it from
Herdr's agent list and the board rows rather than from the bindings, which
stay as a hint. A pane still working its task is adopted, a reservation no
live pane is working is dropped, a task that reached a terminal state or left
review takes its pane with it, and every `hdis-` checkout under this daemon's
own worktree root that no binding names is removed. Nothing outside that root,
and nothing in it this daemon did not create, is touched. A
prompted-but-unclaimed worker is no longer forgotten, and a restart no longer
dispatches a task into a second pane while the first is still alive.

The board principal is now `plugin:hdis@<pane>`, so a hold the board keeps
names the daemon that took it and a peer's hold is not in the answer to
`task list --mine`.

Callers do nothing. `doctor`'s JSON gains `bindings` (where they live) and
`readopted` (how many came back at the last start). The bindings document
itself gains a `reservations` array beside `bindings`, and each binding gains a
`worktree` field naming the checkout it owns — both omitted when empty, so a
document this binary writes stays readable to one that predates them. Anything
reading that file by hand should expect the two.

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
