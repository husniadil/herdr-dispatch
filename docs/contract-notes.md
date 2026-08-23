# Contract notes

Where this plugin stands against the shared plugin contract, section by
section, and the test that fails when each MUST is removed.

The preamble is why this document exists: "A plugin that cannot name the test
that fails without a MUST does not conform to that MUST, however the code
reads." Before this sweep hdis carried five § citations across 33 test files
and roughly 350 tests. The tests were there; the mapping was not, so the
declared revision rested on nobody having checked.

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
| §8.3, §9.3, §9.5 | No event hook and no policy gate. |
| §11.5 | Lease liveness and the sweep are htask's; a second writer racing them is the bug. |
| §11.6 | The manifest declares no `[[panes]]`. |
| §16.1 | Acceptance criteria are a board concept. |

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
| §11.4 | One-line slash command in `agent start` argv | `TestTheTypedSpawnLineStaysUnderItsBudgetWithACodexProfile`, `TestThePromptedSelfReviewGoalFitsItsOwnBudget` |
| §11.4 | A successful `agent prompt` is not delivery | `TestASelfReviewShotHerdrAcceptedIsNotTreatedAsDelivered` |
| §6.2 | With `--json`, a failure is one `{"error":{code,message}}` document on stdout | `TestAFailureWithJSONIsTheContractEnvelope`, `TestAFailureExitsWithTheStatusTheContractFixes` |
| §6.3 | The code is one of the nine, the exit status is the one fixed for it, and a finer name is a sub-reason inside `message` | `TestEverySubReasonAnswersUnderAContractCode`, `TestTheSubReasonsMapOntoTheCodesTheContractFixes`, `TestExitIsTheStatusTheContractFixes`, `TestAFailureExitsWithTheStatusTheContractFixes` |
| §12.2 | A test cites the section it enforces | `TestEveryTestThisDocumentNamesExists` |
| §13.4 | The short name is `dispatch` | `TestTheManifestNamesThisPluginAtThisVersion` |

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
