# The Herdr plugin repo standard

Three plugins are maintained here as one discipline: **herdr-dispatch**
(`hdis`), **herdr-tasks** (`htask`) and **herdr-mail** (`hmail`). They are
separate repositories with separate boards, and they are deliberately the same
shape. This document writes that shape down so a new verb, a new file, or a
fourth plugin has a template instead of a guess, and so a divergence is
recognisable as a divergence rather than mistaken for local taste.

Audited on 2026-08-24 across all three checkouts. Where the three disagree,
the rule below is what two of them already do, and the third carries a delta
filed on its own board.

## The short name governs everything nameable

Every plugin has three names, and only two of them are free:

| Name | hdis | htask | hmail |
| --- | --- | --- | --- |
| Plugin id (`herdr-plugin.toml`) | `herdr-dispatch` | `herdr-tasks` | `herdr-mail` |
| Binary (`cmd/<binary>`) | `hdis` | `htask` | `hmail` |
| **Short name** | `dispatch` | `tasks` | `mail` |

The short name is the third, and it is the one that names things on disk and
on the wire. It is a single exported `config.Name` constant, and everything
derived from it is derived, never spelled out a second time:

- Config dir: `${XDG_CONFIG_HOME:-~/.config}/<short name>/`
- Config file: `<config dir>/<short name>.toml` — **one config path per
  plugin, and no other.** A file under a different directory or a different
  extension is a leftover, not an alternative. An operator machine on
  2026-08-24 carried a stale `~/.config/hdis/hdis.json` beside the live
  `~/.config/dispatch/dispatch.toml`, and the daemon read the live one while
  the operator edited the dead one for an hour. Stating the one path is what
  makes the leftover recognisable.
- State dir: `${XDG_STATE_HOME:-~/.local/state}/<short name>/`, mode `0700`
- Socket, lock, log: `<state dir>/<short name>.{sock,lock,log}`
- Environment variables **the plugin itself reads**: `<SHORT NAME>_CONFIG_DIR`
  and `<SHORT NAME>_STATE_DIR`, each taking precedence over its XDG variable,
  and `<SHORT NAME>_E2E_*` for the layer-3 harness.

A variable the plugin **hands to something else** is prefixed by the binary
name instead, because the reader knows the binary and not the short name:
`HDIS_DISPATCHER_PANE` is read by a spawned worker agent, never by `hdis`.
A boundary test in `internal/spawn` pins that one against the README, since
the name is the whole of the contract with an agent that cannot guess it.
- Policy gate verb names: `<short name>.<verb>`

Config is **TOML**, parsed by a small hand-written `internal/config/toml.go`.
No config library; see the dependency budget below.

## Repository layout

Identical in all three, and additions go in one of these:

```
cmd/<binary>/        main, and nothing else
internal/            see below
docs/contract.md     the Herdr plugin contract this repo is written against
docs/contract-notes.md   where the contract and the observed daemon disagree
skills/<short name>/ SKILL.md, symlinked into ~/.claude/skills by hand
scripts/             start.sh, stop.sh, restart.sh (+ on-pane-gone.sh)
Makefile             the targets below
herdr-plugin.toml    the manifest Herdr installs
CHANGELOG.md  README.md  LICENSE  CLAUDE.md
```

`bin/` and `CLAUDE.local.md` are untracked working artefacts.

### internal packages

These names are the standard, and a package doing this job carries this name:

| Package | Job |
| --- | --- |
| `config` | `Name`, paths, the TOML parser, the config document |
| `verbs` | the one verb registry both doors are generated from |
| `protocol` | the request and response shapes on the socket |
| `codes` | the error codes both doors return |
| `client` | the thin socket client the CLI and MCP door share |
| `daemon` | the listener, the tick, the lock |
| `store` | the persistent state, where a plugin has one |
| `mcpdoor` | the MCP server built from `verbs` |
| `gate` | the policy gate call |
| `cli` | the CLI door built from `verbs` |
| `herdrclient` | the `herdr <verb>` adapter |
| `e2e` | layer 3, driving the shipped binary against a real sibling |

A plugin omits a package it has no job for. It does not rename one it has.

## The two doors, one registry

`internal/verbs` is the single list; the CLI subcommands and the MCP tools are
generated from it, and a parity test enumerates both surfaces against it. A
verb on one door and not the other is a test failure.

- `Verb.Name` is the socket verb and is **dotted** for a namespaced verb:
  `parked.list`, `task.claim`.
- `Verb.MCP` is the tool name: the verb alone with dots as underscores,
  `parked_list`. It is a field, not a transformation applied at the door,
  so an absence from the agent surface is a decision written beside the verb.
- `Verb.Mutates` marks a write; `Verb.Gated` is the `<short name>.<verb>`
  name handed to the policy gate; `Verb.Ungated` is required exactly when a
  verb mutates and is not gated, so an ungated write is never reached by
  omission.
- Every verb takes `--json` on the CLI.

**Verb parity across plugins.** These verbs exist in every plugin, spelled the
same way, because an operator learns them once:

`doctor` · `status` · `stop` · `dump` · `events` · `parked.list` ·
`parked.resolve`

`sweep` exists wherever the daemon has a reconciliation pass to run on demand.
A plugin that owns no reconciliation does not grow one to match: hdis has no
`sweep` because pane-gone sweeps and lease release are htask's alone, and a
second writer racing them is the bug rather than the safety net.

## Makefile targets

```
build          go build -o bin/<binary> ./cmd/<binary>
test           gofmt -l check, then go test -short ./...   (seconds; the loop)
test-full      test + go vet + cross-compile vet + go test -race ./...
e2e            go test -tags e2e ./internal/e2e/...        (needs a sibling checkout)
release-check  test-full + build + e2e in REQUIRED mode
install        go install ./cmd/<binary>
clean          rm -rf bin dist coverage.out
```

`test-full` is the gate: nothing is committed on a green `test` alone. `e2e`
is out of the gate on purpose, because it drives a real sibling binary CI does
not have, and it skips loudly naming what was missing rather than passing
quietly. `release-check` is the same suite with the skip turned into a
failure, via `<SHORT NAME>_E2E_REQUIRED=1`, and is what runs before a release tag.

## README shape

In this order, so the same question is answered in the same place in all
three:

1. What the plugin is, in a paragraph.
2. `## Install` — `herdr plugin install husniadil/<repo>`, what `[[build]]`
   and `[[startup]]` do, then the **skill symlink**, which the install does
   not place for you. Ask Herdr for `plugin_root` rather than writing the
   hashed path out. Then the develop-against-a-checkout variant with
   `herdr plugin link .`.
3. Running it: the daemon, the lock, the socket, the log.
4. The two doors.
5. Configuration, keyed exactly as the TOML document spells it.
6. Building and testing.
7. Dependencies, License.

## Dependency budget

Standard library, plus the official MCP go-sdk
(`github.com/modelcontextprotocol/go-sdk`), plus `spf13/cobra` for the CLI
door, plus `modernc.org/sqlite` where a plugin has a store. Anything else
earns its way in with the reason recorded in that repo's README. There is no
TOML library, no config library, and no logging library.

## Conventions

- Test-first for every behavior in a decision core: the test exists and fails
  before the code that makes it pass.
- No `panic` in a production path; errors carry what the operator needs to
  act.
- Lowercase conventional commits, no emojis, no co-author lines.
- Everything committed is English: tests, docs, comments, commit messages.
- Fail loud, idle safe. When a sibling is unreachable, say so and keep
  ticking; never guess at state, never queue writes for later.

## Known deltas, 2026-08-24

Each is filed on the board of the repo it belongs to. This list is the audit's
output, not a backlog kept in sync by hand.

**herdr-dispatch** — `internal/cli` is stdlib `flag` rather than cobra (task
59); no `events` verb (task 58); `internal/herdr` should be `herdrclient`, and
`parked_list`/`parked_resolve` should be dotted `Name` with an `MCP` field
(task 62). Fixed by this task: the gate config key, the `release-check`
target, and the README `## Install` section.

**herdr-tasks** — `HTASK_E2E_REQUIRED` was prefixed by the binary name where
`htask` is the only thing that reads it; it is `TASKS_E2E_REQUIRED` now, fixed
by this task. Nothing else open.

**herdr-mail** — no `internal/e2e` layer-3 suite, and so no `e2e` or
`release-check` target. Filed on its own board as herdr-mail task 8. Nothing
else open.

The environment prefixes were the audit's one false lead: all three already
build theirs from a `config.EnvPrefix` constant carrying the short name, so
`DISPATCH_`, `TASKS_` and `MAIL_` were never out of line. A grep for the
literal string finds nothing, which is not the same as finding nothing.
