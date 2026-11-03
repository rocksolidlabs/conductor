# Conductor Agent Harness — Architecture, Specification, and Implementation Plan

**Status:** Proposed implementation specification. No repository changes or test executions are claimed by this document.

**Source baseline:** [`phenixrizen/conductor`](https://github.com/phenixrizen/conductor) `main` at commit [`e8fc12f04c750c280e54e4bb4afc3d1a2a274464`](https://github.com/phenixrizen/conductor/tree/e8fc12f04c750c280e54e4bb4afc3d1a2a274464), inspected October 22, 2026. Recheck the branch head before implementation and account for subsequent commits.

**Repository destination:** `docs/plans/agent-harness.md`. The implementation may add separate ADRs and versioned schemas, but this file is the complete source-of-truth implementation plan.

**Architecture decision:** Extend Conductor's existing crew engine into a durable, cross-platform agent harness. Keep vendor coding agents running in real, shared PTYs. **This implementation uses native processes only** on macOS, Linux and Windows via WSL2. Boxcar is pre-alpha and is **not an implementation target**: no Boxcar integration, dependencies, commands, configuration, UI, or tests belong to this project. Preserve a narrow, provider-neutral execution boundary so a future isolated runtime could be integrated under a separate plan.

**Terminology:** Sections describing the current code are observations from the pinned snapshot. New types, API routes, schemas, default limits and workflows are **proposed requirements**, not implemented functionality. **MUST** is a release requirement; **SHOULD** permits a documented exception.

**Read first (2026-10-22):** the implementation follows `docs/plans/agent-harness-order.md`; the errata at the end of this document list where the code differs from the text below and which mechanisms are not built. The tracking issue is #334.

---

## 1. Intended outcome

A crew must become a coordinated unit of work, not merely several terminals launched with role prompts.

The harness must answer, without reading every terminal:

- What is each member assigned, which task attempt is active, and what exact inputs did it receive?
- What is genuinely finished, what has only stopped talking, and what has passed verification?
- Which tasks are blocked, what unblocks them, and who has authority to do that?
- Which messages were accepted, delivered to a terminal, or acknowledged by the agent?
- What survives a restart, and which operations have uncertain outcomes that must not be repeated blindly?

A representative workflow is: an owner approves a plan; the scheduler launches ready work; a worker submits an immutable result; configured checks run against that result; a reviewer receives that exact result; findings lead to a bounded revision cycle; the crew presents an evidence-backed outcome for the owner. An agent finishing a conversational turn is never sufficient evidence that its task succeeded.

The complete workflow MUST work on a Mac with native Claude Code or Codex, without Docker, a Linux VM, an optional isolation runtime, or a separate model API subscription introduced by this feature.

## 2. Existing architecture and integration constraints

### 2.1 User interface and transport foundations

Conductor already has a command palette, shared actions, inspector rail, editor layouts, Roundhouse, keyboard/navigation, and cross-session file navigation. These are the harness's presentation foundations. Managed task orchestration, authoritative results and durable handoffs are separate capabilities to implement. [S1–S2]

Build on these surfaces:

| Existing surface | Observed behavior in the pinned source | Harness integration |
|---|---|---|
| `web/app/utils/actions.ts` | Shared sidebar/palette/account/page action descriptors | Add task/run descriptors; backend authorization remains authoritative |
| `usePalette.ts`, `usePaletteRows.ts`, `CommandPalette.vue` | Existing groups/prefixes plus a cached cross-session Touched index, feed overlay and token-generation cache invalidation | Add managed task/result projections without duplicating search or weakening cache isolation |
| `pages/runs/[run].vue` | Grid, graph and timeline; shared `useAttention` updates | Extend these views with task and attempt projections |
| `useQuickReply.ts`, `transport/base.ts`, `sayWords.ts`, `sayDrafts.ts` | `say` uses `submitAcked(text, { unlessWaiting: true })`; acknowledgment-capable owners confirm entry or refusal; uncertainty remains explicit | Preserve behavior and add durable commands rather than another low-level acknowledgment path |
| `internal/proto/control.go`, `session.SubmitWord`, server/host dispatch | `SubmitAck` capability, request-correlated `submit_ok`/errors and the owner-side waiting guard | Treat this as the existing terminal-write receipt; add managed deduplication/reconciliation above it |
| `internal/api/touched.go`, `session.TouchedIndex`, `signal.HostedSession` | Admin-scoped aggregated file navigation with bounded results; hosted observations are not a full historical inventory | Reuse navigation; do not turn it into an artifact or audit authority |
| `utils/editorRows.ts`, `EditorArea.vue`, `EditorColumn.vue` | Shared editor snapshot/actions for save, close, keymap and asking about a selection | Use the existing editor permission and unsaved-change guards |
| Inspector rail, FileBrowser and `useRepoStatus` | Existing shared inspection/polling, breadcrumb handling and horizontal-overflow fixes | Add lazy evidence views without request fan-out, page overflow or hidden mobile controls |

The implementation must preserve the existing slow-relay file-request behavior, breadcrumb lifecycle, and inspector-width invariants. Automated browser tests do not substitute for real macOS/Windows keyboard and desktop validation. Source inspection of tests is not a claim that they were run for this specification. [S1, S3–S4, S12, S20–S25]

### 2.2 Existing crew behavior

`internal/crew/crew.go` has saved members with prompts, arguments and `immediately`, `after`, or `manual` starts. An `after` condition names one member and follows its first relevant done signal. The current member limit is 12. The schema accepts server/host locations, but `Crew.Launchable()` rejects host crews because the server cannot launch their sessions. [S5]

`internal/crew/run.go` contains the actual engine and existing `Launcher` boundary. `Launcher.Launch` returns `*session.Local`; `internal/api/runs.go` uses `createLocalSession` and the existing catalog, environment, worktree and hook paths. The engine distinguishes process/member state and attention, and a done agent can remain alive. Preserve those distinctions. [S6, S11]

`internal/crew/handoff.go` submits handoff text through `session.Local.Submit`. It queues up to ten handoffs per member, drops the oldest beyond that bound with a log entry, and does not accept a handoff to a member that has no running session. Managed-task delivery needs different durability and backpressure semantics. [S7]

`internal/crew/record.go` already journals runs and reconciles interrupted runs and continuation references after restart. Do not describe persistence as absent or replace its legacy behavior without migration. Its bounded historical records are not a durable task queue or an authoritative result ledger. [S8]

### 2.3 Existing safety mechanisms to preserve

`internal/session/input.go` serializes writes and handles stalled input and credential revocation. `submit.go` distinguishes typed text from Enter, handles partial/stranded submissions, and already binds answers to question IDs with claims. Do not implement a competing input writer or a second first-answer-wins mechanism. [S9]

`internal/agents/adapter.go` already supports injection, skills, probes, MCP registration and submission-confirmation capabilities. Extend this adapter layer. `internal/cli/mcp.go` already exposes reporting, state, questions and crew operations. Extend those tools without making conversational `done` mean verified task success. [S10]

The repository contract requires argv arrays, bounded messages, existing `resolveCwd`/`openCwd` and rooted file access, coordinated Go/TypeScript/protocol documentation changes, dependency justification, and affected-surface tests. It says one issue/branch/PR, CI is the gate, and the owner merges. Harness verification must not silently create new mandatory GitHub merge gates or auto-merge authority. See the repository contract, dependency guide and check matrix. [S13–S16]

### 2.4 Existing terminal acknowledgments are not a durable command ledger

`Submit` carries optional `reqId` and `unlessWaiting`; `SubmitOK` responds with `requestId`. `welcome.submitAck` negotiates support. Both server and developer-host dispatch use the session outcome mapping, with tests for acknowledged entry, guard refusal, legacy unacknowledged submission and bounds. The wire's text limit remains 4,096 bytes, and request IDs remain at most 32 bytes of permitted printable ASCII. These are existing contracts to preserve. [S20]

`BaseTransport.submitAcked` currently generates a connection-local request counter and settles a promise on an acknowledgment/error or timeout/closure. With an older owner it sends a plain legacy submit and resolves `false`. This is useful interactive compatibility; it is not persistent deduplication, cross-reconnect recovery, or agent comprehension. Managed automation MUST reject an insufficient peer before sending, not call the legacy fallback and label the action safe. [S4, S20]

The current UI already distinguishes confirmed entry from unconfirmed outcomes. It retains a draft for definite refusal, not for a partly written/uncertain command. Preserve `sayOutcomeOf`/`sayWords` semantics rather than adding generic automatic retries. [S21]

### 2.5 Existing navigation is not result evidence

`GET /api/touched` aggregates the registry's listed sessions and includes their statuses; do not assume it is only active sessions or a complete repository inventory. It defaults to 500 entries per session, permits up to 2,000, and uses an approximately 2 MiB response budget with truncation flags. A hosted index reflects reported events since connection; it is not a complete replay of the host's own index. The palette caps merged rows at 2,000 and caches/overlays observations. [S22–S23]

A Touched row means an observed/reported file interaction is available for navigation. It does not establish file contents, complete provenance, an immutable candidate, or a passed check. Managed artifact identities and acceptance stay in the new ledger. Reuse the existing route/cache instead of rebuilding cross-session indexing as part of this feature.

## 3. Scope and non-goals

### 3.1 Full harness scope

The implementation comprises versioned harness definitions; task/attempt scheduling; explicit result submission; durable context, messages and checkpoints; artifact handoffs; verification; collaborative decisions; recovery; limits and observability; existing-UI integration; and an optional supervisor. **All execution and verification run as native processes under the existing Conductor owner.**

A coding harness is the first package, but the core must support tasks whose outputs are documents or other artifacts rather than Git commits. Git-specific acceptance is a policy of a coding harness, not the universal task model.

For a package with no mechanical verifier, require an explicit configured review or owner acceptance and label that evidence honestly. Human acceptance and automated test verification are different evidence types; neither should be invented to make an unsupported workflow appear verified.

### 3.2 Non-goals

Do not build a replacement model/tool loop, require an agent SDK or app-server transport, replace PTYs with synthetic chat, introduce a second crew scheduler, implement Boxcar or any other VM/container execution backend, require a cloud control plane, or build a public package marketplace for this release. There are **no runtime-isolation backend deliverables** in scope.

Do not introduce CRDT/consensus infrastructure as a prerequisite. One owner process is authoritative for a run. Browser layouts can remain browser-local. Cross-host live migration of an agent conversation, transparent failover to another machine, and individual members of one run spread across multiple owners are outside the first version.

## 4. Architecture and ownership

```text
Existing Nuxt/Electron/browser interfaces
  Palette | run grid/graph/timeline | inspector | shared terminal
                         |
           Existing HTTP, event and terminal transports
                         |
                 Conductor owning backend
     ----------------------------------------------------
     crew.Engine: run/task scheduling and state transitions
     harness definitions: recipes, requirements and checks
     durable store: attempts, commands, messages and evidence
     agents adapters: native CLI integration and observation
     session.Local: terminal, questions, users and input guards
     ----------------------------------------------------
                         |
           Narrow native execution contract
                         |
       Existing Conductor PTY/process lifecycle
                         |
                macOS / Linux / WSL2
```

The engine MUST remain the single scheduling authority. `internal/harness` holds definitions and compilation into launch/task context; it must not grow an independent orchestrator that can start the same work separately.

`session.Local` remains the owner of session interaction semantics. Use its current process interface and launch path as the default native implementation; do not rewrite every `*session.Local` caller just to invent a generic runtime registry.

The client is a projection, not an authority. `Action.run` is a UI affordance, not an authorization decision. Every mutating action is validated by the owning backend against current state, permissions, session incarnation and applicable policy.

The switchyard remains connectivity infrastructure. It does not schedule tasks, adjudicate results, resolve answer races, or approve policy changes.

## 5. Host and execution capabilities

Native execution is the **only supported execution mode in this plan**:

| Execution host | Implemented execution model | Expected behavior |
|---|---|---|
| macOS | Existing local Conductor PTY/process lifecycle | Complete managed harness with installed native agent CLIs |
| Linux | Existing local Conductor PTY/process lifecycle | Same managed harness, no virtualization prerequisites |
| Windows desktop with WSL2 | Existing Windows/WSL2 Conductor execution path | Same managed harness inside the supported environment |
| Linux owner accessed from a Mac | Native execution on the Linux owner | Client and execution owner stay distinct; optional remote-owner support is a separate work item |

Host capability reporting SHOULD identify OS/architecture, supported native agent commands, PTY attachment, workspace access, check execution, session resume affordances, and whether supported hooks/MCP are available. Agent capabilities remain separate: task context, result reporting, question answering, transcript observation and provider-session resume. No VM-specific flags, capabilities, providers or policy modes are needed now.

Use a **small, internal, capability-aware execution seam** rather than an expansive plugin architecture. The seam should permit the coordinator to request `Launch`, `Inspect`, `Stop` and compatible process/terminal handles, with explicit execution IDs and failure semantics. The existing native implementation owns the only actual behavior. Every task still uses the same native Conductor execution path, and all verification commands are native. The seam MUST NOT create dormant alternative backends, dynamic loading, provider registries or hypothetical VM APIs.

Native processes run with the account's ordinary permissions. A worktree, skill scope and MCP tool list are **not** operating-system isolation. Show that limitation in the UI and documentation. If a future user policy requires OS-level sandboxing that the current product cannot provide, refuse that protected mode rather than mislabel a native run. Such a policy is not required to implement this plan.

A later isolated execution provider should be able to plug in below this boundary without changing the meaning of tasks, attempts, verified results, message delivery, human approvals or durable recovery. That is a **design invariant**, not authorization to add isolation code now.

## 6. Domain model

Preserve the existing distinction between a saved crew and one run. Add the following concepts:

| Entity | Purpose and required relationships |
|---|---|
| `HarnessDefinition` | Versioned instructions, skills, tool requirements, workspace policy and completion/check policy |
| `ResolvedHarness` | Immutable content digest and resolved configuration captured for the run |
| `CrewMember` | Existing named role and agent selection; optionally references a harness; at most one active assignment by default |
| `Task` | A specific assignment, dependencies, member selection, required inputs/outputs and acceptance policy |
| `Attempt` | One try at a task, with execution, session incarnation, input bindings and outcome |
| `Execution` | Native Conductor process/PTY instance, separately identified from task success; future execution modes must preserve this separation |
| `Artifact` | Immutable content identity, producer attempt, provenance and permitted access |
| `Verification` | Check definition, exact subject, execution environment, exit/result, logs and timing |
| `Message` | Durable communication with sender, destination, correlation and delivery state |
| `Question` | Existing session question extended with task/attempt association and durable decision history |
| `Command` | Idempotent requested action with authorization, expected revision and execution receipt |
| `Checkpoint` | Explicit handoff/resume context tied to source and completed evidence |

A run can link a repository, issue, canonical development branch and PR as its external work item. Internal tasks are not automatically separate GitHub issues. One designated owner/member holds the external issue claim; reviewers and supporting members receive internal assignments and separate temporary/detached workspaces, not competing claims on the same issue branch. Integrating their results into the canonical branch is serialized and authorized. The harness must not silently create competing PRs or instruct every member to independently claim the same issue.

Every managed operation must carry the applicable `runId`, `taskId`, `attemptId`, `sessionId`, `sessionIncarnation`, `commandId` and owner epoch. Infer these identities from authenticated launch/session context; do not trust an agent-provided run ID to authorize access.

Do not equate the existing transport `reqId` with `commandId`. A `reqId` correlates one connection's response; the durable command identity survives reconnects and is scoped to the authorized aggregate/actor. Map the two explicitly and preserve the existing request-ID size limit. A reconnect's reused `s1` must never select or execute another connection's command.

Use an execution identity stronger than a PID. PID reuse, restarts and recycled session names must not attach a new process to an old attempt.

An agent account/profile reference can be captured where supported by the existing catalog. Credentials are resolved by the execution owner and must not be copied into manifests, task context, receipts or shared views.

## 7. State and completion semantics

### 7.1 Separate activity from outcome

There are four distinct dimensions: execution lifecycle, conversational activity, task outcome, and human attention. A process can be alive while its task has succeeded. A process can exit successfully while its task has not. A turn can end while a question remains unanswered.

The managed task lifecycle is:

```text
queued -> blocked / ready -> running -> verifying -> succeeded
                              |            |
                              +-> failed <-+
                              +-> cancelled
                              +-> uncertain
```

`blocked` carries structured reasons such as unmet dependencies, missing capability, required decision, or unavailable input. `ready` can wait for a concurrency slot. A question on a running task is an attention/blocker record; it must not accidentally consume a retry or fail the task.

Attempts additionally record reservation/launch stages and the transition from active work to a submitted candidate. The task state is a transactionally maintained projection of those records, not a second independent truth.

### 7.2 Completion protocol

A worker submits a candidate result with its attempt identity, a summary, source/artifact references and unresolved issues. The controller validates the identity, snapshots the result, runs required checks, and applies the acceptance policy. Only the controller transitions the task to `succeeded`.

A hook's `done`, a terminal becoming quiet, an exit code, a Markdown checklist, or a worker-written `ready: true` file MUST NOT independently unlock managed dependencies.

A valid review can successfully produce a `needs_changes` verdict. That means the review task executed successfully, not that the implementation is approved. Acceptance gates evaluate the review verdict separately. This avoids conflating a discovered defect with reviewer infrastructure failure.

The overall run can complete with idle terminal sessions still available for inspection. Stopping terminals is an explicit retention/lifecycle action, not the definition of completion.

### 7.3 Revisions and stale results

Inputs bind to exact producer task/attempt/result identities, never “latest.” A late result from a superseded attempt is retained as diagnostic history and rejected as current output.

Once a result has been consumed downstream, do not silently replace it. A revision operation creates a new plan/result revision and identifies the downstream tasks that must be rerun. Historical successes and reviews remain intact, but cannot satisfy acceptance for a different source snapshot.

Repair loops are bounded new revisions/attempts, not dependency cycles. A failed check does not cause unrestricted repeated prompting.

## 8. Scheduler behavior

Extend `crew.Engine` rather than adding a parallel engine. The scheduler validates an entire plan before dispatch: unique IDs, known members and dependencies, acyclicity, compatible requirements, valid input bindings, allowed check definitions and resource limits.

A dispatch transaction reserves the attempt, binds input versions, consumes the required capacity, and records a launch command before any external launch occurs. One task has at most one active attempt; one member has at most one active managed assignment unless a future capability explicitly changes that rule.

Scheduling is event driven. Relevant events include an accepted result, a completed check, a resolved question, a confirmed execution exit, or an authorized plan change. A timer can enforce an explicit resource limit, but cannot invent completion or answer a question.

Automatic launch retry is permitted only when the previous attempt is known not to have produced a live execution or external side effect. Unknown launch outcomes require reconciliation. Tests failing are not evidence that a fresh duplicate worker should be launched.

A managed worker should normally receive an attempt-specific workspace and a fresh session. Conversation reuse is allowed only when the adapter supports it, workspace and task context are compatible, and the previous assignment is conclusively released. Cross-provider conversation portability is not assumed.

Pausing a run prevents new dispatch and automatic deliveries. It does not promise to suspend every currently executing process. Stopping a process, cancelling a task, pausing scheduling, and taking human control must be distinct actions with explicit consequences.

## 9. Harness packages and context

### 9.1 Package format

Use JSON for the first manifest format, matching the repository's existing configuration conventions. Do not add YAML solely for this feature. A package contains `harness.json`, instructions, optional skills, optional templates, and named checks. Package installation is not implicit permission to execute setup scripts.

Example of the proposed format:

```json
{
  "schemaVersion": 1,
  "id": "go-feature",
  "version": "1.0.0",
  "instructions": ["instructions/worker.md"],
  "skills": ["skills/work-task"],
  "requirements": {
    "operatingSystems": ["darwin", "linux"],
    "commands": ["git", "go"],
    "agentCapabilities": ["task-context", "task-result"]
  },
  "workspace": {
    "mode": "worktree",
    "scope": "attempt"
  },
  "completion": {
    "mode": "explicit-result",
    "checks": [
      {
        "id": "unit-tests",
        "argv": ["go", "test", "./..."],
        "timeoutSeconds": 600
      }
    ]
  }
}
```

This schema is a proposal and is not supported by the current Conductor implementation. The agent is selected through the existing crew/catalog; the harness defines its job and requirements. The first manifest version deliberately **does not define an execution backend selector**. A later, separately reviewed schema revision may introduce one; unknown execution fields must not be silently interpreted as native mode.

Resolve packages to immutable content digests. Record the resolved instructions, check definitions, approved setup, source ref and version in each run. Editing an installed package does not retroactively change an active run. Development-link mode must be explicit and still snapshot the effective content for each run.

Commands are argv arrays. Any substitution uses an allowlisted typed variable as one argument, never shell evaluation. Templates and package paths are checked for traversal, symlink escape and collisions before materialization. Do not overwrite project `AGENTS.md`, user settings or provider credentials as a side effect of opening a crew.

### 9.2 Context contract

Produce a context bundle containing the goal, task and acceptance criteria, role, exact inputs, workspace/base identity, permitted tools and actions, task/attempt IDs, relevant decisions, prior checkpoint and a concise current-state summary.

The initial terminal prompt should be a short instruction to read this bundle or call `task_context`. Do not inject a huge prompt, bypass `MaxSubmitText`, or rely on embedded newlines surviving the existing submission normalization.

Repository instructions, owner instructions and package instructions retain their provenance. File contents, worker messages and artifacts are data, not new authority. Delimiting them reduces ambiguity but is not a security guarantee against prompt injection.

Context rebuilding after restart or conversation compaction must preserve the original requirements and source bindings. Summaries do not replace original decisions or verification receipts. Checkpoints include what is complete, what remains, exact source, unresolved decisions and safe resume instructions.

## 10. Durable commands, messages and terminal delivery

### 10.1 Command envelope

Every new managed mutation has a `commandId`, idempotency key, expected aggregate revision, target identity/incarnation, actor and typed intent. A repeated key with identical content returns the original receipt; reuse with different content is a conflict. On retry, resolve a matching prior command before rejecting it merely because its own successful mutation advanced the aggregate revision. Authorization must still be checked before disclosing or acting on that receipt.

Persist a canonical request fingerprint and scope idempotency to the run and authorized principal. Record any transport request correlation separately. A transient `submit_ok` is an observation to apply to the command record, not a replacement for that record.

Required intents include `task.dispatch`, `task.submit_result`, `message.deliver`, `session.say`, `question.answer`, `task.retry`, `run.pause`, `run.resume`, `run.cancel`, and `verification.request`. Their authorization and validation live on the owner, not in the palette.

### 10.2 Delivery vocabulary

Persist at least:

```text
accepted -> queued -> dispatching -> written -> acknowledged
                \-> cancelled / rejected
                       \-> partial / uncertain
```

`written` means the required terminal writes completed. Map the current owner's `submit_ok` to this level only: it confirms the line and Enter were written, not that the model read or acted on the instruction. `acknowledged` requires a supported hook/tool acknowledgment correlated with the managed delivery; a generic “working” observation is insufficient when several causes are possible. Adapters lacking that capability report it as unavailable, not as success.

Persist negative outcomes with their actual certainty. Existing definite-refusal codes can record a non-executed dispatch; `input_failed`, loss of the connection, or absence of a response can mean partial or uncertain delivery. Do not interpret acknowledgment timeout as cancellation. Preserve the existing `ErrSubmitPending`/`Finished` path: a pending write gets no premature success, and its eventual observation is recorded once. [S20–S21]

Once dispatch has begun, a crash may leave the outcome uncertain. Do not promise exactly-once execution through an arbitrary PTY. Use idempotency and deduplication where supported, and visible uncertainty where they are not.

### 10.3 Reuse the existing input path

All terminal submissions continue through `session.Local.Submit` and its writer. Preserve bracketed paste, separate Enter, question claims, revocation checks and stranded-text behavior. A new task-level outbox coordinates requests; it does not write directly to the PTY.

For managed automation, remove the assumption that elapsed startup time proves readiness. Do not send a task prompt through a startup/trust dialog after a deadline. Unknown readiness becomes a visible blocker. Preserve legacy launch behavior for legacy crews until explicitly migrated.

The owner-side non-answering guard and transient positive receipt exist for `submitAck`-capable owners. Reuse `unlessWaiting`, `SubmitWord` and `submit_ok`; the browser waiting check remains early feedback, not the authority. The managed harness must associate dispatch with durable identity, expected revision, target incarnation and current authorization, then persist the observed outcome. [S4, S20]

The 400 ms fallback in `useQuickReply.say` applies to owners without submit acknowledgments and returns an unconfirmed/legacy-sent outcome, never a confirmed write. Preserve that interactive distinction, but add an explicit managed-send entry point that refuses unsupported peers **before** any bytes are sent. Do not silently change or reuse `submitAcked`'s legacy fallback for scheduler-generated tasks or handoffs. No arbitrary delay substitutes for an acknowledgment or ledger receipt. [S4, S20–S21]

Human takeover pauses automated submissions for that session and cancels queued commands that have not begun. If text was partly written, expose the existing held/stranded condition and require reconciliation; do not try to “clean up” the terminal blindly.

### 10.4 Durable inboxes

Messages may address a task/member that has not started yet. They remain queued until the target is eligible. Distinguish informational messages, artifact handoffs, corrections and cancellation requests. Include sender, scope, input/result references, correlation ID and sequence.

Managed inboxes use explicit backpressure. At capacity, reject new messages with an actionable error; do not drop an older work item. Poll/read/ack tools use cursors so a resumed worker can reconstruct what it has and has not processed.

The `report(event="handoff")` operation can produce a compatibility message in a managed run, but it cannot certify task success or rewrite dependency inputs.

### 10.5 Incremental managed-command compatibility

Keep the existing wire contract usable for ordinary sessions. Add a separately negotiated managed-command capability/version for the stronger identity, deduplication and receipt-lookup contract; `submitAck: true` alone does not advertise those guarantees. Document how it composes with existing `submit`/`answer` frames, and update both server and developer-host paths in the same change.

A local owner calling `Local.Submit` directly must use the same persistent command coordinator; it does not bypass idempotency merely because no WebSocket is involved. A remote caller first checks the managed capability, asks for a prior receipt after a reconnect, and dispatches only when the owner says it is safe. Missing capability yields `incompatible_capability`, not legacy plaintext submission.

Retain a status-read operation for any accepted command. A client can inspect an uncertain outcome without initiating another side effect. There is no exactly-once claim for arbitrary terminal input: even a transactional ledger cannot atomically commit a database row and an external PTY write.

## 11. Persistence, recovery and retention

### 11.1 Storage decision

**Recommended ADR:** add a local SQLite store for managed runs, while retaining existing JSON crew definitions and legacy records. The justification is atomic updates spanning task state, attempt reservation, command receipts, outbox entries and event sequence—not a generic preference for a database.

Select and pin a driver only after checking the repository's supported build/platform matrix and dependency rules. Prefer an implementation that preserves the existing cross-platform build story; do not introduce a required native database service. Record dependency size, build implications, license and vulnerability review, and regenerate notices. No driver/version is assumed by this document. [S13, S15]

If the implementation chooses a file-based alternative, its ADR must demonstrate equivalent atomicity, crash consistency and outbox tests. Do not implement loosely coordinated JSON files that can claim a result was committed while its dependency transitions were not.

The chosen SQLite configuration must document journaling/synchronization, backup and recovery behavior on the supported filesystems and pass failure injection. SQLite's atomic-commit documentation concerns database changes and storage assumptions; it does not make terminal/process/network side effects atomic with those changes. The outbox and uncertain-outcome rules therefore remain necessary. [S26]

### 11.2 Transaction and file rules

Use one owner process and a state-directory ownership lock. Commit state change, event and outbox intent together. Publish events after commit. An outbox worker rechecks actor authorization and target generation immediately before a side effect.

Large artifacts live in a controller-managed content store. Materialize into staging, hash and finalize before committing references; interrupted staging is garbage collected. Never mark a task succeeded with missing referenced content.

Do not persist credentials, workbench tokens or raw terminal streams as part of the task ledger. Store the minimum required prompt/context/result data with owner-only permissions and explicit retention. Same-user local agents are not isolated from the owner by file modes alone; the threat model must say so.

### 11.3 Recovery procedure

On startup, acquire ownership, validate schema, load active managed runs, and mark transient launch/delivery operations for reconciliation. Inspect native executions using strong process/session incarnation identities through the existing owner. A PID alone is insufficient.

If a native session cannot be recovered, preserve its task, context and workspace and mark the attempt interrupted/uncertain as appropriate. Do not pretend the old PTY exists. A new attempt or provider-conversation resume is a distinct recorded operation.

For native executions whose original PTY/process cannot be recovered, retain the attempt and require an explicit, idempotently recorded recovery decision. If optional remote-owner access is implemented, an unavailable remote host becomes unreachable, not failed or successfully completed. Never automatically create a replacement owner on a second machine.

An unanswered question remains unanswered. A command whose effect is unknown remains uncertain. No mutation occurs merely because a client reads a status endpoint.

### 11.4 Retention

Never apply the legacy historical-run pruning limit to active managed work, unacknowledged messages, unresolved questions or evidence still referenced by a run. Retention applies only after references and legal/user retention settings permit it. Garbage collection is interruptible and auditable.

Storage exhaustion blocks new managed transitions that cannot be durably recorded. The UI must show the problem; the engine must not continue launching work while claiming durable recovery.

## 12. Workspaces, artifacts and verification

### 12.1 Source isolation

Default managed coding tasks to attempt-specific worktrees or equivalent isolated source copies. Record base commit, result commit/tree or complete dirty snapshot, and the exact repository identity. Do not run verification against the user's mutable primary checkout.

A worktree isolates ordinary working-tree edits, not the OS or shared Git metadata. Serialize Conductor-managed branch/index/ref-changing operations as necessary, detect unexpected external repository changes, and never claim that a read-only reviewer prompt enforces read-only host access.

### 12.2 Handoffs that carry actual work

Starting the reviewer on a fresh worktree from the original base does not make it see the builder's changes. Materialize the builder's exact result into the review workspace, or provide a read-only artifact representation appropriate to the task. Validate its content identity before dispatch.

Artifacts have content digest, size, media/type, producer attempt, source identity, path scope and sensitivity. File access uses the existing rooted access rules. Reject escaping paths and unsafe symlinks. Do not silently include ignored secrets or an entire home directory when snapshotting untracked files.

For Git work, a committed tree is preferred. Dirty work can be supported through an explicit snapshot that includes approved tracked/untracked content and modes. Rehash or otherwise validate the stable subject before and after capture; concurrent mutation cannot yield a falsely coherent receipt.

### 12.3 Verification

The controller executes named checks from the approved resolved harness against an immutable/materialized candidate. It records command argv, check version, subject digest, environment/tool identities, times, exit status and bounded logs. A worker's statement that tests passed remains a claim, not this receipt.

Check definitions and scripts can themselves execute untrusted repository code. Native verification requires the same explicit workspace trust as native agent execution and must not claim sandboxing. If a task explicitly requires isolated verification, mark the requirement unsupported and block that task rather than silently running its checks without isolation. Do not implement an isolation backend as part of this feature.

Reviews and checks must refer to the exact candidate. A new commit, changed dirty snapshot, changed acceptance policy or incompatible environment invalidates applicability to the current candidate without erasing history.

A coding task's completion policy is separate from GitHub's merge policy. CI remains authoritative where the repository contract says it is, and the owner retains merging authority. Local evidence is not a substitute for a required CI result. [S13]

## 13. Human decisions and permission boundaries

Keep existing question IDs and session claim logic. Persist the association with task/attempt and decision history; extend identity with session incarnation where necessary. Do not create a competing race-resolution protocol in the frontend.

Accept only the first authorized answer to the currently applicable question. A stale answer, a revoked link or an answer intended for an old incarnation is rejected. Confirming receipt is distinct from confirming terminal delivery and from knowing the agent used the answer.

Questions have no automatic answer timeout. They remain open until answered, explicitly withdrawn, cancelled with their task, or superseded by a visible new question. Time spent waiting on a person must not consume a repair retry.

Bulk answer is a convenience, not one global transaction. Each target question is independently identified, authorized and revalidated; report individual outcomes. Similar question text does not establish equivalent permission scope. Security-sensitive approval prompts should require per-target confirmation unless the policy explicitly allows a narrowly scoped batch.

An agent can report its own work, submit its own candidate, read authorized inputs and ask for help. It cannot approve its own privilege escalation, change approved execution requirements, bypass checks, mutate another attempt, forge controller evidence or grant broader credentials.

Preserve current view/control link semantics. Do not silently grant shared controllers run-administration, package-installation or execution-policy authority merely because they can type into a terminal. Add explicit capabilities where needed.

## 14. Agent integration and MCP/CLI surface

Continue using `internal/agents` and `internal/notify` for vendor-specific launch and observation behavior. Do not require an app-server mode for an agent currently run in a shared TUI.

Preserve the existing MCP tools. Add scoped, versioned operations along these lines:

| Proposed operation | Meaning |
|---|---|
| `task_context` | Read the assigned task, inputs, policy and checkpoint, with bounded pagination |
| `task_result` | Submit a candidate for the current attempt; never directly sets verified success |
| `task_blocked` | Report a blocker with structured reason; owner validates any state effect |
| `task_checkpoint` | Save bounded resume/handoff context tied to the current source |
| `message_send` | Queue a scoped message with an idempotency key |
| `inbox_read` / `inbox_ack` | Read messages after a cursor and acknowledge processing |
| `plan_propose` | Submit a candidate plan/revision for validation and authorization |

Use the existing `ask`, `report`, `run_status`, `form_crew` and `add_member` where applicable. In managed mode, dynamic crew changes must obey run budgets and plan authorization. Do not let existing self-service routes bypass new limits.

Equivalent CLI operations call the same owner-side business logic; `internal/cli` must not contain a parallel scheduler. Context identifiers in environment variables are convenience metadata, not authorization.

Optional transcript normalizers may fill gaps in hook coverage. They must be opt-in/capability-aware, limit reads to the bound provider session, track offsets and rotation, deduplicate replay, and preserve provenance. Transcript observation is not mandatory for task correctness when explicit MCP/CLI reporting is available.

## 15. Event model, budgets and observability

Use a versioned event envelope with `eventId`, owner epoch, run-local sequence, source identity, task/attempt/session references, correlation/causation IDs, observed time and bounded payload. Owner sequence/revision governs ordering; wall clocks do not.

Provenance classes must distinguish agent-reported, adapter-observed, execution-observed, controller-verified and inferred information. An event source cannot upgrade its own trust class by placing that label in JSON. An audit event establishes only what its observer actually covers.

Reserve delivery capacity for lifecycle, questions, task results and policy decisions. Terminal traffic and verbose tool observations must not starve them. Coalesce high-frequency display updates without losing the durable control events.

Recommended initial configurable bounds: retain the existing 12-member limit; 128 tasks per plan; four concurrent task executions per run by default, additionally capped by host/profile capacity; one active assignment per member; bounded message/context/artifact sizes; at most three total attempts per task and two automated repair rounds unless the owner explicitly changes the policy. These are proposed defaults, not existing limits.

Limit enforcement is transactional across concurrent tasks. A supervisor or worker cannot increase its own budget. Account quota/rate-limit observations can pause eligible work, but must not trigger credential swapping or duplicate execution.

Token/cost estimates are displayed only when supported by trustworthy usage observations; missing data is “unknown.” An estimated cost is not a hard billing guarantee. Launch count, concurrency, run-owned process limits and retry count can be enforced directly; arbitrary local-agent tool spending cannot necessarily be intercepted.

Record metrics for queued/active/blocked tasks, attempt outcomes, uncertain deliveries, question wait duration, verification failures, stale-result rejections, recovery decisions and gate bypass attempts. Separate active work duration from time waiting on a person.

## 16. UI integration

### Run page

Keep Grid, Graph and Timeline. Grid remains useful for live terminals; graph gains a task view with dependency and artifact edges; timeline shows attempts, verification, waiting and delivery events. Label member topology and task dependencies distinctly rather than mixing them into ambiguous edges.

Each task exposes its owner/member, attempt, source/input version, state, blockers, checks, output and the allowed next action. The header shows aggregate work state separately from terminal activity and whether human input is required.

### Palette and actions

Extend `utils/actions.ts` descriptors and the existing page registration. Proposed actions include Explain blocker, Open task, Show result, Show verification, Retry failed attempt, Pause dispatch, Resume dispatch, Cancel task and Review supervisor proposal. Destructive or privileged actions retain explicit confirmation.

Keep current prefixes and keyboard behavior. Add task results to `#` and actionable harness events to `!` without redefining established syntax. Preserve the terminal's control-key behavior and the existing question-bound `]`/`[` navigation.

Reuse `editorRows(EditorPaletteState, EditorPaletteActs)` for Save, Close file, keymap changes and Ask the agent about the selection. Do not reimplement file editing or bypass the editor's disabled-state explanations, pending-save checks or unsaved-change dialog when navigating task evidence. [S24]

Reuse `sayWords` and `sayDrafts` for interactive send feedback. A definitely refused command can retain its draft; an unconfirmed/partly written command must not turn into a one-click resend. Managed task actions show the richer command receipt rather than translating every transport return into “delivered.” [S21]

Do not replace the palette with an LLM. Deterministic search, state and actions work offline and without a supervisor. Natural-language explanations are optional additions.

### Inspection and transport

Use lazy evidence panes and the shared event/store path. No new timer per task, tile or inspector. Extend `useAttention` or add a selector/composable over its existing subscription; do not establish a competing authoritative frontend store.

Paginate histories and artifacts. A run/task update should not open another terminal connection. Respect file-request and per-connection limits under slow relays and concurrent inspector loads. [S1, S12]

Reuse the existing Touched cache (`usePalette`) and `mergeTouched` rather than rebuilding file search from the limited event feed. Preserve post-first-paint loading, stale-cache refresh and debounced file-event refresh while the palette is open. Retain both token-change invalidation and the generation check that discards an in-flight response for a forgotten credential. A future multi-owner selector must extend cache scoping to owner identity as well as authorization; it cannot combine identically named session IDs from different servers. [S22–S23]

Keep cross-session file navigation through the session page's `?open=` route and its existing file-open checks. Do not treat query parameters as permission to execute commands or edit files. When displaying Touched-derived context to a supervisor, preserve incompleteness/truncation and provenance; do not label it a full workspace inventory. Managed artifacts use separately authorized content retrieval, not this admin-only navigation endpoint. [S22–S23]

Preserve the breadcrumb and inspector-width behavior. Evidence counts and long task/artifact paths must not overflow the rail or page. Do not fix overflow by hiding scrolling that keeps quick-reply controls reachable. Extend the existing `crumbs.spec.ts` and `hscroll.spec.ts` coverage for new panes; the current hscroll fixture's 2,000 px and 1,280 px widths are not proof of phone-layout or real macOS desktop coverage. [S25]

Shared views must filter evidence and context by actual authorization. Supervisor summaries, verification records and artifacts must not leak workbench-only data through a session link or switchyard metadata.

## 17. Optional supervisor

The supervisor is an ordinary optional agent session with scoped tools. It can use an installed CLI and existing account configuration; the core harness does not require a new hosted model API.

Its input is a bounded state/evidence summary with source references, not a constant copy of every terminal. Trigger summaries on meaningful changes with debouncing, not continuous model polling.

The existing Touched index can supply optional file-navigation context but is admin-scoped and incomplete. Do not give an ordinary agent an administrator token to fetch it. The owner provides an authorized, task-scoped projection and retains any truncation/coverage warnings. File-touch observations cannot authorize work or serve as verification receipts.

Mode 1 is read-only: explain blockers, identify missing evidence, summarize progress and propose next actions. Mode 2 can submit typed proposals for plan revisions, remediation, or scheduling changes within a preapproved policy. The deterministic controller validates every proposal exactly as it validates a human command.

The supervisor does not authoritatively mark success, auto-answer people, weaken execution requirements, raise budgets, merge PRs, or infer permission from agent text. If it crashes or gives bad advice, tasks continue according to deterministic state. Its output is labeled inferred, with referenced evidence and a stale-state indication.

Supervisor memory consists of explicit run summaries, decisions and checkpoints already in the ledger. A vector database or separate general memory service is not required.

## 18. Future isolated execution compatibility — **design only, not implementation**

Boxcar remains pre-alpha. **No Boxcar integration is to be built in this project**, even experimentally. This applies to Go packages, CLI commands, wire protocols, schema fields, policy adapters, privileged bridges, environment discovery, audit subscribers, documentation setup instructions, CI jobs, acceptance tests and dependencies. The harness ships entirely on the existing native PTY/process architecture.

To avoid a future rewrite, preserve only these **provider-neutral design invariants**:

1. `Task`, `Attempt`, `Execution` and `Verification` have separate identities and lifecycles. An execution instance can be native today or differently implemented under a future, separately approved design without changing task success semantics.
2. The coordinator refers to execution through a narrow internal contract and records execution identity, host, process/session incarnation, observed capabilities and provenance. The native adapter is the only concrete implementation.
3. Workspace references, artifact identities, checks and delivery receipts are independent of any particular filesystem mount, VM, container or transport. Credentials and policy are never inferred from a worker's text.
4. Capability absence cannot be silently downgraded. The current product must say **not supported** when OS-level isolation or runtime audit guarantees are required.
5. Runtime observations and agent reports are distinct from controller verification. A future isolated runtime's audit data would be additional evidence, never a shortcut to task acceptance.

A later release may evaluate Boxcar again when its interfaces and maturity warrant that work, under its **own specification, issues and tests**. This document neither selects a protocol nor commits to a particular isolation engine. Do not add stubs or placeholder code for that possible integration.

## 19. Remote-owner model

The first remote implementation runs the entire crew under one remote Conductor owner, executing native processes on that owner. A Mac can connect to a Linux owner; paths, credentials, checks and workspaces belong to that execution owner, not to the Mac client.

Connecting through the remote owner's existing authenticated workbench is the simplest initial path. A unified machine selector in the local desktop requires an explicit authenticated owner-control connection and scoped machine identity; it is a separate implementation item. Existing viewer links and terminal-host tokens must not be reinterpreted as remote-administration credentials.

A disconnected client does not stop remote execution. A disconnected owner is marked unreachable. There is no automatic takeover or transfer of the run to the Mac. Reconnection replays state from a cursor and reconciles command receipts before presenting new actions.

Individual tasks spanning multiple owner machines, automatic workspace sync and cross-engine session migration require separate protocols and are not implied by remote terminal visibility.

## 20. API and protocol plan

Reuse the following existing contracts rather than adding equivalent endpoints or frames:

| Existing contract | Scope and reuse rule |
|---|---|
| `GET /api/touched` | Admin-scoped file navigation with limits/truncation; not managed artifact storage |
| `welcome.submitAck` and `submit` with `reqId` / `unlessWaiting` | Existing guarded submission capability; not persistent-command support |
| `submit_ok` / correlated `error` | Owner terminal-write outcome; feed the command ledger without claiming agent acknowledgment |
| `answer` / `answer_ok` and question IDs | Existing answer claim/feedback path; extend association, not a parallel race protocol |
| Session `?open=` navigation and editor palette actions | Reuse file-open/editor guards for task evidence navigation |

Proposed additional owner-side routes:

| Route | Purpose |
|---|---|
| `GET /api/harnesses` | List installed definitions and compatibility |
| `POST /api/harnesses/validate` | Validate a proposed definition without executing setup |
| `GET /api/host-capabilities` | Report the owning host's supported **native** execution and agent capabilities; do not expose hypothetical backend selections |
| `GET /api/runs/{run}/harness` | Return the managed aggregate snapshot and revision |
| `POST /api/runs/{run}/commands` | Accept typed, authorized, idempotent mutations and return their durable receipt |
| `GET /api/runs/{run}/commands/{command}` | Inspect a prior receipt/outcome without redispatch; required for reconnect recovery |
| `GET /api/runs/{run}/tasks/{task}` | Task, attempts, input bindings and allowed actions |
| `GET /api/runs/{run}/artifacts/{artifact}` | Authorized, bounded artifact metadata/content |

Keep existing crew launch/run endpoints and add managed-mode fields without changing their legacy meaning. Read endpoints must not schedule, launch, dispatch messages or grant permissions.

Carry harness change notifications through the existing `/api/events` subscription, with per-run revision/cursor metadata. Use bounded snapshot/replay reads to recover gaps; a subscriber must never depend on an unbounded in-memory event ring. Slow readers must not block execution.

Treat `submitAck`, `reqId`, `unlessWaiting` and `submit_ok` as existing transport functionality. Specify the additive managed-command capability and how it carries/maps durable command IDs, intent, target incarnation and receipts while preserving the current wire bounds and ordinary interactive compatibility. Update `internal/proto`, TypeScript definitions, server and host dispatch, transport handling and `docs/protocol.md` together. Older peers, including peers with only transient `submitAck`, must be shown as incompatible for an action requiring stronger managed semantics rather than silently receiving legacy input. [S13–S14, S20]

Specify limits and error codes in the protocol document: stale revision, stale attempt, incompatible capability, question already resolved, delivery uncertain, queue full, policy denied and evidence unavailable. Expose actionable errors without returning secrets or unbounded payloads.

## 21. Migration and rollout

Add an explicit managed coordination mode to saved crews, with a versioned schema. Legacy crews keep their existing first-done/start behavior. New managed templates use explicit task completion. Do not silently reinterpret old `after` conditions as verified-success dependencies.

Provide an upgrade preview: propose one task per existing member, map the existing start graph, identify missing checks/outputs, and ask the owner to accept the new meaning. Manual starts remain manual gates. Existing shared-directory crews receive a clear warning and a proposed worktree migration, not an automatic filesystem rewrite.

Keep active legacy runs on the legacy runtime path until they finish. Historic records remain readable without conversion to authoritative task evidence. Managed runs use the new ledger; legacy records are compatibility/history, not a second mutable state store.

Distinguish “resume provider conversation,” “retry task,” “resume scheduling,” and “resume legacy run as new run” in API and UI. A managed recovery must not relaunch tasks that already have accepted results merely because the old ResumeRun path starts every member again.

Roll out behind a feature flag for managed crews, with native execution first. Disabling the feature must leave histories accessible and either allow existing managed work to drain or require a deliberate stop; never orphan live executions. Older binaries encountering state must refuse unsafe mutation, not corrupt or discard it.

## 22. Proposed file-level work

| Existing area | Required change |
|---|---|
| `internal/crew/crew.go`, `store.go` | Versioned managed definition and validation; preserve legacy JSON |
| `internal/crew/run.go` | Integrate tasks/attempts into the existing engine; no second scheduler |
| `internal/crew/handoff.go` | Route managed messages through durable inbox/outbox; retain legacy semantics |
| `internal/crew/record.go` | Legacy compatibility and managed-history projection; safe retention separation |
| `internal/crew/worktree.go` | Attempt workspaces, input/result materialization and Git-operation coordination |
| `internal/api/runs.go` and related handlers | Managed commands, preflight and authoritative execution/acceptance checks |
| `internal/session/input.go`, `submit.go` | Reuse writer/claims, `UnlessWaiting`, `SubmitWord` and deferred completion; add durable identity/correlation without raw bypasses |
| `internal/agents/adapter.go`, `internal/notify` | Capability declaration, context injection, result/ack observation |
| `internal/cli/mcp.go`, crew CLI and skill | Scoped task/inbox/checkpoint tools through shared owner logic |
| `internal/proto`, `internal/api/ws_viewer.go`, `internal/hostagent/peer.go`, web protocol/transports | Extend existing `SubmitAck`/`submit_ok` with separately negotiated managed identity, receipt lookup and compatibility rules |
| `web/app/utils/actions.ts`, palette composables | Shared task/run actions and explanations |
| `pages/crews/[[id]].vue`, `pages/runs/[run].vue` | Managed setup, tasks/attempts, blockers, evidence and migration preview |
| `useAttention`, `useQuickReply`, `sayWords.ts`, `sayDrafts.ts`, inspector components | Shared projections, richer receipts, existing uncertainty/draft rules and lazy evidence |
| `internal/api/touched.go`, `internal/session/touched.go`, `internal/signal/hosted.go` | Reuse bounded navigation and current access scope; do not insert task/artifact persistence here |
| `usePalette.ts`, `usePaletteRows.ts`, `touchedIndex.ts` | Reuse index/feed merge and credential-generation invalidation; add owner scoping only when remote-owner support needs it |
| `editorRows.ts`, `EditorArea.vue`, `EditorColumn.vue`, session `?open=` handling | Reuse current editor actions and safe file navigation; preserve unsaved-state checks |
| `FileBrowser.vue`, `InspectorRail.vue`, `SessionInspector.vue`, corresponding e2e tests | Preserve breadcrumb lifecycle, rail bounds and reachable reply controls in new views |
| `docs/protocol.md`, repository map, dependencies and checks | Document contracts, dependency changes and actual validation |

New areas, subject to repository naming review:

```text
internal/harness/                 definitions, resolution, context
internal/execution/               narrow native process lifecycle boundary (no other providers)
internal/crew/ledger/             managed persistence and migrations
internal/crew/tasks.go            task/attempt model and transitions
internal/crew/scheduler.go        scheduling integrated with crew.Engine
internal/crew/delivery.go         durable command/inbox coordination
internal/crew/artifacts.go        artifact and verification ownership
internal/crew/supervisor.go       bounded advisory integration
web/app/composables/useHarnessRun.ts  selectors/actions over shared live state
```

Keep business logic out of CLI and UI packages. Avoid circular dependencies by defining small contracts around execution and storage rather than importing API handlers into the engine.

## 23. Ordered implementation backlog

Create a GitHub epic and one issue/branch/PR per deliverable, following `AGENTS.md`. Cut implementation branches from the then-current `main` and run affected-surface checks for every PR. The IDs below name implementation work packages; they are not existing GitHub issue numbers. The native harness is the entire release scope; the supervisor and remote-owner UX are optional work streams, and **there is no isolated-execution integration work stream**.

| ID | Deliverable | Dependencies | Acceptance boundary |
|---|---|---|---|
| H01 | Baseline regression tests, ADRs, domain schemas and execution capability contract | Source baseline | Existing submission, question, file navigation, editor and legacy crew behavior is preserved; interfaces reviewed |
| H02 | Managed ledger, transactional outbox, schema migrations and recovery skeleton | H01 | Crash, replay, corruption and disk-full tests; durable reservation and event ordering |
| H03 | Managed commands, idempotency, correlation and receipt lookup layered over existing guarded submissions | H01, H02 | Request retries never cause an unexamined second write; uncertainty is explicit; unsupported peers fail before dispatch |
| H04 | Versioned harness definitions, digest resolution and bounded context bundles | H01 | Packages validate on Mac without implicit setup execution or overwriting user configuration |
| H05 | Native process execution seam and capability reporting | H01 | Existing real PTYs work on Mac/Linux/WSL unchanged; no alternative provider implementation or backend selector |
| H06 | Task/attempt DAG and bounded event-driven scheduler integrated into `crew.Engine` | H02–H05 | Conversational `done` or `submit_ok` never completes a task; one active attempt/member assignment |
| H07 | Durable inbox, handoff, checkpoint, and task-scoped MCP/CLI operations | H02, H03, H06 | Messages to unstarted members persist; backpressure does not discard accepted work |
| H08 | Immutable result capture, worktree materialization, verification and revisions | H06, H07 | Reviewers see exact builder outputs; stale checks cannot approve changed results |
| H09 | Existing-UI integration, explicit migration/upgrade flow and shared-state projections | H03, H06–H08 | Palette/grid/graph/timeline agree; no new polling or file-request overload; legacy mode remains compatible |
| H10 | Optional read-only supervisor and policy-validated proposals | H07–H09 | Disable supervisor without affecting scheduling; suggestions never bypass authority |
| H11 | Optional remote-owner access and machine selector | H05, H09 | Mac can control a Linux-owned **native** run without duplicate run ownership or unsafe fallback |
| H12 | Release qualification, platform tests, migration, recovery drills and documentation | Relevant implementation work | Reproducible preflight receipts, fault injection results, rollback/readability and honest coverage claims; no Boxcar runtime dependencies |

**Release gates.** H01–H09 and H12 are the core release path. H10 is optional and non-authoritative; H11 is separately optional for remote-owner UX. There are no VM/container runtime milestones, dependencies, test lanes or feature flags in this plan. Ship the cross-platform native harness without waiting for future isolation technology.

### Vertical slice A: complete native Mac crew

Run one implementation task in an attempt-specific worktree, capture its exact output, verify it, and hand the frozen candidate to a reviewer. Surface a real question through the existing collaboration UI. Persist the managed command receipt. After an owner interruption, preserve the task and identify uncertain delivery instead of duplicating it. A `submit_ok` must not advance task completion. No supervisor, virtualization or additional model API is necessary.

### Vertical slice B: revision and acceptance

Allow a reviewer to submit findings against an exact candidate. Revise the implementation in a new attempt/result version, invalidate stale evidence, rerun applicable checks, and deliver a final outcome to the owner. Limit repairs by explicit budget and require consent for changes that exceed the authorized plan.

### Vertical slice C: optional native extensions

Add read-only supervisor explanations, then optional scoped proposals. Independently, add remote-owner control for **native** processes without cross-machine task migration or silent relocation of execution. Future isolated runtimes are expressly excluded from this implementation plan.

## 24. Test and acceptance matrix

Tests must be deterministic where possible, using fake clocks, controlled process boundaries and the existing stub agents. Add property/model tests for the scheduler and failure injection around every durable side-effect boundary.

### Existing regression suites to extend, not recreate

The repository includes `internal/proto/submit_test.go`, `internal/session/submitword_test.go`, `internal/api/submitack_test.go`, `internal/hostagent/submitack_test.go`, touched-index/API/hosted tests, `sayWords.test.ts`, `sayDrafts.test.ts`, `editorRows.test.ts`, `touchedIndex.test.ts`, and transport/palette coverage. Preserve their existing behavioral contracts before adding ledger assertions. The UI also has `palette-touched.spec.ts`, `crumbs.spec.ts` and `hscroll.spec.ts`. These suites provide regression foundations; their presence does not constitute execution by this specification. [S2, S20–S25]

### Harness-specific acceptance additions

| Scenario | Required outcome |
|---|---|
| Legacy crew saved/run/resumed | Existing meaning and UI remain intact |
| First conversational done signal or `submit_ok` | Task remains incomplete until explicit candidate and required checks |
| Current owner receives `say` with `unlessWaiting` | Existing `not_sent` refusal and no terminal write preserved |
| Deferred `ErrSubmitPending` | No premature receipt; eventual completion observation applied once |
| Same connection-local `reqId` on different connections | No collision with another durable command or actor |
| Repeated durable command after aggregate revision advances | Matching authorized receipt returned, no second write; mismatched payload rejected |
| Peer has `submitAck` but lacks managed-command support | Managed dispatch rejected before bytes are sent; ordinary interactive compatibility stays explicit |
| `submit_ok` is lost before durable outcome is known | Inspect receipt/reconcile; no automatic resend or invented semantic acknowledgment |
| Definite refusal vs partial/unconfirmed input | Preserve current `sayWords` draft retention and no-resend distinction |
| Two upstream dependencies | Downstream waits for both applicable results |
| Duplicate/out-of-order event | No duplicate launch or state regression |
| Two launch requests with one idempotency key | Original receipt returned; conflicting payload rejected |
| Late result from attempt 1 after attempt 2 starts | Stored diagnostically, rejected as current completion |
| Upstream result changes | Old downstream input/review cannot approve the new candidate |
| Reviewer in separate worktree | Receives builder's actual pinned changes, not the original base |
| Message to unstarted member | Durably queued and later acknowledged in order |
| Inbox capacity exceeded | New message rejected explicitly; existing work retained |
| Crash before/after launch or terminal write | Reconciled or uncertain; no blind replay |
| Partially typed text or Enter | Existing stranded/held behavior preserved; no duplicate submission |
| Multiple people answer one question | One authorized winner; others receive a stale/resolved response |
| Question left open indefinitely | No auto-answer, task success or retry exhaustion |
| Link revoked while input queued | Authority rechecked; unbegun write withdrawn |
| Bulk answer with one stale question | Independent outcomes; no answer to replacement question |
| User takes over a terminal | Automatic delivery pauses; pending commands do not overwrite human interaction |
| Fake provider hook claims verification success | Cannot mint controller verification evidence |
| Symlink/path escape or secret snapshot inclusion | Rooted access/allowlist rejects unsafe capture |
| Test changes code while checks run | Result identity fails stability/applicability checks |
| Remote owner disappears | Unreachable state; no replacement run launched locally |
| PID/session identity is recycled | Old attempt cannot adopt unrelated execution |
| Ledger disk full/corrupt migration | No falsely durable dispatch/completion; history preserved |
| New UI through slow relay | Bounded requests, no independent polling, terminal remains responsive |
| Palette Touched item predates the recent event feed | Existing server index supplies navigation without claiming a complete history |
| Touched response or row cap truncates data | Do not report completeness or use absence as proof a file was untouched |
| Workbench credential changes during Touched/evidence fetch | Clear scoped cache and discard the late response for the old credential |
| Same session ID on two remote owners | New owner-scoped projections cannot mix files, actions or receipts |
| Shared session link requests global Touched/evidence | Existing admin boundary and explicit artifact authorization retained |
| Cross-session `?open=` with a dirty current editor | Existing editor/navigation safeguards respected; no implicit save or overwrite |
| Long evidence names/count pills, expanded/collapsed inspector | No page/rail overflow; quick replies and intended pane scrolling remain reachable |
| Breadcrumb explorer/changes switch | Existing mount/recreation behavior retained without initialization errors |
| Native macOS without virtualization or Docker | Complete crew workflow passes using installed agent CLIs |
| WSL2 without KVM or optional VM tools | Same native harness functionality passes, with no hypervisor prerequisite |
| Linux without any VM/container runtime | Same native harness functionality passes |
| Task requests OS-level isolation that is not supported | Explicitly blocked; no claim that a worktree is a sandbox |
| Static/dependency inspection of the harness build | No Boxcar import, install step, binary probe, configuration key or execution-specific test lane |
| Supervisor proposes policy escalation or auto-merge | Denied by owner-side authorization |
| Old client/owner protocol | Unsupported managed action refused; no unsafe legacy fallback |

### Validation commands

Use current `main`'s documented checks, not a separate harness-only certification process: [S16]

```bash
make lint
go test -race -count=1 ./...
npm --prefix web run typecheck
npm --prefix web run typecheck:e2e
npm --prefix web test
make web-build
make build-go
make test-e2e
make desktop-test
make lint-static
make vuln
```

Run relevant live agent and recipe checks with real installed CLIs. Run cross-platform preflight on the actual Mac/Windows/Linux environments where available. Every test that launches sessions sets `CONDUCTOR_RENDEZVOUS=0`; test sessions must not publish to the public switchyard. Regenerate/check notices for dependency changes. Report unavailable checks as not run. A user-agent override is not a substitute for real macOS keyboard/desktop testing.

Suggested performance targets, to validate rather than claim: no new periodic per-task polling; bounded initial snapshots and paginated history; palette operation without model calls; lifecycle/control updates not starved by terminal output; no extra terminal connection per artifact view. Establish measured latency/memory budgets with a repeatable multi-session fixture before choosing numerical release thresholds.

## 25. Definition of done

The harness is complete when a managed crew can plan/receive tasks, execute natively on macOS/Linux/WSL, wait for humans, exchange exact results, independently verify work, revise failed candidates within limits, recover without silent duplication, and present consistent state through the existing UI. Its runtime and verification features must not depend on any VM/container implementation.

The optional supervisor is complete when it can explain and propose actions with evidence and cannot override policy. Optional remote-owner support is complete when a Mac can control a remote owner's native run without becoming a second scheduler or receiving misleading local-execution guarantees. A future isolation backend has **no definition of done, PR, or release gate in this project**.

Across all modes: an agent's claim is not verification; a quiet terminal is not success; an accepted command is not proof of delivery; a worktree is not a sandbox; an absent reply is not approval; and a disconnected host is not permission to launch a duplicate.

## 26. Borrowing from OpenHarness

Borrow the separation of agent and domain package, normalized observations, task-attempt identity, explicit artifact handoffs and conservative handling of uncertain operations. Its package documentation describes a manifest/instructions/skills/toolchain/viewer/verdict contract; that is useful inspiration, not a requirement to reproduce its UI or daemon. [S19]

Do not import its tmux lifecycle, relay, client application or orchestration service wholesale. Conductor already owns those concerns. A worker-written verdict can be displayed as a claim; Conductor's verified acceptance remains controller-owned.

Any actual copied code needs a per-file provenance/license inventory and appropriate third-party notices. This specification copies no upstream implementation and does not prescribe an upstream dependency.

## 27. Source inventory and review limitations

This is a code-informed specification, **not** an implementation, test run or security audit. Claims about existing Conductor behavior are based on the `main` snapshot at `e8fc12f04c750c280e54e4bb4afc3d1a2a274464`. Every implementation work item MUST revalidate its own code paths, current branch head and dependency versions before modification. The following references also identify the particular existing tests to preserve and extend.

| Reference | Source of existing behavior or implementation constraints |
|---|---|
| S1 | Conductor `main` branch metadata and repository architecture at the pinned commit |
| S2 | Pinned source tree and relevant packages under `internal/crew`, `internal/session`, `internal/api`, `web/app` and `desktop` |
| S3 | `web/app/utils/actions.ts`: shared UI actions |
| S4 | `web/app/composables/useQuickReply.ts`: guarded submission and UI outcome mapping |
| S5 | `internal/crew/crew.go`: crew definitions and launch validation |
| S6 | `internal/crew/run.go`: session-based crew engine and launcher contract |
| S7 | `internal/crew/handoff.go`: bounded session handoff delivery |
| S8 | `internal/crew/record.go`: historical records and interrupted-run reconciliation |
| S9 | `internal/session/input.go`, `internal/session/submit.go`: writer, question claims, delayed/partial input and submission guards |
| S10 | `internal/agents/adapter.go`, `internal/cli/mcp.go`: vendor adapters, hooks, skills and MCP |
| S11 | `internal/api/runs.go`: crew session-launch integration |
| S12 | `web/app/pages/runs/[run].vue`: Grid/Graph/Timeline and shared state |
| S13 | `AGENTS.md`: repository code, security and issue workflow contract |
| S14 | `docs/repository-map.md`: source layout and extension points |
| S15 | `docs/dependencies.md`: pinned dependencies and review conventions |
| S16 | `docs/checks.md`: affected-surface checks and platform test requirements |
| S19 | OpenHarness `store/README.md`: domain-specific package format as design reference, not an imported dependency |
| S20 | `internal/proto/control.go`, `internal/session/submit.go`, `internal/api/ws_viewer.go`, `internal/hostagent/peer.go`, `web/app/utils/transport/base.ts` and submit-ack tests |
| S21 | `web/app/utils/sayWords.ts`, `sayDrafts.ts` and related tests: confirmation/refusal/uncertainty UI |
| S22 | `internal/api/touched.go`, `docs/plans/touched-index.md`: bounded cross-session Touched endpoint |
| S23 | `web/app/composables/usePalette.ts`, `web/app/utils/touchedIndex.ts`: cached search and feed overlay |
| S24 | `web/app/utils/editorRows.ts`, `EditorArea.vue`: editor palette and action guards |
| S25 | `web/e2e/crumbs.spec.ts`, `web/e2e/hscroll.spec.ts`: breadcrumb/inspector layout tests |
| S26 | SQLite documentation on atomic commit: background for the transactional storage ADR (database atomicity does not imply atomic PTY delivery) |

**Pinned repository sources:**

- [Conductor at the inspected commit](https://github.com/phenixrizen/conductor/tree/e8fc12f04c750c280e54e4bb4afc3d1a2a274464)
- [Conductor crew engine](https://github.com/phenixrizen/conductor/blob/e8fc12f04c750c280e54e4bb4afc3d1a2a274464/internal/crew/run.go)
- [Conductor control protocol](https://github.com/phenixrizen/conductor/blob/e8fc12f04c750c280e54e4bb4afc3d1a2a274464/internal/proto/control.go)
- [Conductor submission flow](https://github.com/phenixrizen/conductor/blob/e8fc12f04c750c280e54e4bb4afc3d1a2a274464/internal/session/submit.go)
- [Conductor frontend transport](https://github.com/phenixrizen/conductor/blob/e8fc12f04c750c280e54e4bb4afc3d1a2a274464/web/app/utils/transport/base.ts)
- [OpenHarness domain packages](https://github.com/autonomous-ai/openharness/blob/main/store/README.md)
- [SQLite atomic commit](https://www.sqlite.org/atomiccommit.html)

---

## Errata and decisions (2026-10-22)

The code was read against this document on 2026-10-22 (`main` at 37739bc, after #333). The implementation follows `docs/plans/agent-harness-order.md`, the order document beside this file; where the two differ, the order document wins. The tracking issue is #334.

**Decisions taken by the owner.**

- Storage (§11.1): SQLite through `modernc.org/sqlite`, pure Go so `CGO_ENABLED=0` stays; one database per data directory, imported by one package. The intent-before-side-effect rule and the uncertain states stay: a row is never atomic with a PTY write, and SQLite's locks elect no scheduler, so an owner lock on the data directory stays too.
- Scope (§23): H01–H09 become slice A and slice B of the order document. H10 (the supervisor) and H11 (the remote owner) are out of this release, as are package manifests (§9.1) and a content store beyond check logs (§12.2).
- Tracking: one tracking issue (#334), one child issue and one PR per row of the order document, each cut from `main` once the one before it merged; the owner merges.

**Mechanisms proposed above that the code does not need.**

1. §10.5 and §20, the managed-command wire capability. Scheduler deliveries are in-process `Local.Submit` calls, as handoffs are today; browser and agent mutations are HTTP with command receipts. Nothing in `internal/proto`, `ws_viewer.go`, `peer.go` or the web transport changes. A hosted member would be the only remote peer, and `Crew.Launchable()` refuses host crews (#152, #162).
2. §5 and §22, `internal/execution`. `crew.Launcher`, one method, is the seam; the attempt rides in `session.CrewRef`; a session ID is a fresh identity per launch, so no incarnation field is needed.
3. §10.2, delivery `acknowledged` by hook correlation. No agent's hooks carry one. The acknowledgment is the agent reading its task (`task_context`) or its inbox cursor; the levels are accepted, written (the Enter landed) and read.
4. §10.3, unknown readiness as a hard blocker. The engine's 60 s cap stays for the first line, since an agent that prints nothing at its start would otherwise stall an unattended run; a task not read within a bound shows as attention.
5. §9.1, packages. Tasks live in the saved crew's JSON, strictly decoded (an older binary refuses a crew with fields it does not know); the task file written at dispatch is the resolved, recorded instruction.
6. §12.2, dirty snapshots. A result is a commit; the next task's worktree is created at that commit; a dirty tree is refused.
7. §13, a durable decision history for questions. The claim state machine and first-answer-wins exist; the harness projects a question as "waiting on a person" and spends no attempt on it.
8. §15 and §20, an event envelope with owner epochs and cursors. The existing `run` event plus a re-read of the run is the bounded snapshot read; the ledger's sequence is the run's revision.
9. §21, resume. `ResumeRun` makes a new run and relaunches every member with a resumable conversation at once, whatever its start rule (`run.go:595-621`); only the members without one wait for their rule. A managed resume is a continuation that imports accepted results by reference and dispatches only what is unfinished.

**Statements above that the code contradicts.**

- §2.2: a handoff dropped past ten is noted in the run's own log, not the server log (`handoff.go:138`); a member still `starting` with a live session accepts handoffs (`handoff.go:185-189`); worktrees are made in the engine (`run.go:832-873`), not in `runs.go`.
- §2.2, §2.3 and S15: `docs/dependencies.md` is a pin table; the dependency rules are in `AGENTS.md`. S14: `docs/repository-map.md` has "Adding things", no "extension points" section. There is no ADR directory and no epic convention. The stub agent is `web/e2e/stub-agent.sh`; there is no `internal/agents/stub*`.
- §2.4: `welcome.submitAck` is announced by every owner, not negotiated (`local.go:2387-2388`); `input_failed` has no proto constant and is missing from protocol.md's error-code list.
- §9.2: `session.MaxSubmitText` is 32,756 bytes (`proto.MaxInput` minus the paste markers); 4,096 bytes is the browser wire's `submit` limit. `SubmitLine` flattens newlines to spaces, so a task file carries the context, never a typed document.
- §2.3 and S10: `agents.Adapter` is a struct of per-agent fields (`Inject`, `MCP`, `ConfirmsSubmit`, `Probe`, …), not a capability set an engine can query; MCP is wired for Claude Code and Codex only, on server sessions only; submission confirmation (Claude Code only) is "any attention change not from a person's typing within 3 s, else Enter again" (`confirmed()` leaves out `SourceInput`), not an acknowledgment correlated with a delivery.
- §14: the agent token in the environment (`CONDUCTOR_NOTIFY_TOKEN`) is the authentication the MCP server and the CLI use, not metadata beside it.
- §24: the check list omits `make preflight` and the Python suite (`docs/checks.md`).
- §11.4, confirmed: `record.go`'s prune is unconditional and can delete a live run's record (#335).
- Found on the way: no data-directory ownership lock exists (two servers may share one, and each prunes the shared `runs/` directory); the SSE stream has no cursor; `createLocalSession` starts the PTY before the registry's capacity check (`sessions.go:275`, `:335`), so a refusal at the cap is not free of side effects; every release binary is `CGO_ENABLED=0` and cross-compiled, so a cgo driver would break the build.
- "Journal" is taken: `Engine.Journal` is the snapshot save. The new store is the ledger.
- §5 and §18, the execution seam (2026-11-01, Program item C10): by the Program's decision D7 the seam is `ext.Runtime` in `ext`'s registry, provider-neutral, its free default the native Runtime of `ext/local`, and the ledger's evidence rows gain `where` (`docs/plans/runtime.md`); §18's line on Boxcar stands until item F.
