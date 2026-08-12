# CatCLI

CatCLI is a tiny Go-based agent command-line app. It talks to an OpenAI-compatible chat completions API, keeps a conversation loop, and lets the model call a small set of local tools.

This project is intentionally minimal. It is a first step from zero to a working agent, designed for incremental learning rather than a fully featured production system.

## Features

- OpenAI-compatible LLM client
- Multi-step agent loop with tool calling
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

agent:
  max_steps: 8

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

For a minimal local setup, use `config/config.yaml` for tool and agent defaults, and `.env` for API credentials.

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
```

Useful commands inside the CLI:

- `clear` clears the conversation history
- `exit` or `quit` exits the program

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

1. Understand the chat loop in `internal/agent/agent.go`.
2. Inspect how messages and tool calls are represented in `internal/llm/message.go`.
3. Review tool registration and dispatch in `internal/tool/tool_registry.go`.
4. Add one new tool and wire it into the registry.
5. Improve the system prompt, history handling, or error recovery step by step.

This keeps the codebase small enough to understand while still showing the full path from input to model call to tool execution.

## Project Layout

```text
cmd/catcli/                 CLI entrypoint
config/                     Runtime and example YAML config
internal/agent/             Agent loop and tool-call handling
internal/config/            Viper-based config loading
internal/llm/               OpenAI-compatible chat client
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
