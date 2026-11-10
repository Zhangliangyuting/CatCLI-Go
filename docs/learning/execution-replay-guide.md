# ReAct / Plan-and-Execute 执行事件、Replay 与 Resume 阅读指南

## 这一版解决什么问题

恢复会话历史，只能知道用户、模型和工具交换了什么消息。执行日志记录 ReAct 或 Plan-and-Execute Run 的开始、工具调用与结束，以及确认计划、Worker 返回和 Reviewer 审查进度。

范围包括执行事实记录、状态重建、Checkpoint 与字节偏移增量读取、用户确认后的 ReAct 恢复，以及 Plan-and-Execute 的计划与任务进度恢复。Plan-and-Execute 对内置 write_file、edit_file、create_project，以及 filesystem MCP 的 write_file、create_directory 支持后置条件核查；读取类重新获取当前观察，通用命令不猜测执行结果。用户选择继续后，无法确认的 Unknown 调用记录为 retry_allowed，由新 Worker 根据当前现场重新尝试，不提供人工核查回调。相同任务中已确认成功的继承调用可以复用结果。暂不实现无人确认自动续跑、跨进程执行权抢占或上下文压缩的跨文件提交 Checkpoint。恢复不等于精确恢复中断时的程序指令位置，也不保证任意外部副作用只执行一次。

## 阅读顺序

```text
internal/plan/task_definition.go / task.go
    共用的任务定义与持有定义的运行时 Task；执行状态不放进定义
        ↓
internal/execution/event.go
    定义事件字段、类型和校验
        ↓
internal/execution/state.go
    定义 Run、Task、Call 的状态，以及状态转换规则
        ↓
internal/execution/persistence.go
    定义 EventStore 存储接口
        ↓
internal/execution/jsonl_store.go
    追加、Sync、有序加载、处理不完整尾行
        ↓
internal/execution/replay.go
    Replay(events) 全量重放；ReplayFrom(checkpoint, events) 从快照增量重放
        ↓
internal/execution/checkpoint.go
    复用 RunState 保存快照，校验日志身份和字节边界，增量读取与无效快照回退
        ↓
internal/execution/recorder.go
    Recorder 管理 Run 生命周期；RunRecorder 记录工具调用和结束状态
        ↓
internal/execution/recovery.go
    StartRecoveredRun 接收已重放状态，继承计划、任务进度和工具调用状态
        ↓
internal/agent/multiagent/execution_recording.go
    将计划、任务与工具调用绑定到同一 Run，转换确认计划与恢复计划
        ↓
internal/agent/multiagent/plan_execute_agent.go / task_executor.go / task_scheduler.go
    保存确认计划，记录 Worker 和 Reviewer，按恢复状态跳过已完成任务
        ↓
internal/agent/multiagent/resume.go
    校验中断计划，核查未知调用或记录允许重试，恢复确认计划与任务调度
        ↓
internal/agent/react_agent.go
    持有 Recorder，显式 StartRun + defer FinishRun，直接执行工具并记录前后事件
        ↓
cmd/catcli/execution_log.go
    loadExecutionLog 加载并重放一次；promptInterruptedRun 使用得到的 runs 询问继续 / 放弃
        ↓
internal/agent/resume.go
    Resume 校验原 Run，整理恢复提示，准备独立上下文并复用 Run
        ↓
cmd/catcli/main.go
    装配 Store、Agent、终端输入和恢复回调，保存恢复后的会话
```

逐个文件阅读时可以点击：

1. [task_definition.go](../../internal/plan/task_definition.go)：共用的任务描述；[event.go](../../internal/execution/event.go) 统一定义事件类型、载荷和字段校验；[state.go](../../internal/execution/state.go) 统一定义 Run、Task、Call 的状态和状态转换规则。
2. [persistence.go](../../internal/execution/persistence.go)：追加与加载接口。
3. [jsonl_store.go](../../internal/execution/jsonl_store.go)：文件持久化和位置缓存。
4. [replay.go](../../internal/execution/replay.go)：将事件还原为 `[]RunState`；[checkpoint.go](../../internal/execution/checkpoint.go)：状态快照、字节偏移和增量恢复。
5. [recorder.go](../../internal/execution/recorder.go)：通用 Run / 工具调用记录；[workflow_recorder.go](../../internal/execution/workflow_recorder.go) 提供计划、任务与核查入口，均不依赖 Agent 或 LLM。
6. [react_agent.go](../../internal/agent/react_agent.go)：通过 WithRecorder 注入记录器；正常执行循环，以及恢复时使用原任务进行检索。
7. [execution_log.go（CLI）](../../cmd/catcli/execution_log.go)：只检查最后一个 Run，仅当它未结束时询问继续 / 放弃。
8. [resume.go](../../internal/agent/resume.go)：继续执行的完整入口。
9. [main.go](../../cmd/catcli/main.go)：启动和恢复交互的装配位置。

最核心的职责划分是：Recorder 记录执行事件，Store 负责落盘，Replay 还原状态，CLI 接收用户选择并按执行类型分派；两个 Agent 的 Resume 分别准备恢复上下文或恢复计划调度。

### Recorder 为什么放在 execution 包

```text
ReActAgent / PlanAndExecuteAgent
    ↓ 持有并调用
Recorder：一个会话共享，持有 EventStore
    ↓ StartRun
RunRecorder：一次执行专用，记录状态与调用身份
    ↓
EventStore.Append：可靠落盘
```

恢复的 Load + Replay 统一在 CLI 启动的 loadExecutionLog 中完成一次。用户确认后，Agent.Resume 直接接收已重放的 RunState，检查会话、状态和执行类型，不再加载或重放日志。StartRecoveredRun 接收这份状态，记录开始并继承进度；核查调用直接更新句柄中的状态。Recorder.LoadEvents 仍可用于独立读取或诊断，不参与 Resume。

通用入口包括：

- `Recorder.StartRun()`：保存开始事件并返回本次 Run 的记录句柄。
- `Recorder.StartRecoveredRun()`：按旧 Run 的权威事件状态创建恢复 Run，返回记录句柄和继承后的状态。
- `RunRecorder.StartToolCall()` / `CompleteToolCall()`：记录工具调用前后事件。
- `RunRecorder.FinishRun()`：记录成功、失败或取消；拒绝把仍有待完成调用或最新计划任务未完成的 Run 记为成功。
- `RunRecorder.SavePlan()` / `RecordTask()`：记录确认计划和任务进度。
- `RunRecorder.ReconcileToolCall()`：保存已核查的未知调用结论。
- `RunRecorder.State()`：返回当前内存状态的独立副本，不读日志、不重放。

Agent.Run 自己控制主流程：准备输入与恢复来源，调用 StartRun，然后直接执行原有循环，并通过 defer FinishRun 统一收尾及合并错误。Recorder 不再接收执行 Agent 主流程的回调；ID 生成和取消后保存结果仍由 Recorder 负责。

RunRecorder 通过互斥锁保护记录状态，但执行工具时不持锁，并行 Worker 共享同一个 Run 句柄，各自携带 TaskReference。发生持久化错误后，句柄保留错误并阻止继续调度工具或报告成功；Plan-and-Execute 也会停止重规划，而不是把磁盘错误交给 Planner 尝试修复。

## recorder.go 分段阅读

### 1. Recorder：会话级记录器

先看 `Recorder`、`NewRecorder()` 和 `LoadEvents()`：

```text
Recorder
├── store：持久化接口
├── conversationID：事件所属会话
├── startMu：串行化开始与恢复创建
└── resumed：本 Recorder 已交接的来源 Run ID，防止重复创建
```

它不持有 Agent。`main.go` 创建共享 Recorder，注入两个 Agent，并将启动时重放、经用户确认的 RunState 交给对应 Resume；Recorder 负责记录事件和维护本次执行的内存状态。

### 2. StartRun()：为一次执行创建句柄

`StartRun(ctx, input, resumedFrom, kind)` 必须显式传入 `ReActRun` 或 `PlanAndExecuteRun`，不再默认使用 ReAct。它生成 RunID，保存 `run_started`，成功后返回 `*RunRecorder`。失败就返回错误，不能开始本轮模型或工具执行。

Recorder 可以用于多轮执行；每轮都有自己的 RunRecorder：

```text
同一个 Recorder
├── RunRecorder A：RunID A、待返回调用、写入错误、结束标记
└── RunRecorder B：RunID B、待返回调用、写入错误、结束标记
```

`pending` 表记录已成功写入开始事件、尚未成功写入结果的调用。`writeErr` 保留第一次持久化错误；`finished` 阻止结束后继续写事件。

### 3. Agent 中的 defer：统一收尾

Agent 使用命名返回值保存回答及错误，显式传入 kind 创建 Run，并在入口注册 defer。主流程直接调用自身的执行方法，不再传给 Recorder：

```text
StartRun()
    ↓ 成功
注册 defer：执行结束后调用 FinishRun，并合并存储错误
    ↓
Agent 直接执行自己的主流程
    ↓ 正常返回
FinishRun(answer, error)
```

主流程正常返回后将 returned 标记设为 true，defer 才调用 FinishRun；主流程 panic 时不标记结束，也不吞掉 panic。FinishRun 根据错误决定写 Completed、Failed 或 Cancelled。如果先前已经写入失败，则直接返回保留的错误，不尝试补写结束事件。命名返回值允许 defer 将存储错误合并到最终返回错误中。

### 4. 工具执行：Agent 直接调用工具并记录前后事件

```text
StartToolCall()：生成 CallID，开始事件落盘，加入 pending
    ↓
Agent 解析 Arguments，直接调用自己的 registry.Execute()
    ↓
CompleteToolCall()：记录返回内容与错误，成功后移出 pending
```

Recorder 只负责 StartToolCall 和 CompleteToolCall。ReAct 在自身循环内直接执行 `a.tools.Execute()`。多 Agent 的 `startToolCallRecording()` 只组装调用数据、绑定任务、查询复用结果并记录开始；`completeToolCallRecording()` 只记录返回结果和包装记录错误。SubAgent.executeTool 和 WorkerSubAgent.executeWorkerTool 自己解析参数、判断是否使用已有结果，并直接调用 `agent.tools.Execute()`，不把实际执行交给记录辅助函数。复用命中时跳过实际工具执行，仍记录开始、完成及 ReusedFrom。工具执行错误会变为 `ERROR: ...` 结果交给模型；事件持久化错误则直接向上返回，中止执行。实际执行期间不持有 Recorder 的锁；工具 panic 时不记录完成，调用保持 Unknown。

### 5. ReAct 如何接入

在 [react_agent.go](../../internal/agent/react_agent.go) 找这三处：

```text
WithRecorder()：设置 a.recorder
    ↓
Run()：StartRun() → 注册 defer FinishRun() → 直接调用 a.run()
    ↓
推理循环：StartToolCall() → a.tools.Execute() → CompleteToolCall()
```

Agent 持有会话级 Recorder，每次 Run 的句柄通过参数传入私有 `run()`。它不是在 Agent 外面再套一个 LoggingAgent，持久化本身也不由 Observer 的展示回调承担。

## 事件和身份

事件结构拆为公共头和五种载荷：

```text
Event
├── EventHeader：序号、会话 ID、Run ID、类型、时间
├── Run *RunEventData：执行类型、输入、回答、错误、原 Run 引用（继续时）
├── ToolCall *ToolCallEventData：调用身份、工具名、参数、结果、错误、可选任务引用
├── Plan *PlanEventData：确认后的完整计划及版本
├── Task *TaskEventData：计划版本、任务 ID、尝试次数、结果与错误
└── Reconciliation *ToolCallReconciliation：原 Run、原调用、核查结论与依据
```

五种载荷必须且只能填写其中一个，并与事件类型匹配。公共头使用 Go 嵌入字段，JSON 中仍位于顶层；具体数据分别放入 `run`、`tool_call`、`plan`、`task` 或 `reconciliation` 对象。nil 载荷不会写入 JSON。

一个 Run 对应一次 ReAct 或 Plan-and-Execute 执行，Worker 和 Reviewer 不额外创建子 Run。`run_started` 中的 `kind` 区分 react / plan_and_execute，规划草稿尚未确认时也能恢复到正确入口。旧日志没有 kind 时默认按 ReAct 处理，已有确认计划时按 Plan 处理。`conversation_id` 关联会话；`run_id` 区分每轮执行；`call_id` 是本地执行身份；`tool_call_id` 保留模型的协议调用 ID。

`sequence` 是一个会话日志内连续递增的序号，由 Store 分配；同时由 Store 填写 UTC 时间。Worker 的工具调用可以额外携带 `TaskReference`：计划版本、任务 ID 和尝试次数。ReAct 原有调用可以省略它。

```text
run_started                 用户输入
tool_call_started           工具名、原始参数、两种调用 ID
tool_call_completed         工具返回内容、错误（如果有）
run_completed               最终回答
run_failed / run_cancelled  正常返回的错误 / 取消
run_abandoned              用户明确放弃中断任务
```

`tool_call_completed` 表示 Handler 已返回且结果已记录，可能返回错误，不表示业务一定成功，更不能保证没有副作用。`run_cancelled` 也不保证外部 MCP 服务已停止执行。

## 计划、任务与核查事件

任务定义统一位于 `plan.TaskDefinition`，包含 ID、名称、描述、TaskType、依赖和读写资源。运行时 `plan.Task` 持有它，再单独维护状态、结果、错误和执行时间；日志中的 `PlanEventData.Tasks` 和 `TaskState.Definition` 使用同一个定义类型。Planner 的 JSON 解析也复用该类型。

```text
plan.TaskDefinition
├── plan.Task 持有 → 定义 + 运行状态
└── execution.PlanEventData.Tasks 使用 → 确认计划的任务描述
```

确认计划时调用 `task.Definition()`，恢复时调用 `plan.NewTaskFromDefinition()`。两处都复制依赖和资源切片，防止保存的定义与运行时相互修改。`PlanEventData` 仍独立保留计划版本等日志字段，任务进度继续由任务事件记录。

```text
RunRecorder
├── SavePlan(ctx, PlanEventData)
│      ├── 只保存确认后的完整计划，不记录 Planner 草稿
│      ├── 同一个 Run 内 PlanID 不变，Revision 从 1 连续递增
│      ├── 校验任务 ID、依赖存在性、重复依赖和循环依赖
│      └── plan_saved 落盘成功后，调用方才能调度新版本
│
├── RecordTask(ctx, EventType, TaskEventData)
│      ├── task_started：依赖已完成，开始下一次 Attempt
│      ├── task_work_completed：Worker 返回，保存结果，等待审查
│      ├── task_completed：审查通过，任务完成
│      ├── task_failed：执行或审查失败
│      └── task_cancelled：活动任务被明确取消
│
└── ReconcileToolCall(ctx, ToolCallReconciliation)
       ├── 校验当前句柄中已有的未知调用及来源
       ├── 保存核查事件后更新内存，不读取或重放日志
       ├── completed：核查确认已完成，Call 状态 reconciled
       ├── not_executed：核查确认未执行，Call 状态 not_executed
       ├── unknown：核查仍不确定，保持 unknown，允许再次核查
       └── retry_allowed：旧结果仍不确定，但用户已选择继续，允许新的尝试，不复用旧结果
```

任务状态由事件推导：计划中的任务初始为 `pending`；开始而未返回为 `interrupted`；Worker 返回后为 `awaiting_review`；审查通过为 `completed`。失败或取消后允许增加 Attempt 重试，但同一任务旧尝试仍有未知调用时必须先核查。

`RunState.Plan` 只保留当前确认计划，`RunState.Tasks` 只保留当前版本的任务状态。新计划保存成功后直接替换这两部分，任务初始化为 pending；不保留旧任务的 superseded 状态。旧计划和执行结果仍完整保留在事件日志中，Replay 处理到新版本时同样替换内存状态。

生成草稿时旧计划仍有效，只有新版本确认且落盘成功后才替换。替换前，必须先结束或取消当前活动任务、处理待审查结果，并解决当前 Run 的未知工具调用。版本号继续保留，任务事件只能更新当前版本，避免迟到的旧版本结果污染新计划。确认新版本前需要调用方协调 Worker 停止与收尾。

### 继承原计划的进度

继续原计划时，Resume 使用 CLI 启动时 Load + Replay 得到的 RunState，调用 `StartRecoveredRun(ctx, source)`，不重复读取日志。不要再次用 SavePlan 把旧计划当作新版本保存。实现位于 [recovery.go](../../internal/execution/recovery.go)。

```text
PlanAndExecuteAgent.Resume
├── 接收 CLI 已重放并经用户确认的旧 RunState，校验会话、状态和类型
├── 核查 Unknown：内容匹配则复用；无法确认则按用户继续意图记录 retry_allowed
└── Recorder.StartRecoveredRun(ctx, source)
    ├── 检查传入状态的会话、身份、中断状态和确认计划
    ├── 深拷贝计划、任务进度和工具调用状态
    ├── 保存 run_started：kind、resumed_from、input
    └── 返回新 RunRecorder 和继承状态，不读取或重放日志；随后保存核查事件再恢复调度
```

例如旧计划中 A 已完成、B 等待审查、C 尚未开始，恢复 Run 保留 A 的结果、B 的 Worker 输出和待审查状态，以及 C 的 pending 状态。新 Run 中 B 审查通过，不会把旧 Run 中 B 的历史进度也改成完成。返回给调用方的状态与 Recorder 内部状态也相互隔离。

日志用一条 run_started 的 ResumedFrom 关联旧 Run。以后重放读到这条记录时，若源 Run 有确认计划，就直接继承它在此时的状态；没有确认计划则开始新的规划流程。不再设置 RecoveryPoint 或额外的恢复序号。Replay 仍校验来源为中断状态，并校验继承计划时的目标和执行类型。

继承的工具调用保留 `OriginRunID`、CallID 和原状态。未知调用仍为 unknown；不能在新 Run 伪造一次 tool_call_completed 来把它标记为已返回。核查事件会同步同一恢复链上继承的调用状态，但不会自动完成任务。Recorder 也不会因为自身没有新启动的工具，就忽略继承的未知调用并报告成功。

同一个 Recorder 内会拒绝对同一来源重复创建恢复 Run。重启后由 CLI 通过 LoadRuns 获取完整重放或检查点增量重放的最新状态，不能将上次进程的旧快照直接用于新 Recorder。Resume 信任调用方提供的已重放快照；从启动读取到确认恢复之间不能有其他执行器写入该会话，旧执行器必须已经停止。当前不支持多进程写同一会话，也不是跨进程抢占或 Worker fencing 机制。

restorePlan(run) 和调度器通过 RunRecorder.State() 读取内存状态，继续恢复调度。启动展示、用户确认和 Resume 共用同一次 Load + Replay 的结果，Resume 和 StartRecoveredRun 都不会再次读取事件日志。Resume 在创建恢复 Run 前收集未知调用的核查或重试决策，在新 Run 持久化后，再读取最新内存状态交给调度流程。

核查事件不会自动读取文件、运行验证工具或调用模型。调用方先检查外部状态，再提供结论和 `Evidence`；非空依据本身不代表结论必然正确。不能将“LLM 认为已经执行”当作可靠核查。该事件不伪造原工具的返回记录，也不自动触发重试。

核查事件的语义是：有依据确认调用工作已满足后，写入引用旧 Run 和 CallID 的核查事件，Replay 将调用还原为 `reconciled`，不会自动完成任务。无关 Run、缺失调用和已返回结果不能被核查事件覆盖。CLI 不提供人工填写结论的入口。

### 内置 write_file 自动核查

```text
Plan-and-Execute Resume 收到 Unknown 调用
├── 解析原调用参数，按注册工具查询 ToolRegistry.Verify
│      └── 内置 write_file 的 Verify → BuiltinProvider.verifyWriteFile
│             ├── 校验 file_path、content，并通过 Workspace.Resolve 检查边界
│             ├── 获取与内置读写工具共用的文件读锁
│             ├── 读取普通文件，比较完整内容（包括空内容）
│             ├── 缺失 / 不匹配 → 无法确认，不写文件，不推断未执行
│             └── 匹配 → 返回路径、字节数、SHA-256 证据与核查结果
├── 核查满足目标 → 决策 completed，可复用结果
├── 不支持 / 无法确认 / 读取失败 / 原参数无效 → 决策 retry_allowed，允许新 Worker 尝试
└── 创建恢复 Run → ReconcileToolCall 落盘 → 恢复调度；取消或持久化失败仍停止
```

核查能力作为 Tool 的可选 Verify 字段绑定在注册项上，RegisterProvider、RegisterEnabledTools 和 Subset 均保留它。只通过 RegisterTool 注册的同名自定义处理器不会自动得到内置核查能力；MCP 工具也不会因与内置工具同名而获得内置核查能力。核查器只读文件，不调用实际写入 Handler，也不调用 LLM。

`mcp__filesystem__write_file` 使用独立的核查器：仅当同一 MCP 服务端同时启用了 `read_text_file` 时，恢复流程才调用它读取原路径的完整文本，并与写入请求的 `content` 精确比较。匹配时保存当前内容的哈希与核查结论；缺失、MCP 读取失败或内容不匹配时保留不确定性，让 Worker 决定下一次尝试。该核查只绑定到注册名为 `filesystem` 的 MCP 服务端的 `write_file`，不会调用其写入工具，也不会借用本地内置 `write_file` 的文件权限或路径规则。

`mcp__filesystem__create_directory` 仅在同一服务端同时启用 `get_file_info` 时核查。恢复时读取原路径的文件信息，明确得到 `isDirectory: true` 且 `isFile: false` 才确认目录已存在；缺失、类型不符或无法读取时保留不确定性，让 Worker 继续处理。MCP `edit_file` 暂不接自动核查；Unknown 时由恢复后的 Worker 查看当前文件，决定是否重新调用工具。

内置 `read_file`、`list_dir` 和 filesystem MCP 的 `read_text_file`、`list_directory`、`get_file_info` 是现场观察。恢复后 Worker 再次请求这些工具时，旧 Run 即使已记录成功结果，也不复用；新调用重新读取当前状态。旧日志中已记录的 `ReusedFrom` 仍可重放。

MCP `brave_web_search` 和 `fetch` 同样不复用旧查询结果；恢复后 Worker 再次请求时重新查询。MCP `shell_execute` 和 `edit_file` 没有统一后置条件核查器，Unknown 保持不确定并交给恢复后的 Worker 检查现场、决定下一步。

其余内置工具按下面的规则处理：

| 工具 | Verify 的处理 | 恢复决策 |
| --- | --- | --- |
| edit_file | 调用开始前准备修改前后完整文件哈希，恢复时比较预期后哈希，并核对路径和参数身份 | 完整匹配才复用；无证据、缺失或不匹配则新尝试 |
| create_project | 逐一比较所有请求文件的完整内容，检查相对路径、工作区边界及符号链接；部分存在不能算完成 | 全部匹配才复用，否则 Worker 修复剩余文件 |
| read_file / list_dir | 返回 Retry=true，要求重新读取，不在核查器中执行读取 Handler | retry_allowed，新调用获取当前结果 |
| execute_command | 返回 Retry=true，说明任意命令没有统一后置条件；核查器不执行命令 | retry_allowed，Worker 通过正常权限流程决定新命令 |

`Tool.PrepareVerification(ctx, args)` 是可选的执行前取证方法，目前只有 edit_file 使用。Agent 在 tool_call_started 前准备证据，将其作为 `ToolCallEventData.VerificationData` 字符串持久化；Recorder 和 Replay 保留在 `CallState.VerificationData` 中，Checkpoint 也随状态保存。恢复时传给 `ToolRegistry.Verify(ctx, name, args, data)`。实际工具执行仍由 Agent 自己调用，取证方法不包裹执行。

edit_file 的证据包含解析后的路径、规范化参数哈希、修改前后 SHA-256 和替换次数，不保存额外的文件正文。修改计算与实际 Handler 共用字符串替换规则，包括 replace_all、删除和新文本包含旧文本的情况。旧日志没有该字段时向后兼容，按未知结果允许新尝试；证据读取失败不阻止正常工具执行，之后无法确认时同样重试。取证读取限制为 16 MiB，超限时不生成证据。取证和执行不是跨进程事务；若文件之后改变而不满足预期完整哈希，就不会复用。项目核查是逐文件观察，不提供跨文件事务。

这里使用明确的后置条件策略：当前完整内容等于请求内容，视为 write_file 的目标已经满足，可记录 completed 核查结论并复用结果；这不证明旧调用历史上成功执行过。Evidence 和 Result 明确区分“当前核查结果”与“旧工具返回”，不伪造 tool_call_completed。文件不匹配或不存在不能证明未执行，不会标为 not_executed；用户选择继续后，记录 retry_allowed 决策，让 Worker 根据当前现场重新调用工具，使用新的 CallID 和正常权限流程。旧调用没有成功结果，不能被复用。

核查记录写入失败时不派发 Worker，新恢复 Run 保持中断和未解决调用，供下次恢复。文件锁只协调本进程内置工具；核查结果是读取时的观察，不保证文件之后不会被其他进程修改。当前自动核查只接入 Plan-and-Execute，ReAct 的模型引导恢复保持原流程。

### Plan-and-Execute 已接入的调用链

Context 通过 `runRecorderKey{}` 直接保存共享的 `*RunRecorder`，使用处直接通过 `ctx.Value(runRecorderKey{}).(*executionlog.RunRecorder)` 的逗号 ok 形式取出；未配置时得到 nil，不再包装 `planRecording`，也不另存 planID、revision、restored、tasks 或 calls。计划接入统一由 `execution_recording.go` 完成：`recordPlan(ctx, run, p, replanned)` 读取当前状态、决定是否保存及版本号、组装 PlanEventData 并调用 SavePlan；`restorePlan(run)` 读取最新 State 并重建调度计划及任务进度。Agent 不再分别调用纯转换函数和 Recorder 方法。调度器仍在每轮派发 Worker 前读取一次内存状态快照，从中取得版本、Attempt 和历史调用，不读取事件文件或再次 Replay。

```text
PlanAndExecuteAgent.Run
├── StartRun(kind=plan_and_execute)，由 Agent defer FinishRun
├── Planner.Generate / Revise → 用户确认
│      └── 只有选择执行后才 SavePlan；草稿不落为确认计划
├── TaskScheduler 分配任务与 TaskReference
│      └── TaskExecutor.Execute
│             ├── task_started
│             ├── Worker：工具开始 / 返回事件（phase=worker）
│             ├── task_work_completed：保存 Worker 输出
│             ├── Reviewer：工具开始 / 返回事件（phase=reviewer）
│             └── task_completed / task_failed / task_cancelled
├── 执行失败可重规划 → 保存下一版本后再执行
└── run_completed / run_failed / run_cancelled
```

[multiagent/resume.go](../../internal/agent/multiagent/resume.go) 的流程是：

```text
Resume(ctx, selectedRun)
├── 接收选中的 RunState：校验会话、中断状态和执行类型，不重新 Load / Replay
├── 核查 Unknown：按实际工具绑定的核查器检查后置条件或要求新的尝试
│      └── 无法确认 → 保存 retry_allowed 决策，继续任务，不伪装成旧调用已成功或未执行
├── 创建独立规划上下文、新调度器和执行器
├── 尚无确认计划 → 新的关联 Run，重新生成草稿并等待执行确认
└── 已有确认计划 → StartRecoveredRun
       ├── 继承原计划和任务进度
       ├── 保存核查及重试决策，更新内存状态后再调度
       ├── completed → 保留结果，跳过 Worker 和 Reviewer
       ├── awaiting_review → 只恢复 Reviewer，复用 Worker 输出
       ├── interrupted → 关闭旧尝试，新 Attempt 创建新 Worker
       └── pending / failed / cancelled → 按依赖安排后续尝试
```

普通任务和恢复任务共用资源冲突检测、并发上限、任务超时和 Reviewer 流程。恢复时完整保留依赖结果；Worker 的记忆命名包含 Run、计划版本、任务和尝试身份，避免重启后与旧任务记录混淆。中断 Worker 的历史调用作为有长度上限的参考信息交给新 Worker；不复用旧的 goroutine 或不完整工具消息上下文。恢复说明明确任务目标仍有效，无法确认的是具体工具调用；Worker 优先读取或查询现场，目标已满足则跳过，未满足则重新选择补齐方法。retry_allowed 不要求机械重放旧调用；无法核实时，仅在重复操作没有不希望的额外影响或具备幂等性时直接重试，否则继续寻找核实、修复办法或报告不确定性。

[tool_reuse.go](../../internal/execution/tool_reuse.go) 支持恢复期间的有限结果复用：同一任务、同一计划版本、相同工具名和规范化 JSON 参数，且原调用成功返回或经核查确认完成。新日志中的 `reused_from` 指向旧 CallID，Replay 校验复用结果与来源一致。未知、确认未执行、返回错误和不同参数均不会命中；大整数参数使用 UseNumber 保留身份精度。这个机制不覆盖不同计划版本或任意业务幂等性。

Worker 对 Reviewer 只读工具集中的工具仍重新执行，以便检查当前现场；Reviewer 自身所有工具也重新执行。工具是否只读继续由已有的 Reviewer 工具选择配置决定，不由模型自行声明。

当前已移除 RecoveryResolver、相关注入选项和 CLI 人工填写核查结论的实现。Unknown 通过工具绑定的核查器检查；无法确认或核查器要求重新读取时，基于用户已经选择继续的意图记录 retry_allowed 后继续。Worker 恢复提示说明旧调用仍不确定、允许新尝试；这不证明副作用从未发生，重复副作用仍有可能。Checkpoint 只加速还原，不会自动消除不确定性。恢复 API 的调用方应在用户确认继续后才调用 Resume。

## 写入顺序

```text
持久化 run_started
    ↓
调用 LLM，获得工具调用
    ↓
持久化 tool_call_started 成功
    ↓
执行 Handler（内置工具或 MCP 适配器）
    ↓
持久化 tool_call_completed 成功
    ↓
保存工具消息，继续模型或下一个工具调用
    ↓
持久化 Run 结束事件
```

开始记录失败，不执行工具。结果记录失败，停止当前 Run，不继续后续工具或模型请求，也不追加“Run 完成”事件。写入失败可能意味着磁盘状态不确定，因此不会在同一 Run 内自动重试写入。

任务取消后，已经返回的工具结果仍使用独立的短时写入上下文保存。若主流程发生 panic，Agent 的 defer 不写结束事件；进程直接退出时 defer 也不会运行。

## JSONL 文件

```text
.catcli/
├── transcripts/<conversation_id>.jsonl  消息归档
├── conversations/<conversation_id>/state.json  会话快照
└── execution/
    ├── <conversation_id>.jsonl                 执行事件
    └── <conversation_id>.jsonl.checkpoint.json  执行状态快照和字节位置
```

Store 每次追加一个完整 JSON + 换行，调用文件 `Sync()` 后才报告成功。没有换行的最后一条记录视为未提交：Load 忽略，下一次 Append 截去该尾段再写入。带换行但内容损坏、会话不符或序号不连续的记录会报错，不能静默跳过。

每条记录最多 16 MiB。一个 Store 实例内通过互斥锁保证并发追加有序；**不支持多个 CLI 进程或多个 Store 实例同时写同一个会话日志**。

Store 为每个会话缓存最后序号和有效字节位置：首次追加优先校验快照并扫描其后缀，没有有效快照则扫描完整日志；启动时的 LoadRuns 也会建立缓存，后续 Append 可直接追加。只有 Write 和 Sync 都成功后才更新位置；修改文件前先使旧缓存失效，写入或刷盘失败后，下次追加会重新定位。文件大小变化或文件被替换时也会重新定位，但这不能替代多进程写锁，也不会在每次追加时校验历史内容是否被原地等长修改。

文件 Sync 不等于会话快照、Transcript 和执行事件的跨文件原子事务，也不承诺任意掉电条件下目录元数据的持久性。

## Replay 如何解释事件

```text
有 run_started，缺少 Run 结束事件 → Run 状态 interrupted
有 tool_call_started，缺少完成事件 → Call 状态 unknown
有完整工具结果记录               → Call 状态 returned
有核查且确认执行完成             → Call 状态 reconciled
有核查且确认未执行               → Call 状态 not_executed
核查仍无法确定                   → Call 状态保持 unknown
用户选择继续、允许新的尝试       → Call 状态 retry_allowed，旧结果仍不确定
```

`unknown` 不能当作“尚未执行”。例如文件已写入，但结果事件尚未落盘就退出，重放只能知道结果不确定。

Replay 输入完整的单会话有序日志，输出按出现顺序排列的 Run 状态，不访问文件系统、执行工具或调用 LLM。ReplayFrom 输入 Checkpoint 和紧接它的日志后缀，要求序号从 LastSequence + 1 连续递增，并先深拷贝快照，避免成功或失败时修改传入状态。两者共用同一个事件转换函数，拒绝重复开始、无开始的结果、重复结果、结束后的事件等非法转换。日志后缀应传给 ReplayFrom，不能单独传给 Replay。

## Checkpoint 与字节偏移增量读取

只新增 Checkpoint 这一种数据结构，直接复用全部 RunState，不另建 Task 或 Call 快照类型：

```text
Checkpoint
├── ConversationID / LastSequence  会话身份、已应用的最后事件序号
├── ByteOffset                    已提交日志前缀的结束字节位置
├── Runs []RunState               全部 Run，保留恢复祖先和继承调用关系
└── LogID / BoundaryHash / Checksum
       日志文件身份、边界事件哈希、快照内容校验和
```

保存时机与顺序：

```text
Store.Append：Write + Sync 成功
    ↓ 每满 50 条已持久化事件
持有 Store 锁，从上一有效快照及已落盘后缀重建精确状态
    ↓
保存对应的 LastSequence、ByteOffset 和日志身份 / 边界校验信息
    ↓
临时文件（0600）→ Write → Sync → Close → Rename → 同步目录
```

普通退出由 main.go 的 defer 额外调用 SaveCheckpoint，使用独立的 5 秒上下文。强制终止时不依赖 defer，下一次从上个快照继续读取事件。检查点保留旧的完整快照直至原子替换成功；孤立临时文件不参与加载。自动保存为尽力而为，失败时不保留错误，也不把已经落盘的 Append 改成失败；显式调用 SaveCheckpoint 时直接返回保存错误。保存是同步的，期间会占用 Store 锁；未引入后台 Worker 或新的 Manager。

加载路径：

```text
loadExecutionLog → Store.LoadRuns()
    ├── 验证快照内容、会话、恢复祖先、日志文件身份、大小和边界事件
    │      └── Seek(ByteOffset) → 读取 N + 1 以后的事件 → ReplayFrom()
    └── 快照缺失 / 损坏 / 偏移不符 / 日志替换或截短
           └── 从字节 0 完整读取 → Replay()
```

快照中的事件前缀和状态来自同一次加锁读取，不使用尚未完成状态更新的 RunRecorder.state。尾部没有换行的半条事件仍被忽略，下一次追加时裁掉；已提交的损坏后缀仍然报错。LoadRuns 校验成功后同步建立追加位置缓存，因此重启后追加也不必再次扫描旧前缀。

正常恢复统一使用 `LoadRuns()`，`readLogLocked()` 不再接收 allowCheckpoint 或 minimum：有有效快照就从 ByteOffset 增量读取，没有或校验失败才从头读取；有效快照后的事件损坏则直接报错。`Load(ctx, id, afterSequence)` 仅用于原始事件读取或诊断，独立全量读取再按序号筛选，不参与快照选择。检查点既是状态快照，也是一个稀疏的“序号 → 字节偏移”索引，不单独维护索引文件。

快路径假设已提交日志前缀只追加、不原地修改。当前文件身份使用 Unix 的设备号 / inode，边界校验只读取快照前的最后一条事件，不重新扫描整个前缀；因此不用于检测任意早期记录的原地篡改。需要完整审计时使用 Load(..., 0) + Replay。日志不裁剪；快照保存全部 Run，随着会话历史增长也会变大。Checkpoint 不提供跨文件业务事务、自动副作用核查或工具执行 exactly-once 保证。

启动 CLI 恢复旧会话时会重建状态，只检查按开始事件顺序排列的最后一个 Run。仅当它的状态为 `Interrupted` 时询问继续 / 放弃；最后一个 Run 已结束时，不回头寻找更早的中断 Run。提示类似：

```text
发现上次未完成的任务（run=...）：
修改 main.go 并运行测试
已记录调用：2，结果不确定：1
是否继续这个任务？[y=继续 / N=放弃]：
```

选择 n 或直接回车时，追加 `run_abandoned`，Replay 将该 Run 标记为 Abandoned，之后不再提示它。EOF 或读取错误不算放弃，不修改旧日志。每次启动只询问一轮，不逐个调用确认。

选择 y 后，通过 `ReActAgent.Resume()` 把原任务、已返回结果及 unknown 调用整理为有长度上限的参考数据，要求模型先核实当前状态。历史参数和结果不是新的指令；工具结果会截断并标注，原始日志仍完整保留。不会直接执行原来的任何调用。无法核实外部写操作时，提示模型向用户确认而不是盲目重试；现有工具权限保持生效，这不是新的强制副作用去重机制。

继续执行使用独立的模型消息上下文，并复制当前 Facts，避免不完整旧工具消息导致协议错误。新 Run 的 `run_started.run.resumed_from` 指向旧 Run，Replay 原子地将旧 Run 视为 Resumed；新 Run 如果再次崩溃，就提示它，不再重复提示原 Run。如果新开始事件写入失败，旧 Run 仍为 Interrupted。

恢复成功后，将任务和最终回答写回根会话。若根会话快照中存在缺失工具结果的协议消息，会插入明确标注“执行结果不确定”的占位说明以修复配对；这些说明不会变成工具执行完成事件，原始执行事实不变。取消、失败、放弃和已经被后续 Run 接续的旧 Run 不会作为中断任务再次提示。

相关入口：`cmd/catcli/execution_log.go` 的 `promptInterruptedRun()`、`internal/agent/resume.go` 的 `Resume()`。

## Resume 的完整调用链

```text
main.go：加载同一个 Conversation
    ↓
loadExecutionLog()：创建 Store，LoadRuns（快照 + 后缀，或全量回退），返回 Store 和 runs
    ↓
promptInterruptedRun(runs)：使用已还原状态，只检查 runs 的最后一个 Run 是否为 Interrupted
    ├── 没有中断 Run → 正常进入输入循环
    ├── n / 回车 → Append(run_abandoned) → 正常进入输入循环
    └── y → 调用 main.go 传入的恢复回调
                ↓
        runWithInterrupt()：支持 Ctrl+C
                ↓
        ReActAgent.Resume(ctx, selectedRun)
                ↓
        检查传入的 RunState 属于当前会话，且为 Interrupted、类型匹配
                ↓
        recoveryPrompt()：原任务 + 调用记录 + 不确定性说明
                ↓
        新 Manager 复制 Facts；浅拷贝 Agent 并替换上下文
                ↓
        ctx 携带 resumeRequest：原 RunID、原任务输入
                ↓
        continued.Run(ctx, prompt)
                ↓
        Agent 显式调用 Recorder.StartRun()，并注册 defer FinishRun()
        新建 Run B，写 run_started，resumed_from 指向原 Run A
                ↓
        使用正常 ReAct 循环，直接执行工具并记录 StartToolCall / CompleteToolCall
        让模型检查现状并继续
                ↓
        RunRecorder.FinishRun() 保存本轮结束状态
                ↓ 成功返回
        修复根会话缺失的工具消息配对，写回任务和最终回答
                ↓
        main.go：memoryRuntime.Save() → 输出回答 → 正常输入循环
```

恢复回调或会话保存出错时，`main.go` 打印 `resume error` 并结束启动，不会继续输出成功回答。选择继续不经过模式路由器：`resumeExecution` 按已记录的 kind / 确认计划选择原来的 ReAct 或 Plan-and-Execute 入口。明确记录为 run_cancelled 的正常取消属于终态；自动提示针对没有结束事件的 interrupted Run。

启动加载、询问和 Resume 共用同一次 Load + Replay 的结果。用户选择继续后，将该 RunState 直接交给对应 Agent.Resume；Resume 只检查传入状态，不再加载日志。调用方应保证确认期间没有其他执行器写入同一会话。

## resume.go 分段阅读

### 1. Resume()：确认要继续哪个 Run

直接使用 CLI 已重放的 RunState，检查 Recorder 已配置、会话归属、中断状态及执行类型；不再调用 LoadEvents 或 Replay。同一 Recorder 的 resumed 标记防止同一来源重复接续；跨重启时必须重新经过启动加载，不能传入过期快照。

此时尚未调用模型，也未执行工具。

### 2. recoveryPrompt()：把状态整理成模型参考数据

模型接收的是选中 Run 的恢复提示，不是整个 Conversation 的 `runs` 数组。参考数据包括：

```text
source_run_id：原 RunID
original_task：用户原任务
recorded_calls：工具名、参数、returned / unknown、已记录结果和错误
omitted_calls：因长度限制省略的调用数
```

历史数据字段总计最多取 24,000 个 Unicode 字符，最多保留 32 次调用；单个任务输入最多取 8,000 字符、参数 2,000、结果 4,000、错误 1,000。截断会附加说明，固定指令和 JSON 包装不计入这 24,000 字符，因此它不是精确的模型 Token 上限。最终请求仍经过 ReAct 的 Token 预算检查。

提示词要求模型先核实当前环境。`unknown` 不能解释为“工具尚未执行”，`returned` 也不能直接解释为“业务成功”。原始事件文件不会因为提示词截断而删减。

### 3. 新 Manager 与 continued：隔离恢复上下文

```go
manager := memory.NewManager(nil)
for _, fact := range a.memoryManager.Facts() {
    // 将当前 Facts 复制到新 Manager
}
continued := *a
continued.memoryManager = manager
continued.contextBuilder = memory.NewContextBuilder(...)
```

`continued := *a` 是浅拷贝：创建一个新的 Agent 结构体，指针字段仍指向原对象。随后主动替换 Manager 和 ContextBuilder，所以模型客户端、工具、RAG 对象和 Recorder 继续共享，上下文记忆独立。新调用 StartRun 时仍会获得独立 RunRecorder，不会复用原 Run 的记录句柄。

它不是新增一种 Resume Agent，而是复用同一个 ReAct 实现。旧聊天记录不直接复制到恢复上下文，避免把中断时没有配对工具结果的 assistant 消息带进新的模型请求；需要的执行证据由恢复提示提供。

### 4. resumeRequest：把原 Run 关联到新 Run

```go
ctx = context.WithValue(ctx, resumeContextKey{},
    resumeRequest{run.RunID, run.Input})
```

这是 Go 调用链内部的数据，不会因为放入 ctx 就自动发送给模型：

- `react_agent.go` 的 `Run()` 读取它，将原任务与来源 ID 交给 Recorder；Recorder 写入新开始事件的 Input 和 ResumedFrom。
- `react_agent.go` 使用原任务进行 RAG 和记忆检索，避免将整段历史工具结果当成检索 query。
- 模型实际收到的是 `continued.Run(ctx, prompt)` 中存入的新用户消息。

### 5. 为什么原 Run 是 Resumed，而新 Run 是 Completed

```text
Run A：开始 → 工具调用 → 中断
    ↓ 用户选择继续
Run B：run_started，resumed_from=A
    ↓
模型检查并完成任务
    ↓
A：Resumed，表示已交给后续 Run 接续
B：Completed，表示这次继续执行成功结束
```

Resumed 不表示任务成功。B 尚未结束时，A 也已是 Resumed；如果 B 崩溃，下次提示的是 B。如果写入 B 的开始事件失败，就没有可靠的接续关系，A 仍是 Interrupted。

Replay 处理 B 的开始事件时，同时更新 A 的状态，因此不需要先单独写一条“A 已恢复”的事件。这避免了写完旧状态、尚未创建新 Run 就崩溃而失去待恢复入口的间隙。

### 6. repairPendingToolMessages()：恢复成功后的会话整理

恢复的详细工具过程已写进 Transcript 和执行事件。Resume 成功返回后，会将原任务和最终回答加入根会话，供后续对话使用。

若原会话快照中存在 assistant 工具调用但缺少工具结果，`repairPendingToolMessages()` 会补入明确标注“执行结果不确定”的协议占位消息，保留原有历史和 Facts。这是为了让聊天协议消息配对，不是伪造工具成功；它不会写 `tool_call_completed`，不会改变原调用在执行日志中的 unknown 状态，也不会执行原工具。

## 建议的第二遍阅读路线

第一遍理解事件结构和各文件职责。第二遍沿着一个具体例子走：

```text
原任务：读取 main.go，然后修改文件
原事件：run_started → 读取开始 → 读取完成 → 修改开始 → 中断

1. Replay 得到原 Run：Interrupted，读取 Returned，修改 Unknown。
2. CLI 提示一次，用户选择 y。
3. Resume 使用已重放状态；若修改是内置 write_file，检查当前文件完整内容，无法确认则允许新尝试。
4. 创建独立上下文和新 Run，ResumedFrom 指向原 Run，并保存核查或 retry_allowed 决策。
5. 新 Worker 根据历史参考完成剩余工作；同任务同参数的已核查写入可复用，任务仍需正常完成与审查。
6. 新调用继续由 Store 记录，最终新 Run Completed。
7. 根会话保存最终回答，下次启动不再提示这两个已结束的执行。
```

看完后应能说明：Replay 为什么不直接调用模型，Resume 为什么使用新上下文，Resumed 与 Completed 为什么不同，以及为什么这套机制尚不能保证工具副作用只执行一次。

## 验证

```bash
go test ./internal/execution ./internal/agent ./cmd/catcli -count=1
go test -race ./internal/execution ./internal/agent ./cmd/catcli -count=1
```

当前保留的测试覆盖日志重新打开、不完整尾行恢复、已提交记录损坏拒绝、位置缓存、并发序号、非法重放、通用记录器错误处理及继续／放弃交互。模型使用本地测试替身，不调用外部 LLM 或 MCP 服务。

真实子进程崩溃测试使用 SIGKILL，覆盖下面 10 个窗口。父进程通过新的 Store 和 CLI 加载入口恢复，并与完整日志重放结果比较。

| 强制终止窗口 | 恢复后应保持的结果 |
| --- | --- |
| 工具开始事件写入前 | 任务已开始，尚无调用记录 |
| 工具开始事件写入后 | 调用为 Unknown，尚无文件副作用 |
| 文件副作用完成、结果事件写入前 | 文件已修改，重放后调用仍为 Unknown；重放本身不执行写入 |
| 工具结果写入后 | 调用为 Returned，任务仍未结束 |
| Worker 结果写入后 | 任务等待审核 |
| 任务完成事件写入后 | 任务完成，Run 尚未结束 |
| Run 完成事件写入后 | Run 为 Completed |
| 检查点临时文件创建后 | 使用旧检查点和后续事件 |
| 检查点临时文件同步后、替换前 | 使用旧检查点和后续事件，容忍遗留临时文件 |
| 检查点发布完成后 | 使用新检查点 |

```bash
go test ./cmd/catcli -run 'Test(ExecutionRecoveryAfterSIGKILL|CheckpointRecoveryAfterSIGKILL)$' -count=5
go test -race ./cmd/catcli -run 'Test(ExecutionRecoveryAfterSIGKILL|CheckpointRecoveryAfterSIGKILL)$' -count=1
```

测试在明确的执行边界等待后杀死子进程；检查点窗口利用现有 Context 检查定位，没有增加生产故障注入接口。它验证进程被杀后的本地文件恢复，不模拟断电、系统调用执行到一半的中断、真实 MCP 外部副作用或尚未实现的上下文压缩提交事务。

执行日志和恢复相关测试按下面的顺序读：

- [cmd/catcli/execution_crash_test.go](../../cmd/catcli/execution_crash_test.go)：上述 10 个真实 SIGKILL 窗口、重新打开恢复、保留未知调用事实和检查点发布前后的一致性；核查与新尝试另由 multiagent 集成测试覆盖。
- [internal/execution/state_test.go](../../internal/execution/state_test.go)：计划和任务落盘重开、版本替换、依赖与尝试校验、审查边界、祖先调用核查以及计划写入失败。
- [internal/execution/checkpoint_test.go](../../internal/execution/checkpoint_test.go)：50 条触发、字节定位和游标、快照损坏回退、日志替换 / 截短、半行尾部、跨检查点恢复链及核查、输入隔离、并发保存、写入失败和权限。
- [internal/execution/recovery_test.go](../../internal/execution/recovery_test.go)：重新打开日志后继承进度、连续两次恢复、未知调用核查同步、恢复引用校验、开始记录失败和同一 Recorder 的重复恢复竞争。
- [internal/agent/multiagent/execution_integration_test.go](../../internal/agent/multiagent/execution_integration_test.go)：确认计划、自动重规划、并行角色身份、跳过已完成任务、只恢复审查、草稿恢复、未知写调用核查与结果复用，以及两种角色的持久化失败。使用本地 HTTP 模型替身和带 MCP 名称的测试工具，不连接真实模型或 MCP 服务。
- [internal/tool/write_file_verification_test.go](../../internal/tool/write_file_verification_test.go)：内置写入后置条件核查、空内容、缺失与不匹配、路径边界与符号链接、取消、注册与 Subset 保留核查器，以及同名自定义工具不冒用核查能力。
- [internal/tool/verification_test.go](../../internal/tool/verification_test.go)：编辑取证与完整哈希比较、替换与删除、旧日志及无效依据、项目全部或部分完成、路径边界，以及读取和命令核查不会执行 Handler。multiagent 集成测试另覆盖真实内置工具执行成功但结果记录失败后，经 Checkpoint 恢复并复用或重新读取。
- [cmd/catcli/execution_routing_test.go](../../cmd/catcli/execution_routing_test.go)：旧日志、ReAct、规划草稿与确认计划的恢复路由。

- [cmd/catcli/execution_log_test.go](../../cmd/catcli/execution_log_test.go)：只检查最后一个 Run、最后一个已结束时不提示更早中断 Run、继续回调、放弃落盘、EOF 不修改历史。
- [internal/agent/resume_test.go](../../internal/agent/resume_test.go)：新旧 Run 关联、拒绝重复恢复、开始事件写入失败保留原 Run、恢复提示截断、根会话保留。
- [internal/execution/replay_test.go](../../internal/execution/replay_test.go)：Abandoned 终态与非法事件转换。
- [internal/execution/recorder_test.go](../../internal/execution/recorder_test.go)：多个并行执行者共享记录句柄、写入失败后停止调用、拒绝重复完成和结束后调用。
- [internal/agent/execution_helpers_test.go](../../internal/agent/execution_helpers_test.go)：Resume 测试共用的模型替身、工具调用构造和失败 Store；这是辅助定义，不是生产代码，也不单独运行测试。
