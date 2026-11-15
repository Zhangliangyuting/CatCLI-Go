# Read-only Agent Benchmark

This opt-in benchmark runs the real ReAct agent against deterministic temporary
workspaces. It covers single-file lookup, multi-file summaries, directory
inventory, abstention when information is absent, and explicit conflicting
values. The registry passed to the agent contains only tools explicitly marked
read-only.

## Run

The benchmark uses CatCLI's `config/config.yaml` and `.env` model settings by
default. To override them just for the benchmark, set:

```sh
export CATCLI_RUN_AGENT_BENCHMARK=1
export CATCLI_BENCH_API_KEY=...
export CATCLI_BENCH_BASE_URL=https://your-openai-compatible-endpoint/v1
export CATCLI_BENCH_MODEL=your-model
```

Then enable and run the live benchmark:

```sh
export CATCLI_RUN_AGENT_BENCHMARK=1
go test ./benchmarks/readonly -run '^TestReadOnlyAgentBenchmark$' -v
```

Without `CATCLI_RUN_AGENT_BENCHMARK=1`, the live benchmark is skipped, so the
normal test suite does not make API calls or incur model charges.

## Scoring

The verifier checks required and forbidden answer facts, explicit abstention in
the missing-information case, observed tool names, and a before/after snapshot
of the workspace. It reports pass rate, tool-call count, token count reported by
the model endpoint, and elapsed time.

The verifier is intentionally deterministic and independent of the model, but
its answer checks are string-based. This first version measures a narrow set of
observable behaviors; it does not claim full semantic entailment checking, and
reported token totals do not include monetary cost.
