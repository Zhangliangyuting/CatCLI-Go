# CatCLI

CatCLI is a tiny Go-based agent command-line app. It talks to an OpenAI-compatible chat completions API, keeps a conversation loop, and lets the model call a small set of local tools.

This project is intentionally minimal. It is a first step from zero to a working agent, designed for incremental learning rather than a fully featured production system.

## Features

- OpenAI-compatible LLM client
- ReAct agent loop with tool calling
- Automatic routing between ReAct and Plan-and-Execute modes
- Plan generation with dependency-aware parallel execution
- Task- and plan-level timeout and cancellation propagation
- Structured execution events for CLI output and observers
- Concurrent in-memory storage with typed entries and token-budget selection
- Scheduler resource conflict detection and per-file read/write locks
- Config-driven provider and tool registration
- Built-in file and command tools with safety checks
- Interactive CLI with conversation history

## Requirements

- Go 1.26.5 or newer
- An API key for an OpenAI-compatible provider

## Configuration

CatCLI reads configuration from three layers:

1. Defaults built into the app
2. `config/config.yaml` for project settings
3. `.env` / environment variables for secrets and local overrides

Create a local YAML config file from the example:

```bash
cp config/config.example.yaml config/config.yaml
```

If you prefer `.env`, copy the example file and set your values there:

```bash
cp .env.example .env
```

Edit `config/config.yaml` for project settings:

```yaml
openai_compatible:
  api_key: your-api-key
  base_url: https://api.deepseek.com
  model: deepseek-v4-pro
  context_window_tokens: 0
  max_output_tokens: 0
  output_reserve_tokens: 32768
  compaction_max_tokens: 4096

agent:
  max_replan_attempts: 3
  max_workers: 3
  task_timeout: 5m
  plan_timeout: 30m

providers:
  enabled:
    - builtin

tools:
  enabled:
    - list_dir
    - read_file
    - edit_file
    - execute_command
    - write_file
    - create_project
```

The app loads `.env` automatically and also supports environment variables using the `CATCLI_` prefix, for example:

```bash
export CATCLI_OPENAI_COMPATIBLE_API_KEY=your-api-key
export CATCLI_OPENAI_COMPATIBLE_BASE_URL=https://api.deepseek.com
export CATCLI_OPENAI_COMPATIBLE_MODEL=deepseek-v4-pro
# Optional: reopen a previously printed conversation ID.
export CATCLI_CONVERSATION_ID=0123456789abcdef0123456789abcdef
export CATCLI_EMBEDDING_MODEL=your-embedding-model
# Optional when embeddings use a different provider:
export CATCLI_EMBEDDING_BASE_URL=https://your-embedding-provider/v1
export CATCLI_EMBEDDING_API_KEY=your-embedding-api-key
```

`context_window_tokens` and `max_output_tokens` use the built-in model table when set to `0`; a positive value overrides the table for the selected provider. `output_reserve_tokens` is used only to calculate the usable memory input budget (`context_window_tokens - output_reserve_tokens`) and is not sent to the API. Normal answers do not set `max_tokens`. `compaction_max_tokens` is sent only for structured Micro, Session, and Full compaction requests. The built-in table currently covers the DeepSeek V4 Flash, V4 Pro, and V4 Flash Vision model IDs.

For a minimal local setup, copy `config/config.example.yaml` to `config/config.yaml` for internal settings and `.env.example` to `.env` for provider settings. The `.env.example` embedding values target the local Ollama container; remove or leave `CATCLI_EMBEDDING_MODEL` unset to use BM25 only. Keep real API credentials in `.env`.

`agent.max_replan_attempts` controls how many replacement plans may be generated after execution failures. Set it to `0` to stop immediately on the first failed plan.

`agent.max_workers` limits how many dependency-ready plan tasks may run concurrently.

`agent.task_timeout` limits one plan task. `agent.plan_timeout` limits the complete approved plan, including replanning attempts. The plan timeout must not be shorter than the task timeout.

### Separate local embedding container

`compose.embedding.yaml` runs Ollama as an independent service, keeps model files in the `ollama-models` volume, and pulls `bge-m3` once the service is healthy. It binds only the host loopback address on port `11435`, leaving a native Ollama installation on `11434` untouched. Start it with:

```bash
docker compose -f compose.embedding.yaml up -d
curl http://localhost:11435/api/tags
```

For a CatCLI process running on the host, set `CATCLI_EMBEDDING_BASE_URL=http://localhost:11435/v1` in `.env`. If CatCLI later runs in the same Compose network, use `http://ollama:11434/v1` instead and remove the published port. Pin `OLLAMA_IMAGE_TAG` to a tested image version for deployments. The container downloads its own copy of `bge-m3`; it does not reuse the model already installed in native Windows Ollama. Docker Desktop's WSL integration must be enabled before running these commands from WSL.

## Run

Start the CLI:

```bash
go run ./cmd/catcli
```

Then type a question or task at the prompt:

```text
> list files in the current directory
> read README.md and summarize it
> create a simple Go hello world project under ./tmp/hello
> explain what the resource tracker does
> /plan inspect the config package, improve validation, and run tests
> /react explain this function without creating a plan
```

Useful commands inside the CLI:

- ordinary input is automatically routed to ReAct or Plan-and-Execute mode
- `/react <task>` forces direct ReAct execution
- `/plan <task goal>` forces plan generation, review, and dependency-aware execution
- `clear` clears the conversation history
- `exit` or `quit` exits the program

The hybrid router first honors `/react` and `/plan`, then applies deterministic rules for clearly simple or complex tasks. Ambiguous input is classified with one LLM request. An unrecognized classifier response safely falls back to ReAct.

## Plan Execution

[![Plan-and-Execute 架构图](docs/architecture/plan-execute%20agent.png)](docs/architecture/plan-execute%20agent.png)

Plan mode uses `internal/plan` to ask the model for a structured JSON plan and computes a dependency-safe execution order. The CLI then displays the plan and lets you execute it, revise it with feedback, or cancel and return to the normal ReAct prompt. An approved plan runs each task through a fresh ReAct agent. Each task receives the overall goal, its description, declared resources, and the results from completed dependency tasks.

If a task fails, the executor marks both the task and plan as `FAILED` and stops execution. Tasks that have not run remain `PENDING`.

### Execution Flow

```text
User input
        |
        v
HybridModeRouter
        |
        +--> ReActAgent for direct execution
        |
        +--> PlanAndExecuteAgent
        |
        v
PlanAndExecuteAgent.Run
        |
        v
LLMPlanGenerator.Generate
        |
        v
Parse JSON into Plan and Tasks
        |
        v
TopologicalSort (dependency-safe order)
        |
        v
PlanReviewer
        |
        +--> execute the current plan
        +--> revise with feedback --> review the replacement plan
        +--> cancel --> return to the ReAct prompt
        |
        v
PlanScheduler.Execute
        |
        +--> create a fresh ReActAgent for task_1
        +--> create a fresh ReActAgent for task_2
        +--> ...
        |
        v
Return the collected task results
```

The planner asks the model to return JSON in this shape:

```json
{
  "goal": "The overall goal",
  "summary": "A short plan summary",
  "tasks": [
    {
      "id": "task_1",
      "name": "A short task name",
      "description": "Specific instructions for the executor",
      "type": "FILE_READ",
      "dependencies": [],
      "read_resources": ["README.md"],
      "write_resources": []
    },
    {
      "id": "task_2",
      "name": "Analyze the file",
      "description": "Analyze the previously read content",
      "type": "ANALYSIS",
      "dependencies": ["task_1"],
      "read_resources": ["README.md"],
      "write_resources": []
    }
  ]
}
```

Supported task types are `PLANNING`, `FILE_READ`, `FILE_WRITE`, `COMMAND`, `ANALYSIS`, and `VERIFICATION`. Task types describe the plan but do not select tools directly; the ReAct agent decides which enabled tools to call from the task prompt.

Before execution, task dependencies are validated and sorted with Kahn's topological-sort algorithm. Unknown dependencies, self-dependencies, duplicate task IDs, invalid task types, and dependency cycles cause plan generation to fail before any task runs.

Each task uses a new ReAct agent so that conversation history from one task does not accidentally leak into another. Required context is passed explicitly through the task prompt:

- the overall plan goal;
- the current task description;
- completed dependency descriptions and results;
- declared read and write resources;
- instructions to execute only the current task.

The main implementation files are:

- `internal/plan/plan_generator.go`: requests and parses the structured plan;
- `internal/plan/plan.go`: stores the plan and computes dependency order;
- `internal/plan/task.go`: stores task state, dependencies, results, and errors;
- `internal/agent/plan_execute_agent.go`: coordinates plan review, revision, cancellation, and execution;
- `internal/agent/plan_scheduler.go`: owns the worker pool, dynamic DAG scheduling, task timeouts, and fail-fast behavior;
- `internal/agent/resource_tracker.go`: prevents tasks with conflicting declared resources from running together;
- `cmd/catcli/main.go`: assembles the router, agents, observer, tools, cancellation, and CLI reviewer.

Plan execution uses a bounded worker pool. Dependency-ready tasks run concurrently up to `agent.max_workers`, and completing a task immediately makes newly unblocked dependents eligible to run. The scheduler also considers declared resources: read/read access may overlap, while read/write and write/write access to the same normalized path are serialized. After a task failure, the scheduler stops dispatching new work, cancels running siblings, waits for dispatched tasks, and may generate a replacement plan up to `agent.max_replan_attempts` times.

## Architecture and Helper Modules

The runtime is divided into three layers:

```text
Entry helpers
    routing / config / CLI
             |
             v
Agent execution
    ReActAgent / PlanAndExecuteAgent
             |
             v
Execution infrastructure
    events / scheduler / resource tracker / tool registry / file locks
```

The main helper modules are:

| Module | Responsibility | Engineering safeguard |
| --- | --- | --- |
| `internal/routing/mode_router.go` | Selects ReAct or Plan mode using explicit commands, rules, and an LLM classifier | Invalid classifier output falls back to ReAct |
| `internal/agent/event.go` | Defines structured Agent, Task, Plan, Tool, and token events | `SynchronizedObserver` serializes events from concurrent workers |
| `internal/agent/plan_scheduler.go` | Runs the bounded worker pool and dynamically releases dependency-ready tasks | Enforces worker limits, task timeouts, cancellation, fail-fast, and worker shutdown |
| `internal/agent/resource_tracker.go` | Tracks resources held by running tasks | Allows read/read concurrency and blocks read/write or write/write conflicts |
| `internal/memory/manager.go` | Stores typed memory entries and selects recent context within a token budget | Uses immutable entry values, defensive metadata copies, unique IDs, and synchronization |
| `internal/tool/tool_registry.go` | Registers enabled tools and dispatches LLM tool calls | Rejects unknown tools and propagates `context.Context` into handlers |
| `internal/tool/file_lock.go` | Maintains one `sync.RWMutex` per normalized file path | Serializes actual file writes without blocking unrelated files |
| `internal/plan/visualizer.go` | Renders plan status, progress, dependencies, and resources | Makes the generated plan inspectable before and during execution |
| `internal/cli/plan_reviewer.go` | Provides a reusable terminal implementation of `PlanReviewer` | Requires an explicit execute, revise, or cancel decision |

These safeguards operate at different boundaries. The scheduler resource tracker prevents known conflicting tasks from starting together. File locks protect actual file-tool operations if a declaration is incomplete. Each plan task receives a fresh ReAct agent so concurrent tasks never share conversation history.

### Lifecycle and Cancellation

The CLI creates a signal-aware context for each run. Cancellation and deadlines propagate through every layer:

```text
Ctrl+C or deadline
        |
        v
CLI context
        |
        v
Router / Agent / PlanScheduler
        |
        v
ReActAgent / LLM client / ToolRegistry
        |
        v
Tool handler and execute_command child process
```

Plan mode adds a timeout for the whole plan and a separate timeout for each task. A failed task cancels running siblings and stops new dispatches. Ordinary execution errors may trigger bounded replanning, while user cancellation and deadline expiration return immediately instead of starting another plan.

### File and Resource Safety

Plan tasks may declare `read_resources` and `write_resources`. The scheduler normalizes these paths and reserves them for the lifetime of each running task. Missing declarations remain valid for backward compatibility, so the file-tool layer provides a second safeguard:

- `read_file` takes a shared read lock;
- `edit_file` locks the complete read-modify-write transaction;
- `write_file` locks the existence-check-and-write transaction;
- `create_project` locks each destination file.

`read_file` only accepts valid UTF-8 text up to 256 KiB. It rejects NUL bytes, invalid UTF-8, terminal control bytes, executables, images, archives, and other detected binary content before that content can reach the terminal or conversation history.

Commands executed through `execute_command` can modify files without going through file-tool locks. Command tasks should therefore declare accurate write resources; stronger isolation would require a separate workspace or Git worktree per task.

## ReAct Loop

The ReAct agent keeps calling the model while the model requests tools. Tool results are appended to the conversation and sent back to the model. The loop exits when the model returns a message without tool calls, or when an LLM request fails.

There is currently no fixed step limit. If the model keeps requesting tools indefinitely, stop the CLI with `Ctrl+C`.

## Memory Manager

`internal/memory` provides the storage layer for context management. An entry has an immutable ID, content, type, UTC timestamp, typed protocol metadata, optional string attributes, and token count. Supported types are `CONVERSATION`, `FACT`, `SUMMARY`, and `TOOL_RESULT`.

The manager is safe for concurrent use, calculates token counts when entries are added, supports loading pre-built entries, type filtering, removal and clearing, and can select the newest complete entries that fit a token budget while returning them in chronological order. Its default token counter is only an estimate; callers can inject a model-specific tokenizer. Conversation entries preserve roles and assistant tool calls, while tool-result entries preserve tool names and tool-call IDs. Protected facts are stored separately from compressible conversation entries and use stable keys for ordered upserts. The CLI selects relevant facts for each request and places them in a system context message.

`ContextBuilder` keeps the two most recent turns as intact protocol messages and retrieves relevant older active turns, summaries, and SESSION, PROJECT, and USER facts from the Manager. SESSION constraints and USER preferences are prioritized within the retrieved-token budget; older turns and summaries are supplied as reference text rather than replayed tool protocol messages. `MemoryRetriever` ranks candidates with BM25 and, when `CATCLI_EMBEDDING_MODEL` is set, embeddings from the configured OpenAI-compatible `/embeddings` endpoint. The embedding model must be served by that endpoint. `CATCLI_EMBEDDING_BASE_URL` and `CATCLI_EMBEDDING_API_KEY` can select a different provider; when the model is unset retrieval uses BM25 only. Retrieval filters out BM25 matches far below the best lexical score and embedding matches below a 0.60 cosine floor or 80% of the best cosine score; these defaults were calibrated with BGE-M3 and may need adjustment for other embedding models. `MaxResults` is only a cap, so fewer results or none may be returned. Retrieved material uses at most one fifth of the usable input budget. The transcript remains an audit log and is not searched for request context.

Facts have `SESSION`, `PROJECT`, and `USER` scopes. Session facts protect constraints for the current task and are removed by `clear`; project and user facts survive ordinary history clearing. `MarkdownFactStore` persists project and user facts in editable Markdown, while session facts are deliberately rejected by the store. Construct a manager with `NewManagerWithFactStore` to load durable facts and keep later upserts/removals synchronized with `.catcli/memory/project.md` and `.catcli/memory/user.md`. `ClearAll` clears the manager's in-memory view but deliberately does not erase those durable files.

The CLI wraps both its main ReAct and Plan agents with `FactAwareAgent`. A local rule gate looks for explicit memory intent such as “remember”, “以后”, “默认”, preferences, or project conventions. Messages that do not match make no extra model request. Matching messages are sent to `LLMFactExtractor` in JSON mode, which proposes validated `UPSERT` or `REMOVE` operations with a scope, stable key, and content. The program applies those operations before running the selected agent and emits a `memory_fact` event. Extraction or persistence failure stops that request, so a model cannot claim it remembered something that was not stored. Plan task agents do not extract facts again; the original Plan request is handled once by the main wrapper.

`JSONLTranscriptStore` appends every immutable protocol entry to one `.jsonl` file per conversation and restores them in order without dropping message roles, assistant tool calls, tool-call IDs, metadata, timestamps, or token counts. Appends are idempotent by entry ID, including after a restart, so a compactor can defensively archive an already-recorded source without duplicating it. A typical root is `.catcli/transcripts`; the conversation ID is restricted to letters, digits, `-`, and `_` so it cannot escape that directory. The transcript store is separate from active context: the transcript remains a complete audit log while the manager keeps the active entries and summaries from which request context is selected.

`JSONConversationStore` provides resumable per-window checkpoints at `.catcli/conversations/<conversationID>/state.json`. The state contains the manager's current conversation entries—including summaries—and SESSION facts. `Manager.SaveConversation` creates an atomic checkpoint; `Manager.LoadConversation` restores it atomically into `entries` and session-scoped `facts` while retaining PROJECT and USER facts loaded through `FactStore`. A missing conversation is reported separately from an existing empty conversation.

Compaction uses two validated content schemas. `ToolResultCompactContent` is the MicroCompact output and is rendered back into a `TOOL_RESULT` entry so its tool-call protocol fields remain intact. `SummaryContent` is shared by Session and Full compaction and always renders fixed sections for the goal, user requirements, decisions, completed work, current state, pending work, important files, errors, fact references, and continuation. `Metadata.Compaction.Kind` distinguishes `MICRO`, `SESSION`, and `FULL` outputs and records their source entry IDs and original token count.

`Compactor` implements all three replacement operations. `MicroCompact` targets one original tool result; `SessionCompact` selects the oldest four complete user turns after the latest summary; `FullCompact` merges the safe historical prefix—including older summaries—while retaining the most recent user turn verbatim. Tool-call groups are protocol-validated before selection. The generated output must reduce token count, all source entries are appended to `TranscriptStore`, and `Manager.ReplaceEntries` then applies the replacement atomically. An archive failure, malformed model response, incomplete tool group, concurrent source change, or non-reducing result leaves active memory unchanged. `LLMCompactionGenerator` supplies production JSON prompts through the existing OpenAI-compatible client.

`CompactionScheduler` applies the token policy incrementally. Below 60% usage it does nothing. From 60% to 75% it MicroCompacts the largest eligible original tool result. From 75% to 85% it runs SessionCompact only when replacing the next four complete turns is estimated to reduce total usage to at most 65%. At 85% it goes directly to FullCompact instead of first producing a Session summary. At 95% the decision is marked emergency; if no historical prefix is safe for FullCompact, the scheduler attempts MicroCompact on the largest eligible tool result. Before one model request, `CompactToFit` performs at most three reducing operations, re-measures after each one, and stops early at the 65% low-water mark. If protected/current context still exceeds the hard input budget, it returns `ContextBudgetExceededError`; the request is not sent and no content is silently truncated.

A structurally valid compaction can still be larger than its source. `Compactor` reports that case as `CompactionNotReducingError` without archiving or changing active memory. The scheduler emits a `SKIP` decision, remembers the rejected ToolResult ID, and tries the next-largest eligible ToolResult. After two ineffective Micro candidates it tries FullCompact; an ineffective FullCompact falls back to remaining Micro candidates. Retries are bounded. If no strategy reduces the complete request but it still fits the hard input budget, the original request proceeds unchanged; only a request that remains over the hard budget ends with `ContextBudgetExceededError`.

`ReActAgent` now stores its short-term conversation through the memory manager and records every user, assistant, and tool message in the transcript. Before every model request it estimates the complete logical input—including the system prompt, facts, conversation entries, tool calls, and tool definitions—and passes that measurement to `CompactToFitMeasured`. The scheduler uses the complete request estimate for thresholds and the hard input limit, re-measuring after every compaction; per-entry token counts remain responsible for candidate selection and replacement checks. After a successful request, the shared `CalibratedRequestTokenEstimator` compares its estimate with the provider's `usage.prompt_tokens` and gradually corrects later estimates while retaining a safety margin. Token events display both `estimated_input` and the actual `input`. A scheduling or hard-budget failure prevents the request from being sent. The CLI saves active state after every turn and prints the generated conversation ID. Set `CATCLI_CONVERSATION_ID` to that ID on a later launch to restore its entries, summaries, and session facts. The system prompt remains agent configuration rather than mutable conversation memory, and `clear` removes all managed conversation entries and checkpoints the cleared state.

`PlanAndExecuteAgent` manages its root conversation directly. Planning receives the root facts, summaries, and conversation history as role-preserving messages, applies the root compaction budget before generation, and writes the current request and final plan result back to the root Manager and transcript. The task-aware factory still gives every task a fresh Manager and its own CompactionScheduler, so concurrently running tasks cannot mix short-term context or mutate the main conversation; each task Manager starts with a snapshot of all SESSION, PROJECT, and USER facts. Task messages use a derived `<conversationID>-task-<sequence>-<taskHash>` transcript ID. Task state remains ephemeral and is not written to the main conversation checkpoint; the combined plan result is recorded in the root conversation.

## Built-in Tools

The `builtin` provider currently exposes these tools:

- `list_dir`: list files and directories under a path
- `read_file`: read a UTF-8 text file up to 256 KiB and reject detected binary content
- `edit_file`: replace an exact string in a file
- `write_file`: write a full file, requiring `overwrite=true` for existing files
- `create_project`: create a project structure from a list of files
- `execute_command`: run a small allowlisted command

`execute_command` is intentionally limited. It rejects shell syntax such as pipes, redirects, command substitution, and command chaining. Allowed commands include:

- `pwd`
- `ls`
- `go test`
- `go run`
- `go fmt`
- `go mod tidy`

## Learning Path

If you want to study how the agent is built, a good progression is:

1. Understand the `Agent` interface in `internal/agent/agent.go`.
2. Follow the ReAct loop in `internal/agent/react_agent.go`.
3. Inspect how messages and tool calls are represented in `internal/llm/message.go`.
4. Review tool registration and dispatch in `internal/tool/tool_registry.go`.
5. Follow event delivery in `internal/agent/event.go`.
6. Read the planner and executor flow in `internal/plan` and `internal/agent/plan_execute_agent.go`.
7. Study dynamic scheduling in `internal/agent/plan_scheduler.go`.
8. Compare task-level resource tracking with tool-level file locking.
9. Follow hybrid mode selection in `internal/routing/mode_router.go`.
10. Add one new tool and wire it into the registry.
11. Improve the system prompt, history handling, or error recovery step by step.

This keeps the codebase small enough to understand while still showing the full path from input to model call to tool execution.

## Project Layout

```text
cmd/catcli/                 CLI entrypoint
config/                     Runtime and example YAML config
internal/agent/             Agent interface, ReAct loop, and plan executor
internal/cli/               Reusable CLI-specific implementations
internal/config/            Viper-based config loading
internal/llm/               OpenAI-compatible chat client
internal/memory/            Typed memory entries, token counting, and in-memory management
internal/plan/              Plan generation, task state, dependency ordering, visualization
internal/routing/           Hybrid ReAct/Plan mode selection
internal/tool/              Tool definitions, handlers, providers, registry
```

## Development

Run all tests:

```bash
go test ./...
```

Format code:

```bash
go fmt ./...
```

Run static checks:

```bash
go vet ./...
```

See [TESTING.md](TESTING.md) for the complete test plan, unit-test cases, and manual CLI checks.
