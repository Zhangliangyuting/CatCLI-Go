# RAG 系统推荐阅读顺序

```text
1. Workspace
      ↓
2. Chunk / Chunker
      ↓
3. EmbeddingClient
      ↓
4. WorkspaceIndexer
      ↓
5. ChunkStore / SQLiteStore
      ↓
6. Retriever
      ↓
7. SearchResultFormatter
      ↓
8. ContextProvider
      ↓
9. Agent 接入
      ↓
10. main.go 装配
```

## 第一部分：先理解工作区

先看：

- [workspace.go](../../internal/workspace/workspace.go)

重点理解：

```go
type Workspace struct
```

它表示 Agent 当前可以操作和索引的项目根目录。

你只需要先搞懂三个问题：

- 工作区根目录是什么？
- `.catcli` 放在哪里？
- 如何保证文件路径不会逃出工作区？

暂时不用研究每个路径校验细节。

## 第二部分：理解文件怎么变成 Chunk

先看数据模型：

- [chunk.go](../../internal/rag/chunk.go)

重点看：

```go
type Chunk struct
```

理解每个字段：

```text
Path       文件路径
Language   文件语言
Kind       function、method、section、paragraph
Symbol     Go 函数或方法名称
Heading    Markdown 标题
StartLine  起始行
EndLine    结束行
Content    真正参与检索的内容
```

然后看统一入口：

- [chunker.go](../../internal/rag/chunker.go)

重点看：

```go
type Chunker interface
type ChunkerSet struct
func (set *ChunkerSet) Chunk(...)
```

它所做的事情很简单：

```text
传入文件
  ↓
判断由哪个 Chunker 处理
  ↓
生成 []Chunk
```

接着依次看三个具体实现：

1. [go_chunker.go](../../internal/rag/go_chunker.go)
2. [markdown_chunker.go](../../internal/rag/markdown_chunker.go)
3. [text_chunker.go](../../internal/rag/text_chunker.go)

建议重点学习 `go_chunker.go`，因为这里体现了 AST 分块：

```text
Go 源文件
  ↓
parser.ParseFile()
  ↓
得到 AST
  ↓
找到类型、函数、方法
  ↓
根据源码位置截取内容
  ↓
生成 Chunk
```

配合测试看最容易理解：

- [chunker_test.go](../../internal/rag/chunker_test.go)

## 第三部分：理解 Embedding

先看接口：

- [embedder.go](../../internal/llm/embedder.go)

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}
```

它的本质就是：

```text
文字
  ↓
Embedding 模型
  ↓
[]float32
```

例如：

```text
"WorkspaceIndexer 用来构建索引"
        ↓
[0.17, -0.42, 0.81, ...]
```

然后看真实 API 实现：

- [embedding_client.go](../../internal/llm/embedding_client.go)

重点关注：

```go
Embed()
EmbedBatch()
```

暂时不用纠结向量里的每个数字是什么意思。只需要知道：意思越相似的文本，向量方向通常越接近。

## 第四部分：重点学习 WorkspaceIndexer

这是整个 RAG 的“数据生产流水线”，建议认真看：

- [workspace_indexer.go](../../internal/rag/workspace_indexer.go)

入口：

```go
func (indexer *WorkspaceIndexer) Index(
    ctx context.Context,
) (IndexReport, error)
```

把它理解成：

```text
扫描工作区
  ↓
过滤 .git、.catcli、二进制文件等
  ↓
计算文件 Hash
  ↓
文件未修改 → 跳过
  ↓
文件有变化 → Chunker 分块
  ↓
EmbedBatch 生成向量
  ↓
ReplaceFile 写入 SQLite
  ↓
删除已经不存在的文件索引
```

重点看这些方法：

```go
Index()
scan()
readFile()
embedChunks()
```

再看它的测试：

- [workspace_indexer_test.go](../../internal/rag/workspace_indexer_test.go)

测试比实现更容易帮助你理解：

- 第一次如何创建索引
- 第二次如何跳过未修改文件
- 文件修改后如何重新索引
- 文件删除后如何清理索引
- Embedding 模型变化后为什么重建
- Chunker 配置变化后为什么重建

## 第五部分：理解 SQLite 怎么存

先看接口，不要直接看 SQL：

- [chunk_store.go](../../internal/rag/chunk_store.go)

重点看：

```go
type IndexedFile struct
type EmbeddedChunk struct
type IndexMetadata struct
type ChunkReader interface
type ChunkStore interface
```

这里定义了“索引器希望数据库提供什么能力”。

然后看具体实现：

- [sqlite_store.go](../../internal/rag/sqlite_store.go)

阅读顺序：

```text
sqliteSchema
  ↓
NewSQLiteStore()
  ↓
ResetIndex()
  ↓
ReplaceFile()
  ↓
LoadChunks()
  ↓
DeleteFile()
```

三张表的关系：

```text
rag_metadata
    保存模型和 Chunker 版本

rag_files
    一行代表一个被索引文件

rag_chunks
    一个文件对应多个 Chunk
    每个 Chunk 保存源码和 embedding
```

建议最后再研究事务、外键和 BLOB 编码。

## 第六部分：重点学习 Retriever

这是 RAG 的“查询流水线”：

- [retriever.go](../../internal/rag/retriever.go)

入口：

```go
func (retriever *Retriever) Search(
    ctx context.Context,
    query string,
    limit int,
) ([]SearchResult, error)
```

流程：

```text
用户问题
  ├── codeTerms() 提取关键词
  │      └── BM25 关键词评分
  │
  └── Embed() 生成查询向量
         └── 余弦相似度评分

两路结果
  ↓
fuseRetrievalRanks()
  ↓
排序并返回 Top-K
```

建议按这个顺序读：

```go
Search()
buildRetrievalItems()
scoreBM25()
scoreSemantic()
fuseRetrievalRanks()
vectorCosine()
```

其中最重要的概念是：

- BM25：找“字面上相同”的代码
- Embedding：找“语义上相似”的代码
- Rank Fusion：合并两路排序

配合测试看：

- [retriever_test.go](../../internal/rag/retriever_test.go)

## 第七部分：看检索结果怎么进入上下文

先看：

- [result_formatter.go](../../internal/rag/result_formatter.go)

它负责把 `[]SearchResult` 变成模型能读的文本：

````markdown
### Workspace excerpt
path=internal/rag/workspace_indexer.go:105-172
kind=method
symbol=WorkspaceIndexer.Index

```go
func (indexer *WorkspaceIndexer) Index(...) {
    ...
}
```
````

同时确保不超过 token 预算。

然后看：

- [context_provider.go](../../internal/rag/context_provider.go)

核心方法：

```go
func (provider *ContextProvider) Build(
    ctx context.Context,
    query string,
) ([]llm.Message, error)
```

它把前面的组件串起来：

```text
query
  ↓
Retriever.Search()
  ↓
SearchResultFormatter.Format()
  ↓
llm.SystemMessage(...)
```

这就是 RAG 的上下文增强（Augmentation）步骤，之后由聊天模型根据参考资料生成回答。

## 第八部分：看 Agent 怎么使用 RAG

先看公共接口：

- [agent.go](../../internal/agent/agent.go)

```go
type ReferenceContextProvider interface {
    Build(
        ctx context.Context,
        query string,
    ) ([]llm.Message, error)
}
```

然后看 ReAct：

- [react_agent.go](../../internal/agent/react_agent.go)

重点找：

```go
WithReferenceContextProvider()
buildReferenceContext()
contextMessagesFor()
```

最终消息顺序是：

```text
Agent 系统提示词
  ↓
RAG 检索内容
  ↓
对话记忆
  ↓
用户消息
```

Multi-Agent 可以最后看：

- [plan_execute_agent.go](../../internal/agent/multiagent/plan_execute_agent.go)
- [worker_sub_agent.go](../../internal/agent/multiagent/worker_sub_agent.go)
- [reviewer_sub_agent.go](../../internal/agent/multiagent/reviewer_sub_agent.go)
- [task_executor.go](../../internal/agent/multiagent/task_executor.go)

先理解：

```text
Planner  → 使用原始问题检索
Worker   → 使用当前任务检索
Reviewer → 使用任务要求检索
```

## 第九部分：最后看 main.go

最后再看：

- [main.go](../../cmd/catcli/main.go)

重点看：

```go
buildWorkspaceRAG(...)
```

它只是装配前面的所有零件：

```text
SQLiteStore
+ ChunkerSet
+ Embedder
+ WorkspaceIndexer
+ Retriever
+ SearchResultFormatter
+ ContextProvider
```

然后把 `ContextProvider` 注入 ReAct 和 Multi-Agent。当前在启动时调用 `Index()` 同步索引，每次提问时再检索；不会在每次提问时重新索引文件。

## 最适合的实际学习路线

第一遍只看这 6 个文件：

```text
chunk.go
chunker.go
workspace_indexer.go
chunk_store.go
retriever.go
context_provider.go
```

先建立整体概念。

第二遍再看实现细节：

```text
go_chunker.go
embedding_client.go
sqlite_store.go
result_formatter.go
```

第三遍看 Agent 接入：

```text
react_agent.go
worker_sub_agent.go
reviewer_sub_agent.go
main.go
```

最核心的一条主线是：

```text
文件
→ Chunk
→ Embedding
→ SQLite
→ Search
→ Format
→ System Message
→ LLM
```

只要先把这条线看明白，整个 RAG 系统就已经理解了大约 80%。
