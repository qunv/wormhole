# Agent harness coordination architecture

## 1. Purpose

This document describes a proposed architecture for using Wormhole as a vendor-neutral coordination and model-routing plane between user interfaces, reasoning providers, and execution harnesses.

ChatGPT Web and Codex are the initial reference implementations, not hard dependencies of the architecture. In particular, the design must not assume that the model used by an execution harness is owned by that harness.

The design separates four responsibilities:

- **Interaction / Planner UI**: presents the conversation, receives user intent, and displays plans, execution progress, diffs, and review results. ChatGPT Web and Codex UI are possible surfaces.
- **ReasonerProvider**: performs model inference and reasoning. It may be OpenAI, another model provider, or a future supported ChatGPT-session adapter.
- **Execution Harness**: manages repository interaction, tool calls, code changes, test/build/lint loops, and implementation iteration. Codex is the first reference harness.
- **Wormhole**: owns canonical task state, decisions, memory, execution leases, evidence, policy, handoff data, and optional model routing between harnesses and reasoners.

The main problem this architecture solves is long-lived coding work that outgrows a single conversation, model context, browser session, model backend, or executor process.

The core invariants are:

```text
Transcript is not state.
UI context is not state.
Harness context is not state.
Model-provider context is not state.
Task state belongs in Wormhole.
```

A second set of invariants is equally important:

```text
UI != harness.
Harness != reasoner.
Reasoner != protocol.
Transport != protocol.
```

The system should continue to work if the UI, harness, or reasoner is replaced, provided the replacement can integrate through a supported adapter.

## 2. Overall architecture

```text
┌──────────────────────────────────────────┐
│              Interaction UI              │
│                                          │
│ ChatGPT Web / Codex UI / IDE / other UI │
└────────────────────┬─────────────────────┘
                     │
               Planner Adapter
                     │
             WorkPacket / ReviewDecision
                     ▼
┌──────────────────────────────────────────┐
│                 Wormhole                 │
│                                          │
│ Coordination Core + optional AI Gateway  │
│                                          │
│ - canonical task state                   │
│ - decisions and rationale                │
│ - memory and context references          │
│ - execution claims and leases            │
│ - evidence and review state              │
│ - policy / audit boundary                │
│ - reasoner routing policy                │
└───────────────┬──────────────────┬───────┘
                │                  │
        Executor Adapter     ReasonerProvider
                │                  │
                ▼                  ▼
┌───────────────────────┐  ┌───────────────────────┐
│  Execution Harnesses  │  │    Model Backends     │
│                       │  │                       │
│ Codex / other harness │  │ chat-latest / GPT /   │
│ / deterministic runner│  │ other providers       │
└───────────────────────┘  └───────────────────────┘
```

A harness may call its reasoner directly, or it may be configured to call a Wormhole-compatible model gateway. The latter keeps the harness UI and execution loop while moving model selection and model policy outside the harness.

Wormhole is not itself the primary reasoning model or coding harness. It provides stable protocol, state, policy, and routing boundaries between those environments.

## 3. Reference implementations

The initial workflow can use two related reference layouts.

The first keeps ChatGPT Web as the planner/reviewer and Codex as an agentic executor:

```text
Planner / Reviewer UI: ChatGPT Web
Execution harness:     Codex
Reasoner for planner:  ChatGPT-hosted model
Reasoner for harness:  Codex-configured provider
Coordination:          Wormhole
Transport:             MCP where available
```

The second uses Codex UI as the end-user interaction surface while moving its model backend behind Wormhole:

```text
End-user UI:        Codex UI
Execution harness: Codex
Model provider:     Wormhole AI Gateway
Reasoner:           configured upstream model
Coordination/state: Wormhole
```

Codex currently supports custom model providers with a configurable API `base_url`; the provider wire protocol is the Responses API. This makes a local Wormhole Responses-compatible gateway a viable integration boundary without requiring Codex to own model selection.

These choices are reference layouts, not architecture requirements. A future deployment can replace the UI, execution harness, reasoner, or transport independently.

The canonical task state must not require migration when any of those components changes.

## 4. Planner / Reviewer responsibilities

A planner or reviewer should focus on work where reasoning and human collaboration have the highest value:

- understand the user's goal;
- analyze architecture and trade-offs;
- establish constraints and invariants;
- define implementation scope;
- split work into executable steps;
- define acceptance criteria;
- review execution output and evidence;
- approve, reject, or redirect an implementation;
- decide when a task is complete.

The planner should reduce reasoning outcomes into structured state rather than requiring the complete discussion to survive.

For example:

```yaml
task_id: binding-isolation

goal:
  Prevent independently running daemons from sharing tunnel identities.

constraints:
  - Legacy CodeBridge and Wormhole must run simultaneously.
  - Existing CodeBridge tunnel IDs must remain unchanged.
  - Wormhole must use independent tunnel IDs.

decisions:
  - Session bindings remain daemon-local.
  - Full and fast endpoints may share one SessionRouter.
  - Tunnel identity must be unique per independently running daemon.

acceptance_criteria:
  - Both daemons run simultaneously.
  - Wormhole Fast and Full use their own tunnel IDs.
  - workspace_select followed by workspace_current succeeds reliably.
  - CodeBridge bindings are unaffected.
```

The planner adapter converts planner-specific interaction into the neutral Wormhole protocol.

## 5. Executor responsibilities

An executor receives a sufficiently precise `WorkPacket` and performs the implementation loop.

A normal execution cycle is:

```text
Understand task
    ↓
Inspect repository
    ↓
Create or refine implementation plan
    ↓
Modify code
    ↓
Run targeted tests
    ↓
Run broader verification
    ↓
Review diff
    ↓
Return structured evidence
```

Executors are suited to work such as:

- multi-file changes;
- repository exploration;
- call-graph investigation;
- repeated compiler/test failure repair;
- refactoring;
- build and lint loops;
- bounded shell/process work;
- final diff inspection.

The executor must not become the owner of canonical task state. Harness-specific session IDs, thread IDs, prompts, and logs are adapter metadata only.

## 6. Vendor-neutral adapter model

Wormhole should depend on abstract planner and executor contracts rather than directly on one vendor implementation.

A conceptual executor interface is:

```go
type Executor interface {
    ID() string
    Capabilities() ExecutorCapabilities

    Start(ctx context.Context, task WorkPacket) (ExecutionID, error)
    Resume(ctx context.Context, id ExecutionID) error
    Cancel(ctx context.Context, id ExecutionID) error
    Status(ctx context.Context, id ExecutionID) ExecutionStatus
    Result(ctx context.Context, id ExecutionID) ExecutionResult
}
```

Possible implementations include:

```text
CodexExecutor
OtherHarnessExecutor
LocalProcessExecutor
RemoteExecutor
ReviewOnlyExecutor
```

The same principle applies above Wormhole:

```text
ChatGPTPlannerAdapter
OtherWebPlannerAdapter
IDEPlannerAdapter
APIPlannerAdapter
ManualImportAdapter
```

Wormhole should not require all adapters to use the same transport.

### 6.1 ReasonerProvider and AI Gateway

The model backend should be abstracted independently from both the planner UI and the execution harness.

A conceptual provider interface is:

```go
type ReasonerProvider interface {
    ID() string
    Capabilities() ReasonerCapabilities
    Complete(ctx context.Context, req ReasonerRequest) (ReasonerResult, error)
}
```

Possible providers include:

```text
OpenAIResponsesProvider
ChatLatestProvider
OtherAPIProvider
LocalModelProvider
FutureChatGPTSessionProvider
```

A user-selectable reasoner should be represented by a canonical `ReasonerProfile`, not only by a raw model ID. A conceptual profile is:

```yaml
id: chatgpt-web-sol
provider: openai-responses
model: gpt-5.6-sol

reasoning:
  effort: high

context_policy: wormhole-task
memory_policy: wormhole
tool_policy: codex-compatible
session_policy: stateless-with-wormhole-state

accounting:
  usage_surface: api
  quota_pool: provider-defined
  inherit_chat_conversation_usage: false
```

`usage_surface` describes the execution/accounting surface through which inference is actually performed, not the branding or capability family of the selected model. Initial values may include:

```text
chat
agentic
api
local
external
unknown
```

The accounting metadata is descriptive and policy-relevant; it must not claim or emulate a provider quota bucket that the upstream provider does not officially expose. In particular, selecting a profile whose model is also used by ChatGPT does not make a Codex or API invocation count as a regular Chat conversation.

The `FutureChatGPTSessionProvider` name is intentionally aspirational. The architecture must not assume that an active ChatGPT Web conversation can currently be invoked as a supported model endpoint. Browser automation, cookie reuse, scraping, or private ChatGPT endpoints are not acceptable provider implementations.

A Wormhole AI Gateway can expose a Responses-compatible endpoint to harnesses that support configurable model providers:

```text
Codex UI
   │
   ▼
Codex harness
   │ Responses API protocol
   ▼
Wormhole AI Gateway
   │
   ├── state/context injection
   ├── model routing policy
   ├── provider credentials
   ├── cost/model restrictions
   └── request/result observability
           │
           ▼
     ReasonerProvider
```

For Codex, the conceptual configuration is:

```toml
model = "chatgpt-like"
model_provider = "wormhole"

[model_providers.wormhole]
name = "Wormhole AI Gateway"
base_url = "http://127.0.0.1:8132/v1"
wire_api = "responses"
```

This is a deployment sketch rather than a committed Wormhole configuration contract. The exact endpoint, authentication, streaming behavior, and supported Responses features must be validated before implementation.

The first upstream reasoner can be an API-accessible OpenAI model. `chat-latest` is useful for compatibility experiments because it points to the latest Instant model used in ChatGPT and supports the Responses API, function calling, structured outputs, and MCP. It is still an API model endpoint, not the ChatGPT Web product or an existing browser conversation.

This separation creates three independently replaceable dimensions:

```text
UI       = Codex UI / ChatGPT Web / IDE / other surface
Harness  = Codex / deterministic runner / other harness
Reasoner = OpenAI / other provider / future supported session provider
```

A key policy dimension is whether a harness may choose its own model or must use a Wormhole-routed provider. For example:

```json
{
  "reasoner_policy": {
    "routing": "wormhole_only",
    "allow_harness_model_override": false,
    "allowed_providers": ["openai-chat"],
    "allowed_usage_surfaces": ["api", "local"],
    "require_accounting_surface": true
  }
}
```

This allows Codex to retain its UI and agent loop while preventing the harness from silently switching to a different reasoning backend.

### 6.2 Current compatibility facts

The architecture should distinguish verified integration facts from future extension points.

As of the current design review:

- Codex supports custom `model_providers`, including a provider `base_url` and authentication configuration.
- The supported custom-provider wire protocol is the Responses API.
- OpenAI exposes `chat-latest`, described as the latest Instant model used in ChatGPT, through `v1/responses`.
- `chat-latest` supports function calling and structured outputs, but does not advertise every coding-specific hosted tool. Therefore Codex compatibility must be verified across the full local agent/tool loop rather than inferred from basic Responses compatibility.
- No assumption is made that an existing ChatGPT Web conversation can be called as a model provider. Such an adapter should be added only if a supported interface becomes available.
- Model identity and usage accounting are separate concerns. The same model family may be reachable through Chat, agentic product surfaces, or API/provider surfaces with different quota and billing rules.
- A `ReasonerProfile` must record its actual `usage_surface` and must not imply that a Codex/API invocation inherits the regular Chat conversation allowance merely because the profile resolves to a model also used by ChatGPT.

Official references:

- [Codex configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
- [OpenAI `chat-latest` model reference](https://developers.openai.com/api/docs/models/chat-latest)

## 7. Wormhole Agent Handoff Protocol

The architecture should define a transport-neutral handoff protocol owned by Wormhole.

The first version can be based on four canonical objects:

```text
WorkPacket
ExecutionClaim
ExecutionResult
ReviewDecision
```

The flow is:

```text
Planner
   │
   │ WorkPacket
   ▼
Wormhole
   │
   │ ExecutionClaim
   ▼
Executor
   │
   │ ExecutionResult
   ▼
Wormhole
   │
   │ ReviewDecision
   ▼
Planner / Reviewer
```

MCP is one transport for this protocol, not the protocol itself.

The same logical contract may later be exposed through:

```text
MCP
HTTP
SDK
CLI
local IPC
job queue
```

This distinction prevents transport lock-in as well as model/vendor lock-in.

## 8. WorkPacket

The primary handoff unit between planner and executor is a structured `WorkPacket`, not a transcript and not a vendor-specific prompt.

A conceptual schema is:

```json
{
  "protocol_version": "1",
  "task_id": "binding-isolation",
  "goal": "...",
  "constraints": [],
  "decisions": [],
  "plan": [],
  "acceptance_criteria": [],
  "context": {
    "files": [],
    "symbols": [],
    "memory_refs": []
  },
  "open_questions": [],
  "evidence": []
}
```

A useful `WorkPacket` answers five questions:

1. What are we trying to achieve?
2. What has already been decided?
3. What must not change?
4. Where should the executor look?
5. How do we know the task is complete?

The packet should not contain unnecessary browser history, hidden reasoning, rejected reasoning branches, or unbounded tool output.

Vendor-specific prompt construction belongs inside the adapter:

```text
WorkPacket
    ↓
Codex adapter
    ↓
Codex-specific execution input
```

Another executor can transform the same packet differently without changing canonical task state.

## 9. ExecutionClaim and execution leases

Multi-executor support requires explicit ownership rather than assuming one worker exists.

A conceptual claim is:

```json
{
  "execution_id": "E-456",
  "task_id": "T-123",
  "executor_id": "codex-worker-2",
  "claimed_at": "...",
  "lease_until": "..."
}
```

A lease allows Wormhole to recover from crashed or disconnected workers.

```text
READY
  ↓ claim
CLAIMED
  ↓
RUNNING
  ↓ worker disappears and lease expires
READY
```

Claims and leases also prevent two executors from accidentally mutating the same task concurrently unless the workflow explicitly allows parallel sub-tasks.

## 10. ExecutionResult

Executors should return a neutral structured result rather than requiring the planner to understand harness-specific logs.

A conceptual schema is:

```json
{
  "execution_id": "E-456",
  "executor_id": "codex",
  "status": "needs_review",
  "summary": "...",
  "changes": [],
  "tests": [],
  "evidence": [],
  "risks": [],
  "questions": []
}
```

Adapter-specific raw output may be retained for diagnostics, but the canonical review path should use the normalized result.

The reviewer should normally need only:

- resulting diff or change summary;
- verification evidence;
- unresolved decisions;
- implementation risks;
- deviations from the agreed plan.

This keeps review context compact even when the executor performed many tool calls internally.

## 11. ReviewDecision

A planner or human reviewer should return an explicit review decision.

A conceptual schema is:

```json
{
  "task_id": "T-123",
  "execution_id": "E-456",
  "decision": "request_changes",
  "summary": "...",
  "required_changes": [],
  "new_decisions": [],
  "acceptance_updates": []
}
```

Typical outcomes are:

```text
approve
request_changes
blocked
cancel
```

Review output becomes durable state and can produce the next `WorkPacket` without replaying the original conversation.

## 12. Task state machine

Long-running work should have an explicit lifecycle.

```text
READY
  │
  ▼
CLAIMED
  │
  ▼
RUNNING
  │
  ├──────────────► BLOCKED
  │
  ├──────────────► FAILED
  │
  ▼
NEEDS_REVIEW
  │
  ├──────────────► READY
  │                 │
  │                 └── another execution round
  │
  ▼
APPROVED
  │
  ▼
DONE
```

Terminal or administrative states may also include:

```text
CANCELLED
```

State meanings:

| State | Meaning |
|---|---|
| `READY` | The task contains enough information for an executor to start. |
| `CLAIMED` | One executor holds an active claim/lease. |
| `RUNNING` | The executor is actively implementing or verifying the task. |
| `BLOCKED` | Execution requires a decision or dependency that cannot be resolved safely. |
| `FAILED` | The execution attempt ended unsuccessfully and requires retry or review. |
| `NEEDS_REVIEW` | Implementation is ready for planner/human review. |
| `APPROVED` | The implementation has been accepted and is ready for finalization. |
| `DONE` | Acceptance criteria and required completion steps are satisfied. |
| `CANCELLED` | The task was intentionally abandoned. |

## 13. Capability negotiation

Executors should advertise capabilities so Wormhole can avoid routing incompatible tasks to them.

A conceptual capability document is:

```json
{
  "executor_id": "codex",
  "capabilities": {
    "filesystem": true,
    "shell": true,
    "mcp": true,
    "browser": false,
    "parallel_workers": true,
    "resume": true,
    "review": true,
    "model_required": true,
    "external_model_provider": true,
    "autonomous_reasoning": true
  }
}
```

A deterministic runner can advertise the opposite reasoning profile:

```json
{
  "executor_id": "wormhole-runner",
  "capabilities": {
    "filesystem": true,
    "shell": true,
    "model_required": false,
    "external_model_provider": false,
    "autonomous_reasoning": false
  }
}
```

A task can declare requirements and model policy separately:

```json
{
  "requires": {
    "filesystem": true,
    "shell": true
  },
  "reasoner_policy": {
    "routing": "wormhole_only",
    "allow_harness_model_override": false
  }
}
```

Wormhole can then select only compatible executors and reject a harness/provider combination that violates the task's reasoner policy.

Capability negotiation is important because coding harnesses, deterministic runners, browser agents, review agents, remote workers, and model providers will not expose identical abilities.

## 14. Multi-executor workflows

Once task ownership and result normalization exist, Wormhole can support more than one executor implementation.

Examples include:

```text
Task A → executor worker 1
Task B → executor worker 2
Task C → review-only agent
```

or sequential specialization:

```text
Implementation executor
        │
        ▼
Independent review executor
        │
        ▼
Fix executor
```

A larger task may also be decomposed into independent sub-tasks with separate claims and leases.

The important constraint is that Wormhole remains the canonical source of task identity, state, and evidence. Executor-specific IDs must never replace Wormhole task or execution IDs.

## 15. Planner portability

The planner side should be replaceable independently from the executor side.

If an external web product supports MCP or another tool integration, its adapter can communicate with Wormhole directly.

If it supports an API but not MCP, a planner adapter can translate between the product API and the Wormhole protocol.

If it supports neither, a lower-quality fallback can still use explicit export/import:

```text
wormhole handoff export
        ↓
structured JSON / Markdown
        ↓
manual or external planner
        ↓
review / decisions
        ↓
wormhole handoff import
```

Direct integration provides the best experience, but the canonical protocol should not assume every reasoning UI has native MCP support.

## 16. Wormhole as durable shared state

Existing Wormhole primitives already provide most of the required lower-level capabilities:

```text
task_context
task_plan
task_state

decision_log

checkpoint
resume

session_report

compose_prompt

memory_context
memory_commit
```

They can be grouped conceptually into three layers.

### 16.1 Context construction

```text
task_context
memory_context
compose_prompt
```

These tools assemble the minimum context required for a specific task rather than replaying an entire conversation history.

### 16.2 Durable task state

```text
task_plan
task_state
decision_log
checkpoint
```

These tools record where the task is, what was decided, what remains, and what must survive planner or executor rotation.

### 16.3 Resume and reporting

```text
resume
session_report
memory_commit
```

These tools allow a new planner session or executor process to reconstruct enough state to continue safely.

The proposed handoff protocol should be implemented on top of these primitives where practical rather than duplicating existing state machinery.

## 17. Higher-level orchestration API

Candidate Wormhole tools are:

```text
handoff_create
handoff_get
handoff_update
handoff_resume

execution_claim
execution_heartbeat
execution_complete
execution_fail

review_submit
```

A normal flow is:

```text
Planner
    │
    ├── handoff_create()
    ▼
READY

Executor
    │
    ├── execution_claim()
    ▼
RUNNING

Executor
    │
    ├── execution_complete()
    ▼
NEEDS_REVIEW

Planner / Reviewer
    │
    ├── approve ───────────────► APPROVED → DONE
    │
    └── request changes ───────► READY
```

These tools are orchestration abstractions, not replacements for the lower-level task, memory, repository, policy, and audit primitives.

## 18. Session and agent rotation

A central benefit is that both planner sessions and executor sessions can be disposable.

```text
Planner session A
  architecture discussion
  ↓
checkpoint / decisions → Wormhole

Executor A
  implementation attempt
  ↓
ExecutionResult → Wormhole

Planner session B
  resume from Wormhole
  review result
  ↓
ReviewDecision → Wormhole

Executor B
  continue from new WorkPacket
```

A new planner session should need only a bounded reconstruction such as:

```text
current task state
relevant decisions
recent evidence
open questions
acceptance criteria
```

A new executor should receive the same neutral `WorkPacket` regardless of which executor handled the previous attempt.

The goal is not to preserve one conversation or one harness session indefinitely. The goal is to preserve the work independently from them.

## 19. Reasoning outcomes, not chain-of-thought transfer

The architecture does not require transferring private chain-of-thought between agents.

What must survive is the outcome of reasoning: decisions, rationale, constraints, and consequences.

For example:

```text
Decision:
Bindings remain process-local.

Why:
Persisting raw binding tokens introduces lifecycle and security complexity.

Consequence:
Clients must select the workspace again after a daemon restart.
```

The durable flow is:

```text
Reasoning → Decision
Decision  → State
State     → Handoff
```

not:

```text
Reasoning → Transcript → Another agent
```

This reduces context size, improves portability, and makes handoffs easier to audit.

## 20. Memory layers

Wormhole memory should be separated by lifetime and purpose.

### 20.1 Task memory

Short-lived state required for the current unit of work:

```text
current plan
current state
open blockers
test failures
files changed
```

### 20.2 Project memory

Long-lived information useful across many tasks:

```text
architecture decisions
repository conventions
known pitfalls
common procedures
important subsystem behavior
```

### 20.3 Execution history

Evidence from prior attempts:

```text
executor
attempt
result
failure
fix
verification
```

Context retrieval should select only memory relevant to the current task and token budget.

## 21. Efficiency model

The architecture is intended to reduce repeated context transfer and unbounded transcript growth.

### 21.1 Compact planner-to-executor handoff

Instead of replaying a large conversation, the planner emits a bounded `WorkPacket` containing only current goals, decisions, constraints, context references, and acceptance criteria.

### 21.2 Targeted executor context

The executor should receive only the files, symbols, memory, and repository evidence relevant to the current task rather than broad project history by default.

### 21.3 Compact executor-to-reviewer handoff

An executor may perform many internal tool calls, but the reviewer normally receives only normalized evidence:

```text
summary
diff / changes
tests
risks
open questions
```

### 21.4 Replaceable contexts

Because canonical state lives in Wormhole, planner sessions and executor sessions can be rotated before their contexts become unnecessarily large.

## 22. Initial deployment model

The first implementation can remain local-first while testing model/backend separation explicitly.

### 22.1 Planner-first layout

```text
ChatGPT Web
    │ MCP
    ▼
Wormhole
    │
    ├── repository tools
    ├── task state
    ├── memory
    └── handoff state

Codex CLI / Harness
    │
    └── MCP / adapter → Wormhole
```

This layout keeps ChatGPT Web as the explicit planner/reviewer and allows Codex to remain a conventional executor.

### 22.2 Codex-UI with Wormhole-routed reasoner

The more interesting experiment keeps the Codex interaction surface and harness but routes inference through Wormhole:

```text
User
  │
  ▼
Codex UI
  │
  ▼
Codex harness
  │ Responses-compatible model provider
  ▼
Wormhole AI Gateway
  │
  ├── reasoner policy
  ├── context/state integration
  ├── provider selection
  └── observability
          │
          ▼
   ReasonerProvider
          │
          ▼
   OpenAI API model
```

The first compatibility target can be `chat-latest` or another Responses-compatible model. This does not forward requests into a ChatGPT Web browser conversation. It uses an API-accessible model backend while preserving Codex as the UI and execution harness.

The experiment must validate the complete agent loop rather than only one completion request:

```text
user prompt
    ↓
model response / tool call
    ↓
local tool execution
    ↓
tool result returned to model
    ↓
subsequent reasoning
    ↓
patch / tests / final response
```

If the selected reasoner does not support a Codex-required Responses feature, the gateway must fail explicitly or negotiate a supported capability rather than silently changing models.

In both layouts, Wormhole remains the common repository confinement, policy, approval, audit, state, and optional model-routing boundary.

## 23. Future deployment models

The same coordination core can support different integration shapes.

### 23.1 Programmatic executor SDK

```text
Planner
   │
   ▼
Wormhole
   │
   ▼
Executor Adapter
   │
   ▼
Harness SDK workers
```

### 23.2 Multiple executor implementations

```text
                  WorkPacket
                      │
                      ▼
               Executor Router
                /      |      \
               /       |       \
        Executor A  Executor B  Executor C
```

### 23.3 Different planner interface

```text
Other Web / IDE / internal UI
            │
      Planner Adapter
            │
            ▼
         Wormhole
            │
      Executor Adapter
            │
            ▼
         Executor
```

### 23.4 Replaceable reasoner provider

```text
Codex UI / other UI
        │
        ▼
Execution Harness
        │
        ▼
Wormhole AI Gateway
   /       |        \
  ▼        ▼         ▼
OpenAI   Provider B  Local model
```

The harness-facing protocol remains stable while Wormhole applies provider policy and normalizes provider-specific differences where feasible.

A future official integration may allow a ChatGPT-hosted workspace/session/agent to participate as a provider. Until such an interface exists, the architecture must distinguish an API model that is also used by ChatGPT from the ChatGPT Web product itself.

### 23.5 No OpenAI dependency

A useful architecture test is whether Wormhole still functions if both reference implementations are removed.

The answer should be yes:

```text
Alternative planner
       │
       ▼
    Wormhole
       │
       ▼
Alternative executor
```

The core coordination and task data model must remain unchanged.

## 24. Vendor-specific metadata

Vendor or harness-specific identifiers may still be useful for resume and diagnostics, but they must remain secondary metadata.

For example:

```json
{
  "adapter_metadata": {
    "codex": {
      "thread_id": "..."
    }
  }
}
```

The canonical identity remains:

```text
Wormhole task_id
Wormhole execution_id
```

Avoid designs where:

```text
conversation ID = task ID
executor thread ID = execution ID
planner message ID = decision ID
vendor prompt = canonical task state
raw executor output = canonical result
```

Those patterns create unnecessary lock-in.

## 25. Design principles

The architecture follows these principles:

1. **Agent context should be disposable.** A task must survive the loss or rotation of any conversation or worker process.
2. **Project state should be durable.** Canonical task progress belongs in Wormhole rather than one agent's prompt history.
3. **Handoffs should be structured.** Agents exchange explicit goals, constraints, decisions, criteria, and evidence.
4. **The protocol must be vendor-neutral.** ChatGPT Web and Codex are reference adapters rather than hard dependencies.
5. **Transport must be replaceable.** MCP is preferred where useful but must not define the canonical data model.
6. **Decisions matter more than transcripts.** Persist reasoning outcomes instead of replaying the complete reasoning process.
7. **Executors need acceptance criteria.** A coding worker should know exactly what successful completion means.
8. **Evidence flows back to the reviewer.** Diffs, tests, failures, and risks are first-class handoff data.
9. **Capabilities are explicit.** Routing should depend on declared executor capabilities rather than assumptions about a harness.
10. **Execution ownership is leased.** Claims and leases make crashes and concurrency recoverable.
11. **UI, harness, and reasoner are independent.** Keeping Codex UI must not require using a Codex-selected model, and changing the reasoner must not require migrating task state.
12. **Model routing is policy-controlled.** A harness must not silently override a task that requires `wormhole_only` reasoner routing.
13. **API model and web product are distinct concepts.** A model used by ChatGPT is not equivalent to an active ChatGPT Web conversation or product session.
14. **Model identity does not define accounting.** `ReasonerProfile.accounting.usage_surface` records the actual inference/accounting path; model aliases must not be used to imply a different quota pool.
15. **Usage-surface policy is enforceable.** Wormhole may restrict eligible reasoners by provider and accounting surface independently from model capability.
16. **Agents should be replaceable independently.** Changing planner, executor, model, UI, or browser session must not destroy task state.
17. **Wormhole remains the policy boundary.** Repository confinement, approvals, auditing, execution policy, and configured reasoner policy apply consistently regardless of the initiating agent.

## 26. Non-goals

This design does not attempt to:

- scrape, automate, or embed a specific web interface as a model backend;
- reuse ChatGPT browser cookies or private/internal ChatGPT endpoints;
- treat an active ChatGPT Web conversation as callable unless an official supported interface exists;
- claim that `chat-latest` or another API model reproduces the full ChatGPT Web product environment;
- relabel or proxy an API/agentic invocation in order to make it appear as regular Chat conversation usage or inherit a different quota pool;
- export private model chain-of-thought;
- use browser transcripts as a durable event store;
- make any executor responsible for canonical project memory;
- make Wormhole a replacement for the reasoning model;
- require every planner, executor, or reasoner to support MCP;
- require all executor implementations to expose identical capabilities;
- require the first implementation to support a distributed job queue;
- automatically replay mutation operations after ambiguous executor failures.

The initial objective is a simple, robust, versioned handoff protocol using the existing local-first Wormhole runtime.

## 27. Recommended implementation order

A practical implementation sequence is:

```text
1. Define versioned WorkPacket, ExecutionResult, and reasoner-policy schemas.
2. Add canonical Wormhole task_id and execution_id identifiers.
3. Add execution claim + lease semantics.
4. Add review decision state transitions.
5. Define ReasonerProvider and Responses-compatible gateway boundaries.
6. Implement Codex as the first ExecutorAdapter.
7. Test Codex with a Wormhole custom model provider against one supported Responses model.
8. Validate the full tool-call loop: prompt → tool → result → reasoning → patch/test → final.
9. Keep ChatGPT Web as the first Planner/Reviewer integration.
10. Add capability negotiation for both executors and reasoners.
11. Enforce reasoner routing policy, including disabling harness model override when requested.
12. Prove executor portability with a second executor adapter.
13. Prove reasoner portability with a second ReasonerProvider.
14. Prove planner portability with a second planner/import adapter.
15. Add routing or parallel execution only after the contracts are stable.
```

This order validates UI/harness/reasoner separation before investing in multi-agent orchestration complexity.

## 28. Summary

The architecture can be summarized as:

```text
             Any User Interface
          ChatGPT / Codex / IDE
                    │
             Planner Adapter
                    │
                    ▼
          ┌────────────────────┐
          │      Wormhole      │
          │                    │
          │ WorkPacket         │
          │ Task State         │
          │ Decisions          │
          │ Memory             │
          │ Claims / Leases    │
          │ Evidence / Reviews │
          │ Reasoner Policy    │
          │ AI Gateway         │
          └──────┬───────┬─────┘
                 │       │
          Executor       Reasoner
           Adapter       Provider
                 │       │
          ┌──────┼───┐   ├── OpenAI API
          ▼      ▼   ▼   ├── Provider B
       Codex  Runner ...  └── future supported provider
```

A particularly useful reference layout is:

```text
Codex UI → Codex harness → Wormhole AI Gateway → selected reasoner
```

This preserves the Codex interaction and execution experience while allowing Wormhole to control which model backend performs inference.

Or in one sentence:

> Wormhole is a vendor-neutral coordination and model-routing plane that separates UI, execution harness, and reasoner; ChatGPT Web, Codex, and OpenAI API models are reference integrations rather than inseparable components.

The long-term goal is not to make one conversation, model provider, or harness session live forever. It is to make the work live longer than any individual conversation, model context, UI, vendor, reasoner, or executor process.