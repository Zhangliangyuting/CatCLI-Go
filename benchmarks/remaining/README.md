# Remaining Agent Benchmark Scenarios

This package adds the other seven categories from the draft CatCLI benchmark
list: file creation/overwrite, expected-version edits, partial project creation,
command execution, interrupted-run recovery, cross-turn Fact recall/update, and
permission approval/refusal.

These are opt-in live-agent scenarios. They run the real ReAct agent against a
temporary workspace and use filesystem state, the permission callback, or the
execution log as the verifier. Recovery starts from a persisted `unknown` tool
call and verifies that `Resume` observes the changed current file. The command
scenario exposes only `execute_command` and approves only the exact `pwd`
request.

## Run

The tests use `CATCLI_BENCH_API_KEY`, `CATCLI_BENCH_BASE_URL`, and
`CATCLI_BENCH_MODEL` when set; otherwise they use the corresponding
`CATCLI_OPENAI_COMPATIBLE_*` values from the environment or repository `.env`.
To run all seven categories:

```sh
export CATCLI_RUN_AGENT_BENCHMARK=1
go test ./benchmarks/remaining -run 'Benchmark$' -v
```

To run selected categories, name the relevant test, for example:

```sh
go test ./benchmarks/remaining -run '^TestInterruptedRecoveryBenchmark$' -v
```

Without the opt-in variable, each live test is skipped and ordinary test runs
do not contact the model endpoint.

## Measurements and limits

Each scenario reports observed ReAct tool calls, ReAct token usage, and elapsed
time in `go test -v`; test pass/fail is the independently verified success
signal. The Fact scenario also calls the LLM fact extractor, whose usage is not
included in ReAct token events. These seven are initial representative cases,
not a statistically broad benchmark suite. Conflict handling for every tool
argument permutation and recovery at every process-kill boundary remain covered
by the focused package tests rather than these live model scenarios.
