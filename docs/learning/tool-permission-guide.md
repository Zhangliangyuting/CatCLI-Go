# 工具权限与人工确认

每个 `Tool` 的 `Permission` 字段标记默认权限 `Allow`、`Ask` 或 `Deny`。Registry 统一检查，CLI 负责显示本次工具名、操作类型、完整参数和原因，并接收用户决定。参数相关的动态权限可以选配 `Policy`。

```text
LLM 返回 ToolCall
└── Agent 解析参数
    └── Registry.Authorize(ctx, name, args)
        ├── Allow → 返回绑定参数的一次性授权
        ├── Deny → 返回权限错误
        └── Ask → CLI 串行询问
            ├── 同意 → 再查一次硬限制 → 返回授权
            └── 拒绝 / EOF / 取消 → 返回权限错误
    ├── 未获授权 → 不收集证据、不记 ToolCallStarted；错误 ToolMessage 交给 LLM
    └── 已获授权
        ├── AuthorizedCall.PrepareVerification（若工具提供）
        ├── Recorder.StartToolCall（若启用记录）
        └── AuthorizedCall.Execute
            └── 消费授权一次，调用 Handler，不重复询问
                └── Recorder.CompleteToolCall（若启用记录）
```

| 工具或操作 | 当前策略 |
| --- | --- |
| read_file、list_dir | 工作区内读取允许；越界或符号链接逃逸拒绝 |
| write_file、edit_file、create_project | 每次执行先询问；同意后 Handler 检查工作区边界，项目逐一检查文件路径 |
| execute_command | 每次执行先询问；同意后 Handler 仍按现有命令白名单和 shell 语法规则拒绝不允许的命令 |
| MCP 工具（包括 Brave、Fetch） | 询问本次外部调用；ReadOnly 不是免授权标记 |
| 未配置 Permission 的自定义工具 | 默认询问；没有确认器时拒绝 |

恢复 `Verify` 直接调用工具登记的核查器，不执行工具，也不弹出执行权限确认；核查器自身负责输入和资源边界校验。执行前证据收集发生在本次执行已获授权之后，复用这次授权，不再单独确认。

注册工具时通过 Provider 在 `Tool.Permission` 声明静态权限。`RegisterTool(definition, handler)` 没有显式权限字段，因此默认询问。测试或非交互调用方若需要批准操作，应显式通过 `SetPermissionConfirmer` 提供自己的授权决定，不能依赖未配置时自动允许。

`Subset()` 保留工具权限并共享确认器和提示队列；在父 Registry 设置确认器，已创建的子集也生效。确认只针对本次调用，不创建长期授权。拒绝执行通过工具错误反馈给 Agent。复用历史结果没有新外部执行，因此不触发新的写入确认。

CLI 的所有读行使用同一个 TerminalReader。权限提示等待输入时可因 Ctrl-C 或任务超时取消；同步轮询 stdin，不创建遗留的后台读输入协程。EOF、空行或非 y/yes 输入均不批准。CLI 计划确认沿用原有流程。

## 边界

- 权限确认发生在 Handler 和文件锁之前，等待用户时不持有文件锁。
- 用户批准不能替代 `overwrite`、`expected_hash` 等工具自身条件，也不能让 Handler 接受越界路径或白名单外命令。静态 `Ask` 在 Handler 校验之前，因此某些无效输入会先触发一次确认，随后仍被拒绝执行。即使文件已经符合目标，当前文件修改工具的 Execute 仍先询问，之后 Handler 才判断是否跳过写入。
- 命令白名单和确认不是进程沙箱。获准的 `go run`、`go test` 可以执行项目代码；工作目录也不限制子进程能访问的全部文件。
- 文件路径检查不是对外部恶意并发修改的完整防护；已有文件写入的并发边界保持不变。
- 本轮不引入持久授权、会话级全允许、操作系统沙箱或独立审批事件类型。工具开始/结果仍使用现有执行日志。
