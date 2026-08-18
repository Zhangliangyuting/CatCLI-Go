# CatCLI

CatCLI is a tiny Go-based agent command-line app. It talks to an OpenAI-compatible chat completions API, keeps a conversation loop, and lets the model call a small set of local tools.

This project is intentionally minimal. It is a first step from zero to a working agent, designed for incremental learning rather than a fully featured production system.

## Features

- OpenAI-compatible LLM client
- ReAct agent loop with tool calling
- Plan generation and sequential plan execution
- Config-driven provider and tool registration
- Built-in file and command tools
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
> /plan inspect the config package, improve validation, and run tests
```

Useful commands inside the CLI:

- `/plan <task goal>` generates a task plan and executes each task in dependency order
- `clear` clears the conversation history
- `exit` or `quit` exits the program

## Plan Execution

The `/plan` command uses `internal/plan` to ask the model for a structured JSON plan, computes a dependency-safe execution order, and then runs each task through a fresh ReAct agent. Each task receives the overall goal, its description, and the results from completed dependency tasks.

If a task fails, the executor marks both the task and plan as `FAILED` and stops execution. Tasks that have not run remain `PENDING`.

### Execution Flow

```text
/plan <task goal>
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
PlanAndExecuteAgent.Run
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
      "dependencies": []
    },
    {
      "id": "task_2",
      "name": "Analyze the file",
      "description": "Analyze the previously read content",
      "type": "ANALYSIS",
      "dependencies": ["task_1"]
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
- instructions to execute only the current task.

The main implementation files are:

- `internal/plan/plan_generator.go`: requests and parses the structured plan;
- `internal/plan/plan.go`: stores the plan and computes dependency order;
- `internal/plan/task.go`: stores task state, dependencies, results, and errors;
- `internal/agent/plan_execute_agent.go`: executes tasks and passes dependency results;
- `cmd/catcli/main.go`: routes `/plan` commands to the plan executor.

Plan execution is currently sequential even when multiple tasks have no dependencies. It stops at the first failed task and does not currently retry or automatically replan.

## ReAct Loop

The ReAct agent keeps calling the model while the model requests tools. Tool results are appended to the conversation and sent back to the model. The loop exits when the model returns a message without tool calls, or when an LLM request fails.

There is currently no fixed step limit. If the model keeps requesting tools indefinitely, stop the CLI with `Ctrl+C`.

## Built-in Tools

The `builtin` provider currently exposes these tools:

- `list_dir`: list files and directories under a path
- `read_file`: read a file
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
5. Read the planner and executor flow in `internal/plan` and `internal/agent/plan_execute_agent.go`.
6. Add one new tool and wire it into the registry.
7. Improve the system prompt, history handling, or error recovery step by step.

This keeps the codebase small enough to understand while still showing the full path from input to model call to tool execution.

## Project Layout

```text
cmd/catcli/                 CLI entrypoint
config/                     Runtime and example YAML config
internal/agent/             Agent interface, ReAct loop, and plan executor
internal/config/            Viper-based config loading
internal/llm/               OpenAI-compatible chat client
internal/plan/              Plan generation, dependency ordering, execution
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
