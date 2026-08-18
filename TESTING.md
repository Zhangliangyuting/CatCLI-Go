# CatCLI 测试说明

本文档用于指导 CatCLI，尤其是 `PlanAndExecuteAgent` 的测试。测试的重点不是判断 LLM 是否足够聪明，而是验证项目自身的计划解析、依赖排序、任务执行和状态管理是否正确。

## 测试范围

计划执行功能可以分为三层：

1. `internal/plan`：解析 LLM 返回的 JSON，创建 `Plan` 和 `Task`，计算执行顺序。
2. `internal/agent`：按照执行顺序运行任务，传递依赖任务结果，维护任务和计划状态。
3. `cmd/catcli`：识别 `/plan <任务目标>`，并将请求交给 `PlanAndExecuteAgent`。

前两层应优先使用单元测试，不调用真实 LLM。第三层以及完整调用链可以通过手工端到端测试验证。

## 建议的测试文件

```text
internal/plan/plan_generator_test.go
internal/plan/plan_test.go
internal/agent/plan_execute_agent_test.go
```

测试文件与被测试代码放在同一个包中，便于测试 `parsePlan`、`buildTaskPrompt` 等未导出函数。

## 计划解析测试

### 合法计划

输入一个包含目标、摘要、任务和依赖关系的合法 JSON。

期望结果：

- `parsePlan` 不返回错误。
- Goal 和 Summary 内容正确。
- 所有任务都被创建。
- Task 的名称、描述和类型正确。
- ExecutionOrder 满足依赖顺序。

### 空任务列表

输入：

```json
{
  "goal": "测试空计划",
  "summary": "没有任务",
  "tasks": []
}
```

期望 `parsePlan` 返回 `plan has no tasks` 错误。

### 重复任务 ID

输入两个具有相同原始 ID 的任务。

期望 `parsePlan` 返回 `duplicate raw task id` 错误，避免依赖映射覆盖。

### 缺少任务描述

输入 `description` 为空的任务。

期望 `parsePlan` 返回错误，不创建无法执行的任务。

### 非法任务类型

输入不在以下集合中的类型：

```text
PLANNING
FILE_READ
FILE_WRITE
COMMAND
ANALYSIS
VERIFICATION
```

期望 `parsePlan` 返回 `invalid task type` 错误。

### 未知依赖

让一个任务依赖计划中不存在的任务 ID。

期望 `parsePlan` 返回 `unknown dependency` 错误。

### 自我依赖

让 `task_1` 依赖自身。

期望 `parsePlan` 返回 `depends on itself` 错误。

### 循环依赖

构造以下关系：

```text
task_1 -> 依赖 task_2
task_2 -> 依赖 task_1
```

期望拓扑排序返回 `cycle detected in plan` 错误。

### Markdown JSON 代码块

输入由以下代码块包装的合法 JSON：

````text
```json
{"goal":"测试","tasks":[...]}
```
````

期望 `cleanJSON` 去掉代码块标记，并成功解析 JSON。

## 执行顺序测试

构造三个任务：

```text
task_1：无依赖
task_2：依赖 task_1
task_3：依赖 task_2
```

期望执行顺序为：

```go
[]string{"task_1", "task_2", "task_3"}
```

还应测试多个无依赖任务，确保生成的执行顺序稳定，避免 Go map 的随机遍历顺序造成测试偶发失败。

## PlanAndExecuteAgent 测试

这部分不应调用真实 LLM。测试中使用假的 `PlanGenerator` 和假的执行器。

示例执行器：

```go
type fakeAgent struct {
	result string
	err    error
	input  string
}

func (a *fakeAgent) Run(input string) (string, error) {
	a.input = input
	return a.result, a.err
}
```

示例计划生成器：

```go
type fakePlanGenerator struct {
	plan *plan.Plan
	err  error
}

func (g *fakePlanGenerator) Generate(string) (*plan.Plan, error) {
	return g.plan, g.err
}
```

### 全部任务成功

期望：

- 所有任务依次执行。
- 每个任务最终为 `COMPLETED`。
- 每个任务保存执行结果。
- Plan 最终为 `COMPLETED`。
- `Run` 返回所有任务的结果且 error 为 nil。

### 生成计划失败

让假的 `PlanGenerator` 返回错误。

期望：

- 不创建任务执行器。
- `Run` 返回带有 `generate plan` 上下文的错误。

### 中途任务失败

让第二个任务的执行器返回错误。

期望：

- 第一个任务为 `COMPLETED`。
- 第二个任务为 `FAILED`，并保存错误。
- 后续任务不再执行，或按当前设计标记为 `BLOCKED`。
- Plan 为 `FAILED`。
- `Run` 返回可通过 `errors.Is` 或 `errors.As` 检查的包装错误。

### 每个任务使用独立执行器

记录 executor 工厂函数被调用的次数。

期望执行 N 个任务时，工厂函数也被调用 N 次，避免 ReAct 对话历史污染后续任务。

### 依赖结果进入任务 Prompt

让 `task_1` 返回一个固定结果，并让 `task_2` 依赖 `task_1`。

期望传给 `task_2` 的 Prompt 包含：

- 整体目标。
- 当前任务描述。
- `task_1` 的描述。
- `task_1` 的执行结果。
- 只执行当前任务的约束。

### 无依赖任务 Prompt

对于没有依赖的任务，期望 Prompt 不包含空的“前置任务及其执行结果”段落。

## CLI 手工测试

启动程序：

```bash
go run ./cmd/catcli
```

### 普通 ReAct 请求

输入：

```text
列出当前目录中的文件
```

期望请求进入普通 `ReActAgent`，不会先生成计划。

### Plan 请求

输入：

```text
/plan 阅读 README.md，总结项目功能并运行测试
```

期望：

- 先调用 LLM 生成 JSON 计划。
- 按依赖顺序逐个执行任务。
- 后续任务能够读取依赖任务的结果。
- 最终在终端打印任务结果。

### 空 Plan 请求

输入：

```text
/plan
```

期望终端提示用户输入计划目标，不调用 LLM。

### 清理普通对话历史

先进行几轮普通对话，再输入：

```text
clear
```

期望普通 `ReActAgent` 的对话历史被重置。计划任务当前每次创建新的执行器，因此不依赖该命令清理历史。

## 常用命令

运行全部测试：

```bash
go test ./...
```

显示每个测试名称：

```bash
go test -v ./...
```

只测试计划包：

```bash
go test -v ./internal/plan
```

只测试 Agent 包：

```bash
go test -v ./internal/agent
```

运行静态检查：

```bash
go vet ./...
```

检查测试覆盖率：

```bash
go test -cover ./...
```

生成可在浏览器中查看的覆盖率报告：

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## 完成标准

PlanAndExecuteAgent 可以认为达到基础可用状态，需要满足：

- 所有单元测试稳定通过。
- `go vet ./...` 没有报告问题。
- 非法计划不会进入执行阶段。
- 任务严格按照依赖顺序执行。
- 任务失败会正确更新 Task 和 Plan 状态。
- 失败后不会继续执行依赖该任务的后续任务。
- 每个任务使用独立执行器。
- `/plan` 和普通输入能够正确路由。
- 至少完成一次使用真实 API 的端到端测试。
