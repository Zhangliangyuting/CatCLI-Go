# MCP 接入与阅读顺序

CatCLI 作为 MCP Client 连接外部 Server，将发现的工具注册到现有 ToolRegistry。
ReAct Agent 和 Plan-and-Execute 的 Worker 共用这份工具集合；Reviewer 优先使用 `filesystem` Server 的只读目录和文本读取工具，缺失时按操作回退到内置只读工具。

推荐按下面的顺序读，先理解一条调用链，再研究并发和故障处理：

```text
1. Config / ServerConfig
      ↓
2. Message / Transport
      ↓
3. StdioTransport
      ↓
4. JSONRPCClient
      ↓
5. Client：握手、发现、调用
      ↓
6. StreamableHTTPTransport
      ↓
7. ToolDescriptor → tool.Tool
      ↓
8. ServerManager
      ↓
9. main.go 装配与 Agent 接入
```

整体组件关系：

```text
main.go
└── ServerManager：并行连接 Server，发现并注册工具，退出时关闭连接
    ├── LoadConfig：读取 JSON 配置、展开环境变量
    ├── Server：保存 Client 和发现的工具
    │   └── Client：initialize → initialized → tools/list → tools/call
    │       └── JSONRPCClient：请求 ID 匹配、并发调用、超时取消、反向 ping
    │           └── Transport
    │               ├── StdioTransport：子进程 stdin/stdout
    │               └── StreamableHTTPTransport：HTTP POST + JSON/SSE
    └── ToolDescriptor → tool.Tool → ToolRegistry → Agent
```

## 启用

1. 复制 `config/mcp.example.json` 为 `config/mcp.json`。
2. 修改需要连接的 Server，删除它的 `disabled: true` 或设置为 `false`。
3. 示例已提供固定版本的 Filesystem 和 Shell Server；HTTP 示例需要填写真实 endpoint。`tools` 填写该 Server 实际提供的工具名称。
4. 启动：

```bash
CATCLI_MCP_CONFIG_FILE=config/mcp.json go run ./cmd/catcli
```

也可以在 `config/config.yaml` 中填写：

```yaml
mcp:
  config_file: config/mcp.json
```

未设置配置路径时，MCP 不启动。配置路径相对于工作区根目录；stdio 的 `cwd` 相对于 MCP JSON 文件所在目录，省略时继承 CatCLI 当前目录。

配置中的 `${VARIABLE}` 在 JSON 解码后展开，支持命令、参数、工作目录、环境变量和 HTTP Header。变量必须已存在于环境中（CLI 会先加载 `.env`）；禁用的 Server 不展开变量。`config/mcp.json` 已加入 Git 忽略规则。

### 与内置工具对应的 Server

[Filesystem MCP](https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem) 使用 `npx -y @modelcontextprotocol/server-filesystem@2.1.0 .` 启动，需要 Node.js 24 或以上；[Shell MCP](https://github.com/tumf/mcp-shell-server) 使用 `uvx mcp-shell-server==1.1.14` 启动，需要 uv 和 Python 3.11 或以上。它们都是本地 stdio 子进程，不需要 API Key。

| 内置工具 | Server | 对应的原始 MCP 工具名 |
| --- | --- | --- |
| `list_dir` | filesystem | `list_directory` |
| `read_file` | filesystem | `read_text_file` |
| `edit_file` | filesystem | `edit_file` |
| `write_file` | filesystem | `write_file` |
| `create_project` | filesystem | 组合调用 `create_directory` 和 `write_file`，没有相同的单个工具 |
| `execute_command` | shell | `shell_execute`，当前仅允许 `pwd`、`ls` |

两个 stdio 配置的 `cwd: ".."` 都指向 JSON 文件上一级目录，即当前仓库根目录。Filesystem 的参数 `.` 在这个工作目录中解析，将文件访问范围限制在仓库内；Shell 的 `cwd` 只指定启动目录，不限制命令访问其他路径。Shell 的程序名白名单也不等于内置工具的参数校验，因此 Go 命令继续使用内置 `execute_command`，不要为了测试而直接放开所有命令。

注册后名称为 `mcp__filesystem__read_text_file`、`mcp__shell__shell_execute` 等。内置工具继续保留，MCP 配置不会替换它们。参数要遵循外部 Server 的 schema，例如 Filesystem 写文件使用 `path`，编辑使用 `edits: [{oldText, newText}]`；其 `write_file` 会覆盖已有文件，没有内置工具的 `overwrite` 开关。

初次启动 `npx` / `uvx` 会下载固定版本包，后续复用本地缓存。示例为冷启动保留了 `120s` 超时；该值也适用于单次 RPC，Shell 内部另外将命令执行时间限制为默认 10 秒、最多 30 秒。

`tools` 省略时暴露 Server 的全部工具，`[]` 表示不暴露工具；填写数组则仅注册指定的原始工具名，找不到指定工具时该 Server 启动失败。`tools.enabled` 仍只控制原有 Provider 的工具。外部 MCP 工具的权限由对应 Server 决定，不受内置文件工具的 Workspace 路径校验约束。

启动输出示例：

```text
MCP: connecting configured servers...
MCP local: ready, tools=1
MCP remote: unavailable (MCP HTTP status 503)
```

最多同时连接 4 个 Server，单个失败不会阻止其他 Server 和 Agent 启动。每个 Server 的 `timeout` 默认 `30s`，限制其完整握手与工具发现阶段，也限制后续单次 RPC 请求。启动时 Ctrl+C 取消连接并退出。

## 工具如何进入 Agent

```text
Server 返回工具：search
配置中的 Server 名称：local
         ↓
注册工具名：mcp__local__search
         ↓
LLM 选择这个工具，传入 arguments
         ↓
Client.CallTool("search", arguments)
         ↓
Server 返回 content / structuredContent / isError
         ↓
保留为 JSON 工具结果，交给 Agent 继续处理
```

不合法、含有歧义分隔符或过长的名称会附加稳定哈希，保持模型接口所需的字符集和 64 字节长度限制。工具的文本、图片、资源等 content 块会保留；`isError=true` 会作为工具执行错误反馈。MCP 调用沿用现有 Observer 的 `tool call` / `tool result` 事件，Debug 模式会显示参数和结果。

## 协议范围

- 握手优先使用 `2025-11-25`，也接受 `2025-06-18`、`2025-03-26`；不声明未实现的 sampling、roots 等客户端能力。
- stdio 使用逐行 JSON；子进程 stderr 单独转发到终端。退出时关闭 stdin，超时后终止并回收直接子进程。
- Streamable HTTP 支持 JSON 与 POST SSE 响应，包括流中的通知和 Server 请求；携带协商后的协议版本及会话 ID，退出时尝试 DELETE 会话。
- 超时会尽力发送取消通知，但无法保证 Server 已停止有副作用的操作。工具调用不会自动重试。
- HTTP 会话返回 404 时，本次调用报错；下一次显式调用重新握手，不重放上一次操作。
- 当前没有后台 GET 订阅、SSE 断线续传、OAuth 授权流程、旧版 HTTP+SSE 双端点或运行时工具列表热更新。HTTP 可通过配置 Header 使用已有令牌；工具列表变化后需重启 CatCLI。

## 源码阅读

### 第一部分：先理解配置里的一台 Server

先看：

- [mcp.example.json](../../config/mcp.example.json)
- [config.go](../../internal/mcp/config.go)

重点看：

```go
type Config struct
type ServerConfig struct
func LoadConfig(...)
```

这里的一条 Server 配置，表示 CatCLI 要连接的一个外部 MCP 服务：

```text
mcpServers
    ├── local：启动本地子进程，通过 stdin/stdout 通信
    └── remote：连接已有服务的 HTTP endpoint
```

先搞懂四个问题：

- `transport` 怎么决定使用哪种连接方式？
- `command`、`args`、`cwd` 分别传给谁？
- `${VARIABLE}` 从哪里取值，为什么先解码 JSON 再展开？
- `tools` 省略、为空数组、填写名称，分别是什么意思？

第一遍先看结构体和 `LoadConfig()` 主流程，最后再研究 `validate()` 中的具体限制。

### 第二部分：理解消息和传输接口

先看：

- [transport.go](../../internal/mcp/transport.go)

重点看：

```go
type Message struct
type RPCError struct
type Transport interface
```

`Message` 是通信双方收发的 JSON-RPC 消息。以调用一个工具为例：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {"name": "search", "arguments": {"query": "workspace"}}
}
```

先理解这些字段：

```text
ID       将响应对应到原请求
Method   请求或通知的方法名
Params   请求或通知的参数
Result   成功响应的结果
Error    JSON-RPC 错误响应
```

再看接口的三个方法：

```text
Start(receive, failed) → 安装收消息和连接失败的回调
Send(ctx, message)    → 发送消息
Close()              → 释放连接或子进程
```

注意 `Send()` 返回的是发送操作的错误，不是工具执行结果。响应会通过 `receive` 回调交给上层处理。

自查：请求与通知有什么区别？为什么必须保留响应的 ID？

### 第三部分：先看最直观的 stdio 传输

看：

- [stdio_transport.go](../../internal/mcp/stdio_transport.go)

按这个顺序读：

```go
NewStdioTransport()
Start()
Send()
Close()
```

把它理解成：

```text
启动外部 MCP Server 子进程
    ├── CatCLI 写入子进程 stdin：请求、通知或响应
    ├── CatCLI 读取子进程 stdout：逐行解析 JSON-RPC 消息
    └── 子进程 stderr：单独转发日志
```

重点理解 `exec.Command()`、两个 Pipe 和读取 stdout 的 goroutine。暂时不用深入环境变量合并、写锁和退出等待的全部细节。

自查：为什么 Server 的普通日志不能输出到 stdout？CatCLI 退出时，为什么还要关闭 stdin 并等待子进程？

### 第四部分：重点学习 JSONRPCClient

看：

- [jsonrpc_client.go](../../internal/mcp/jsonrpc_client.go)

这是把“发消息”变成“调用方法并等待结果”的一层。

重点看：

```go
type JSONRPCClient struct
Call()
receive()
Notify()
fail()
```

先跟着一次 `Call()` 看：

```text
分配请求 ID
    ↓
pending[id] = 等待响应的 channel
    ↓
Transport.Send() 发送请求
    ↓
Server 返回带相同 ID 的响应
    ↓
receive() 查找 pending[id]
    ↓
将响应放入 channel，移除 pending 记录
    ↓
Call() 读取结果，解码到调用方提供的结构体
```

`pending` 是这一层最关键的字段：多个请求可以同时等待，即使响应顺序变化，也能按 ID 找到对应的调用方。

第二遍再看超时取消、连接失败时如何唤醒等待者，以及 Server 发来 `ping` 时如何回复。

配合测试：

- [jsonrpc_client_test.go](../../internal/mcp/jsonrpc_client_test.go)

优先看 `TestJSONRPCMatchesConcurrentOutOfOrderResponses`。

自查：为什么每个请求需要自己的 channel？超时后的迟到响应如何处理？`Notify()` 为什么不用等待 RPC 响应？

### 第五部分：理解 MCP Client 的握手、发现和调用

看：

- [client.go](../../internal/mcp/client.go)

先看数据模型：

```go
type ToolDescriptor struct
type InitializeResult struct
type CallToolResult struct
```

`ToolDescriptor` 定义在 [tool_descriptor.go](../../internal/mcp/tool_descriptor.go)，另外两个响应结构体定义在 `client.go`。这里先理解工具描述的数据结构，第七部分再看它如何适配成 `tool.Tool`。

然后看：

```go
Initialize()
ListTools()
CallTool()
```

主流程：

```text
initialize
    ↓
检查协商后的协议版本，保存 Server 能力
    ↓
notifications/initialized
    ↓
tools/list → 获取名称、描述、inputSchema，处理分页
    ↓
tools/call → 传入原始工具名和 arguments
    ↓
得到 content / structuredContent / isError
```

这里 `JSONRPCClient` 负责通用请求匹配，`Client` 负责 MCP 方法及其参数和结果。`ListTools()`、`CallTool()` 都会先确保初始化完成；已初始化时不会每次重新握手。

自查：`tools/list` 和 `tools/call` 分别解决什么问题？Server 返回的 `inputSchema` 最后会给谁使用？

### 第六部分：再看 HTTP 传输和会话

看：

- [http_transport.go](../../internal/mcp/http_transport.go)

建议顺序：

```go
NewStreamableHTTPTransport()
request()
Send()
readSSE()
Close()
```

先理解两种响应分支：

```text
POST 一条 JSON-RPC 消息
    ├── application/json：解析一条响应
    └── text/event-stream：读取 SSE 事件
           ├── 中间可能收到通知或 Server 请求
           └── 收到当前请求的最终响应后返回
```

两种分支最后都通过 `receive` 回调进入同一个 JSON-RPC 客户端。

再理解两个 Header：

- `Mcp-Session-Id`：Server 分配的 HTTP 会话标识，后续请求携带。
- `Mcp-Protocol-Version`：初始化时协商后的协议版本。

第一遍可以先跳过 Header 校验和关闭细节。第二遍再看会话过期后的初始化，以及为什么 `tools/call` 不会自动重放。

自查：HTTP 传输里的 `Start()` 为什么没有像 stdio 一样启动读循环？SSE 中的 `ping` 会由哪一层回复？

### 第七部分：看外部工具怎么变成 tool.Tool

看：

- [tool_descriptor.go](../../internal/mcp/tool_descriptor.go)
- [tool_registry.go](../../internal/tool/tool_registry.go)

重点看：

```go
ToolName()
adaptTool()
RegisterProvider()
Execute()
```

转换关系：

```text
ToolDescriptor
    ├── Name        → 加命名空间后的 FunctionDefinition.Name
    ├── Description → FunctionDefinition.Description
    └── InputSchema → FunctionDefinition.Parameters

Handler 闭包
    └── 记住原始工具名和对应 Client
           ↓
        Client.CallTool(原始工具名, arguments)
```

例如，注册名是 `mcp__local__search`，发给外部 Server 的工具名仍然是 `search`。Agent 调用现有 `ToolRegistry.Execute()` 后，就会进入这个 Handler。

自查：为什么要同时保留“注册名”和“原始工具名”？`isError=true` 怎样反馈给 Agent？

### 第八部分：理解 ServerManager 如何协调多个 Server

看：

- [server_manager.go](../../internal/mcp/server_manager.go)

先看：

```go
type Server struct
type ServerStatus struct
type ServerManager struct
```

这里的 `Server` 是 CatCLI 保存一条连接及其工具的状态容器，实际服务运行在子进程或远端。

然后看 `Start()`：

```text
筛选启用的 Server
    ↓
最多 4 个 Server 并行初始化
    ↓
为每个 Server 创建 Transport → JSONRPCClient → Client
    ↓
ListTools()：确保握手完成，并发现工具
    ↓
按配置过滤工具，adaptTool() 转为 tool.Tool
    ↓
汇总成功连接和工具，分别保留失败状态
```

最后看 `Tools()` 和 `Close()`，理解它如何作为 `tool.Provider` 提供工具，以及如何统一释放连接。

自查：一个 Server 启动失败，为什么其他 Server 还能使用？为什么在 Agent 开始执行前完成工具注册？

### 第九部分：最后看 main.go 装配

最后看：

- [config.go](../../internal/config/config.go)：CLI 如何读取 `mcp.config_file`。
- [main.go](../../cmd/catcli/main.go)：搜索 `mcp.LoadConfig` 和 `RegisterProvider(manager)`。

组装过程：

```text
mcp.config_file
    ↓
mcp.LoadConfig()
    ↓
NewServerManager() → Start()
    ↓
ToolRegistry.RegisterProvider(manager)
    ├── ReAct Agent 使用完整注册表
    └── Plan-and-Execute 的 Worker 使用完整注册表

退出 → manager.Close()
```

第一遍只需要确认：MCP 工具在创建 Agent 前注册，后续通过已有工具调用流程执行。`cmd/catcli/reviewer_tools.go` 为 Reviewer 优先选择 `mcp__filesystem__list_directory` 和 `mcp__filesystem__read_text_file`，缺失时分别回退到 `list_dir` 和 `read_file`。它不会获得写文件、编辑或 Shell 工具；如果更改配置中的 Server 名称，也需要同步这里的明确白名单。

如果想继续追踪执行过程，再看：

- [react_agent.go](../../internal/agent/react_agent.go)
- [task_executor.go](../../internal/agent/multiagent/task_executor.go)
- [worker_sub_agent.go](../../internal/agent/multiagent/worker_sub_agent.go)

### 最适合的实际学习路线

第一遍先看接口和主流程：

```text
config.go
transport.go
jsonrpc_client.go
client.go
tool_descriptor.go
server_manager.go
```

第二遍看具体传输实现和故障处理：

```text
stdio_transport.go
http_transport.go
jsonrpc_client_test.go
integration_test.go
```

第三遍看装配与 Agent 接入：

```text
internal/config/config.go
internal/tool/tool_registry.go
cmd/catcli/main.go
internal/agent/react_agent.go
internal/agent/multiagent/task_executor.go
```

最核心的一条调用主线：

```text
Agent 选择 mcp__local__search
→ ToolRegistry.Execute()
→ adaptTool() 创建的 Handler
→ Client.CallTool("search", arguments)
→ JSONRPCClient.Call("tools/call", params)
→ Transport.Send()
→ 外部 Server 执行并返回响应
→ receive() 按 ID 交付结果
→ 工具结果回到 Agent
```

先把这条主线看懂，再研究分页、并发、取消、会话过期和关闭顺序。

协议依据：[MCP 传输规范](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)、[生命周期](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)、[工具](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)。

## 测试

```bash
go test ./internal/mcp -count=1
go test -race ./internal/mcp -count=1
```

测试会启动本地 HTTP 服务及测试用子进程，验证握手、分页发现、JSON/SSE、请求乱序匹配、超时取消、会话重建、命名空间、工具白名单、并行启动、故障隔离与退出回收，不需要外部 MCP Server 或 LLM API。
