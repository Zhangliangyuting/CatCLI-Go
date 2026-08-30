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
```

For a minimal local setup, use `config/config.yaml` for provider and tool settings, and `.env` for API credentials.

`agent.max_replan_attempts` controls how many replacement plans may be generated after execution failures. Set it to `0` to stop immediately on the first failed plan.

`agent.max_workers` limits how many dependency-ready plan tasks may run concurrently.

`agent.task_timeout` limits one plan task. `agent.plan_timeout` limits the complete approved plan, including replanning attempts. The plan timeout must not be shorter than the task timeout.

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
