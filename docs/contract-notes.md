# Contract notes

Where this plugin stands against the shared plugin contract, section by
section, and the test that fails when each MUST is removed.

The preamble is why this document exists: "A plugin that cannot name the test
that fails without a MUST does not conform to that MUST, however the code
reads." Before this sweep hdis carried five § citations across 33 test files
and roughly 350 tests. The tests were there; the mapping was not, so the
declared revision rested on nobody having checked.

## The vendored contract

[`docs/contract.md`](contract.md) is a **byte copy** of herdr-tasks'
`docs/contract.md` at revision 0.10.0, taken on 2026-08-24. herdr-tasks' copy
is the source of truth; this one is here so that a reader who has only this
repository can open every § the code cites, and so that the citations can be
checked against a document rather than against a memory of one.

That makes two things testable that were not before.
`TestContractCitationsResolve` reads every tracked file towards the contract
and fails on a § that resolves to nothing — before this, every citation in
the repository pointed at a document living somewhere else.
`TestTheDeclaredRevisionIsTheVendoredOne` reads `version.Contract` and the
document's own `Status:` line together: the declaration may lag the vendored
text, never lead it, and a lag only while a single entry in this document
names both revisions. They are equal today, so there is no gap to record.

Re-vendoring is a copy and nothing else. An amendment is written in
herdr-tasks' copy, cited there the way a test cites the § it enforces, and
arrives here byte for byte.

## Method

A MUST is NOT counted as pinned by reading its test. For each one the
behaviour was removed from the source, `make test-full` or the narrowest
package was run, and the pin counted only when a NAMED test went red. Every
mutation compiled — a mutation that does not build proves the compiler works
and nothing else. Each is recorded with the test that caught it.

Two mutations in this sweep survived and were the finding rather than the
control:

- `SocketMode` opened to 0666 left the socket test green, because the test
  compared the file's mode against the constant it had just moved. Re-aimed at
  the literal 0600, with the constant asserted separately.
- The self-review shot's own guard is the §11.4 entry below, and the defect it
  found was live rather than only unpinned.

## What does not apply, and why

These are recorded rather than omitted: an absence is only a decision once it
is written down.

| Section | Why it does not reach this plugin |
|---|---|
| §3.4, §3.7 | This plugin attributes nothing. It holds no ledger, records no actor, and has no verb whose authority is anyone's in particular. See the README section on where the contract is written for a plugin this one is not. |
| §7.5 | Same reason, and the contract has a gap here rather than this plugin: §7.5 rests its declaration on "`<name> doctor` (§10.3) already prints the calling principal", which §10.3 does not require and this door has no principal to print. |
| §5.2 | No SQLite store, so no schema and no migrations. §5.1's store choice is a recorded divergence in the README. |
| §6.6 | No review semantics. The board's review gate is htask's, and this binary stops at review. |
| §8.3 | No event hook. |
| §11.5 | Lease liveness and the sweep are htask's; a second writer racing them is the bug. |
| §11.6 | The manifest declares no `[[panes]]`. |
| §16.1 | Acceptance criteria are a board concept. |

## Where this plugin diverges, and why

A divergence is a rule that DOES reach this plugin and is not implemented. It
is written here rather than left to be discovered, and each carries the one
reason it stands.

| Section | The rule | Where this plugin stands |
|---|---|---|
| §5.8 | `<name> dump --json` prints the whole store | No `dump` verb. The whole store is one JSON document at `<state_dir>/dispatch-bindings.json`, already readable without this binary, which is the guarantee §5.8 exists for. A verb that only `cat`s it is on the list. |
| §8.1, §8.2 | Events for every state change, and an `events` verb | No events and no `_events` table. Every state change here is a binding moving, and a binding is derivable from the board plus Herdr the moment a worker claims; the durable trail of what was dispatched is the board's own. Adding an event stream means adding the store §5.5 shapes it around. |
| §11.1 | Reach Herdr through `HERDR_BIN_PATH`, or the socket at `HERDR_SOCKET_PATH` | The binary path is now read (`TestTheHerdrBinaryComesFromTheVariableTheContractNames`). `HERDR_SOCKET_PATH` is not, and is a non-divergence in substance: this binary shells out to the CLI and opens no socket of its own, so it hard-codes no socket path — the CLI resolves that variable itself. |

## The MUSTs that do apply

| Section | The MUST | The test that fails without it |
|---|---|---|
| §1.1 | No multiplexer, no PTY of its own | `TestNoSourceFileDrivesATerminalBesidesHerdr` |
| §1.2 | No agent registry of its own | `TestTheBindingsAreNoRegisterOfAgents` |
| §1.4 | No importing a sibling plugin's code or store | `TestEveryBoardCallGoesThroughTheOneScrubbedSpawn`, `TestNoSourceFileReachesIntoHerdrMail` |
| §2.2 | A CLI call with no live socket starts the daemon and waits | `TestACallWithNoDaemonStartsOneAndWaitsForIt` |
| §2.4 | The manifest declares startup, stop and restart | `TestTheManifestCarriesStartupStopAndRestart` |
| §3.5 | State dir 0700, socket 0600, boundary documented | `TestEnsureStateDirMakesItPrivate`, `TestTheSocketIsCreatedPrivateToTheUser` |
| §5.1 | `state_dir` never from `HERDR_PLUGIN_STATE_DIR` | `TestTheHerdrPluginDirsAreNotRead` |
| §6.1 | A parity test enumerating both surfaces, failing both ways | `TestTheServedToolListIsPinned`, `TestTheSchemaDeclaresExactlyWhatTheCLITakes` |
| §7.2 | The door's instructions say what a tool list cannot | `TestTheServerRegistersUnderTheRepositoryAndServesBareVerbs` |
| §7.3 | Every verb the CLI serves is on the door | `TestStopIsServedWithItsBlastRadiusStated`, `TestTheServedToolListIsPinned` |
| §10.1 | `config_dir` never from `HERDR_PLUGIN_CONFIG_DIR` | `TestTheHerdrPluginDirsAreNotRead` |
| §10.1 | Config is TOML at `<config_dir>/<name>.toml`, under the short name, with the `<NAME>_` prefix | `TestTheConfigIsTomlUnderTheShortName`, `TestWhatThisSubsetDoesNotCoverIsRefusedByLine` |
| §11.4 | One-line slash command in `agent start` argv | `TestTheTypedSpawnLineStaysUnderItsBudgetWithACodexProfile`, `TestThePromptedSelfReviewGoalFitsItsOwnBudget` |
| §11.4 | A successful `agent prompt` is not delivery | `TestASelfReviewShotHerdrAcceptedIsNotTreatedAsDelivered` |
| §9.1 | Every world-changing verb passes one `gate()` before doing anything | `TestAGateThatDeniesRefusesTheDispatchWithItsReason`, `TestAGateThatDeniesStopLeavesTheDaemonServing`, `TestEveryWritingVerbEitherPassesTheGateOrSaysWhyNot` |
| §9.2 | The gate is configured, not built in, and any failure to get a well-formed answer denies | `TestAnUnconfiguredGateLetsEveryVerbThrough`, `TestAGateThatCannotAnswerDeniesTheDispatch`, `TestEveryFailureIsDeny` |
| §9.3 | `defer` parks the action and returns DENIED with `parked_id`, and resolving re-runs the verb under the ORIGINAL subject, recording who resolved it | `TestADeferredDispatchIsParkedAndTheDeniedNamesTheRow`, `TestResolvingRunsTheVerbWithoutAskingTheGateAgain`, `TestAParkedActionResolvesOnlyOnce`, `TestAResolvedActionWhoseVerbFailedStaysInFrontOfTheOperator` |
| §9.4 | Gate verb names are `<short name>.<verb>`, and the README lists them | `TestTheGatedSetIsTheTwoWorldChangingVerbs`, `TestTheREADMEListsTheGatedVerbs` |
| §9.5 | The gate is where a verb is withheld, and no door withholds one | `TestTheServedToolListIsPinned`, `TestNoDocumentSaysAServedVerbIsWithheld` |
| §6.2 | With `--json`, a failure is one `{"error":{code,message}}` document on stdout | `TestAFailureWithJSONIsTheContractEnvelope`, `TestAFailureExitsWithTheStatusTheContractFixes` |
| §6.3 | The code is one of the nine, the exit status is the one fixed for it, and a finer name is a sub-reason inside `message` | `TestEverySubReasonAnswersUnderAContractCode`, `TestTheSubReasonsMapOntoTheCodesTheContractFixes`, `TestExitIsTheStatusTheContractFixes`, `TestAFailureExitsWithTheStatusTheContractFixes` |
| §10.3 | `doctor` prints the state dir and the config dir | `TestDoctorNamesTheDirectoriesItResolved` |
| §11.1 | Herdr is reached through `HERDR_BIN_PATH` | `TestTheHerdrBinaryComesFromTheVariableTheContractNames` |
| §11.2 | `herdr api schema --json` is read once, in both accepted document shapes, and a missing capability is UNSUPPORTED at the verb that needs it, naming it | `TestTheJSONSchemaShapeIsRead`, `TestTheFlatShapeIsAlsoRead`, `TestTheSchemaIsReadOnce`, `TestAMissingCapabilityIsUnsupportedAtTheVerbThatNeedsIt`, `TestEveryVerbThatNeedsACapabilityRefusesWithoutIt` |
| §10.3, §11.2 | `doctor` reports the Herdr schema it saw and every capability it is missing | `TestDoctorReportsTheHerdrSchemaItSaw` |
| §12.2 | A test cites the section it enforces | `TestEveryTestThisDocumentNamesExists`, `TestContractCitationsResolve` |
| §13.4 | The declared revision is the vendored one, and the README states it | `TestTheDeclaredRevisionIsTheVendoredOne`, `TestTheChangelogHasALineForTheDeclaredContractRevision` |
| §13.2 | The short name is `dispatch` | `TestTheManifestNamesThisPluginAtThisVersion` |

## What the sweep found

**§11.4, and it was live.** The self-review lane marked its shot spent the
moment `herdr agent prompt` returned, and only a task LEAVING review cleared
the mark. Herdr accepting the text says it was accepted and nothing more: an
agent TUI collapses a long paste, and a pane can exit between the call and the
agent's next turn. So a submission whose shot was accepted and seen by nobody
had its one check burned, with the board still green and nothing anywhere
saying so. The shot is now re-sent while Herdr still calls that worker idle,
bounded by the same `max_prompts` and claim timeout the unclaimed nudge uses.

**§3.5 was named and not enforced.** `TestEnsureStateDirMakesItPrivate` called
`EnsureStateDir` twice and asserted only that neither call errored. That is
idempotence, which is true and is not what the name claims. The socket's own
mode had no test at all.

**§5.1 and §10.1 were right and unpinned.** Nothing here has ever read
`HERDR_PLUGIN_STATE_DIR` or `HERDR_PLUGIN_CONFIG_DIR`, and nothing said so, so
the first reader to reach for them would have found no resistance.

**§6.3 defined seven codes of its own.** `INVALID`, `NOT_READY`,
`AT_CAPACITY`, `NO_BASE_PANE`, `ALREADY_DISPATCHED`, `ALREADY_RUNNING` and
`NOT_RUNNING` were top-level codes, which §6.3 forbids without a contract bump,
and every failure exited 1 whatever the code said. They are now sub-reasons
inside `message`, under the contract code each belongs to, and the exit status
is §6.3's own. Nothing a caller could read is gone: the sub-reason is the first
word of the sentence.
