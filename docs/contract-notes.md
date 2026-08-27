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
| §3.4, §3.7 | This plugin attributes nothing on the board. It holds no ledger and has no verb whose authority is anyone's in particular. See the README section on where the contract is written for a plugin this one is not. §7.5 was recorded here for the same reason until 2026-08-27, when it turned out to reach this plugin after all; the entry below says how. |
| §5.2 | No SQLite store, so no schema and no migrations. §5.1's store choice is a recorded divergence in the README. §5.5's sibling events TABLE is the same divergence: the trail is a bounded list in the one JSON document, written whole with what it is a trail of, which is what §5.5's "same transaction" buys. |
| §6.6 | No review semantics. The board's review gate is htask's, and this binary stops at review. |
| §11.5 | Lease liveness and the sweep are htask's; a second writer racing them is the bug. |
| §11.6 | The manifest declares no `[[panes]]`. |
| §4.4 | No table here holds user-visible entities. The store is bindings keyed by task id, and a task's project is the board's fact, read from the board. There is no column to add and no partition to default. |
| §16.1 | Acceptance criteria are a board concept. |

## Where this plugin diverges, and why

A divergence is a rule that DOES reach this plugin and is not implemented. It
is written here rather than left to be discovered, and each carries the one
reason it stands.

| Section | The rule | Where this plugin stands |
|---|---|---|
| §4.2 | Project defaults to the caller's working directory | It defaults to EVERY board. One daemon runs per user and drives the whole fleet, so defaulting to the directory the operator happens to be standing in would hide most of what `status` is driving and refuse most of the ready list to `dispatch`. `--project` narrows to one board and is resolved to §4.1's canonical path in the door; `--all-projects` is the explicit spelling of the default, and naming both is refused rather than ranked. `TestTheDefaultScopeIsEveryBoard`, `TestAnExplicitProjectIsResolvedBeforeItIsSent`, `TestNamingOneProjectAndEveryProjectIsRefused`, `TestDispatchScopedToAProjectOnlyOffersThatProjectsRows`, `TestDispatchScopedToAProjectDoesNotReachPastIt`, `TestTheProjectAJobNamesReachesTheLoop` |
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
| §3.2 | A principal is derived, never declared, and only `cron`, `trigger` and `plugin` may be declared with `--as` | `TestAsDeclaresOnlyThePrincipalsThatMayBeDeclared`, `TestWithoutAsThePrincipalIsStillTheProcessesOwnPane` |
| §5.1 | `state_dir` never from `HERDR_PLUGIN_STATE_DIR` | `TestTheHerdrPluginDirsAreNotRead` |
| §5.8 | `dump --json` prints the whole store, on both doors | `TestDumpPrintsTheWholeStore`, `TestDumpCarriesTheTrail`, `TestTheServedToolListIsPinned` |
| §5.5 | An append-only event trail written in the same write as the mutation | `TestTheTrailIsWrittenWithTheChangeItRecords`, `TestAnEventSurvivesARoundTrip` |
| §8.1 | An event for every state change, named `<name>.<entity>.<verb>` | `TestReservingATaskIsOnTheTrail`, `TestAWorkerComingUpIsOnTheTrail`, `TestAReservationGivenUpOnIsOnTheTrail` |
| §8.2 | `events [--follow] [--since] [--json]` streams the trail | `TestEventsAnswersTheTrail`, `TestEventsResumesAfterAnID`, `TestFollowingHandsOverAnEventWrittenAfterItOpened`, `TestAStreamEndsWithDoneWhenTheDaemonGoes` |
| §8.3 | One configurable `on_event` hook, detached, and a hook that fails does not fail the write | `TestTheHookRunsForEveryEventCarryingIt`, `TestAHookThatCannotRunDoesNotFailTheWrite`, `TestEveryEventReachesTheHook` |
| §6.1 | The three binaries present one flag shape: the CLI is cobra, every verb answers its own `--help`, and the four globals are on the root | `TestEveryVerbTakesTheFourContractGlobals`, `TestEveryVerbAnswersItsOwnHelp`, `TestTheBinaryOffersShellCompletion`, `TestAStrayArgumentToAGroupIsARefusalAndNotItsHelp` |
| §6.1 | A parity test enumerating both surfaces, failing both ways | `TestTheServedToolListIsPinned`, `TestTheSchemaDeclaresExactlyWhatTheCLITakes` |
| §4.2 | The scope flags reach every door | `TestEveryToolTakesTheScopeArguments`, `TestTheMCPDoorDefaultsToEveryBoard`, `TestAnExplicitProjectIsResolvedInTheMCPDoor`, `TestNamingOneBoardAndEveryBoardIsRefusedOnTheMCPDoor`, `TestTheScopeArgumentsAreHeldToTheirTypes`, `TestBothDoorsBuildTheSameRequest` |
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
| §6.3 | A parse failure of the CLI framework's own is USAGE, not the UNAVAILABLE an unnamed error would fall through to | `TestAnUnknownFlagIsAUsageRefusal`, `TestAStrayArgumentToAGroupIsARefusalAndNotItsHelp` |
| §10.3 | `doctor` prints the state dir and the config dir | `TestDoctorNamesTheDirectoriesItResolved` |
| §10.3 | `doctor` prints the calling principal, in both its shapes | `TestDoctorReportsTheCallingPrincipal`, `TestDoctorPrintsTheCallingPrincipal` |
| §3.6, §3.7 | A paneless CLI invocation is the operator, because its argv is the human act | `TestAPanelessCLIInvocationIsTheOperator` |
| §4.2 | The board `hdis mcp --project` names is the default scope of every call that names none | `TestTheMCPDoorIsStartedOnTheBoardTheProjectFlagNames`, `TestTheDoorsProjectFlagIsTheRootsOwn`, `TestTheDoorsProjectIsTheDefaultScopeOfEveryCall`, `TestADoorStartedOnNoBoardStillDefaultsToEveryBoard` |
| §11.1 | Herdr is reached through `HERDR_BIN_PATH` | `TestTheHerdrBinaryComesFromTheVariableTheContractNames` |
| §11.2 | `herdr api schema --json` is read once, in both accepted document shapes, and a missing capability is UNSUPPORTED at the verb that needs it, naming it | `TestTheJSONSchemaShapeIsRead`, `TestTheFlatShapeIsAlsoRead`, `TestTheSchemaIsReadOnce`, `TestAMissingCapabilityIsUnsupportedAtTheVerbThatNeedsIt`, `TestEveryVerbThatNeedsACapabilityRefusesWithoutIt` |
| §10.3, §11.2 | `doctor` reports the Herdr schema it saw and every capability it is missing | `TestDoctorReportsTheHerdrSchemaItSaw` |
| §12.1 | Layer 3 exists: the built binary against the real sibling plugin, outside the gate | `TestADispatchReservesARealBoardsTask`, `TestDispatchingATaskTheRealBoardDoesNotHaveIsNotFound` |
| §12.2 | A test cites the section it enforces | `TestEveryTestThisDocumentNamesExists`, `TestContractCitationsResolve` |
| §13.4 | The declared revision is the vendored one, and the README states it | `TestTheDeclaredRevisionIsTheVendoredOne`, `TestTheChangelogHasALineForTheDeclaredContractRevision` |
| §13.2 | The short name is `dispatch` | `TestTheManifestNamesThisPluginAtThisVersion` |
| §7.5 | `hdis mcp` accepts `--operator` and the declaration arrives by no other route: read once at start, refused as a tool argument, the pane resolved before it, and a declared door inside a pane refusing to start | `TestTheOperatorDeclarationIsAFlagOnTheMCPCommandAndNowhereElse`, `TestTheOperatorDeclarationNeverArrivesPerCall`, `TestServeRefusesADeclaredDoorInsideAPane`, `TestAnInPaneDeclaredDoorIsStillThePanesAgent`, `TestTheDeclaredDoorIsTheOperatorAndTheUndeclaredOneIsNot`, `TestTheCallerIsDerivedAndNeverMoreThanTheDaemonKnows`, `TestADeclaredPanelessDoorIsTheOperatorAtTheGateAndOnTheParkedRow`, `TestAnUndeclaredPanelessDoorIsNobodyAtTheGateAndOnTheParkedRow` |

## §7.5 — the operator declaration, and what "records no actor" was hiding

§7.5 is implemented as of 2026-08-27. It was recorded under "what does not
apply" until then, on the ground that this plugin "records no actor". That
sentence was true of one thing and read as if it were true of everything.

It is true of the trail. `loop.principal()` is the board principal this daemon
writes with, `plugin:hdis@<its own pane>`, and it is fixed for the process on
purpose — a principal that changed mid-run would leave rows written under one
name and released under another. No caller's identity reaches it, and that is
a design decision this entry does not touch.

It was never true of the §9 gate. `Request.Caller()` is the subject the policy
gate is asked about, the subject a deferred call is parked under, and the
resolver `ClaimParked` records. A door registered in a desktop MCP client
carries no `HERDR_PANE_ID`, so before this change every gate decision and every
`parked resolve` an operator made through such a door was filed under
`unknown` — the paneless case §7.5 exists to answer, reached through this
plugin's own gate rather than through a ledger it does not keep.

So `hdis mcp --operator` is the one route and there is no other: the flag is on
the `mcp` command alone, read once at start into `mcpdoor.Options`, sent on
every request as `Operator`, and `operator` is refused BY name as a tool
argument with `USAGE`. `Caller()` reads `--as`, then the pane, then the
declaration, so an agent that starts a declared door gains nothing by it, and
`Serve` refuses to start a declared door carrying `HERDR_PANE_ID` with
`FORBIDDEN`. Both halves of §7.5's fourth property are pinned, the ordering and
the startup refusal, because the half that exists to reassure a reader is the
half a reader stops checking.

The visibility §7.5 leans on is here too, as of 2026-08-27. §7.5 says
"`<name> doctor` (§10.3) already prints the calling principal" where §10.3
requires no such thing, and both siblings print it, so `hdis doctor` now
carries `principal` in its JSON and on its text line. It is `req.Caller()`
and nothing else, so what an operator reads there is exactly what the §9 gate
is told and what a parked row is filed under — three answers that cannot
disagree because there is one of them.

That line is also what made a second §3.6 gap visible: the CLI door sent no
`Operator`, so a paneless `hdis doctor` answered `unknown` where §3.6 makes a
CLI invocation `human` — its argv IS the deliberate act §3.7 asks for. The CLI
door now says so on every request, exactly as `hsched` does. The pane still
wins, and so does `--as`.

One divergence is left and is deliberate: a caller with none of the three is
`unknown` here where §3.7 spells it `none`. Nothing reads the string but a
human, the gate and the parked rows already written carry `unknown`, and
renaming it is a change to what a gate script matches on — so it is a separate
decision rather than a rider on this one.

## What the §8 sweep added

The §8.1/§8.2 divergence and the §8.3 exemption above are gone, and what
replaced them is narrower than "every state change" reads. The events here are
the ones this plugin OWNS: a reservation, a binding, a pane. A task claimed,
submitted or approved is a board fact, it is on htask's trail, and copying it
here would be the second ledger the boundary with htask forbids.

Two things about the shape are decisions rather than omissions. The trail is
bounded to `store.MaxEvents`, because the whole document is rewritten on every
change; a `--since` id that has rotated past that bound is refused rather than
answered with the window again, since a consumer handed the window would take
it for the tail of its own stream. And `--follow` is on the CLI alone: it is a
property of the connection rather than an argument of the verb, so the MCP
door publishes `events` without it (§8.2 with §7.1) and
`TestFollowIsOnTheRequestAndNotAnArgument` is what keeps it off the schema.

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
