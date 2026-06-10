# @resume — ACP 会话恢复设计

## 概述

支持 `@resume` 命令，用于恢复当前智能体的历史 ACP 会话。仅 ACP 传输且具备 `LoadSession` 能力的 agent 可用。用户输入 `@resume` 后，前端通过 ACP `ListSessions` 获取会话列表，在 BottomSheet 中结构化展示，用户点击确认后通过 `LoadSession` 恢复会话到新 ClawBench 会话，重放消息全部存入数据库。

## ACP 协议关键语义

`@resume` 涉及两个独立的 ACP 能力，必须分别检测：

| 能力 | 声明位置 | SDK 接口 | 作用 |
|------|---------|---------|------|
| `loadSession` | `AgentCapabilities.LoadSession` (Initialize 响应) | `AgentLoader` 可选接口 | 支持 `session/load`：恢复会话 + 重放消息 |
| `list` | `SessionCapabilities.List` (Initialize 响应) | `Agent` 基础接口的 `ListSessions` 方法 | 支持 `session/list`：列出历史会话 |

**关键区别**：`ListSessions` 是 `Agent` 基础接口的必需方法（所有 agent 都必须实现），但只有声明了 `SessionCapabilities.List` 的 agent 才真正支持会话列表。未声明时调用 `ListSessions` 会返回 `MethodNotFound` 错误。`LoadSession` 则是可选的 `AgentLoader` 接口，必须同时声明 `AgentCapabilities.LoadSession = true`。

`@resume` 需要**两个能力同时具备**：`list` 提供会话列表供用户选择，`loadSession` 提供恢复 + 重放。

### LoadSession 重放语义（ACP 规范确认）

根据 ACP 规范，`LoadSession` 的行为是：

1. Client 调用 `session/load`（传入 `sessionId`、`cwd`、`mcpServers`）
2. Agent 恢复会话上下文，连接 MCP servers
3. Agent **必须**通过 `session/update` 通知重放**全部对话历史**（user_message_chunk + agent_message_chunk + tool_call 等）
4. **所有重放完成后**，Agent 的 `session/load` RPC 返回 `LoadSessionResponse{Modes, ConfigOptions}`

**关键**：`LoadSession` RPC 是**阻塞调用**，在重放全部完成后才返回。这与 `ResumeSession`（不重放、立即返回）完全不同。

## 用户操作流程

1. 用户在聊天输入栏输入 `@resume`（仅 ACP + LoadSession + ListSessions 能力的 agent 在 autocomplete 中展示）
2. 前端拦截消息发送（不发到后端聊天流），改为调用 `GET /api/agents/{agentId}/acp-sessions`
3. BottomSheet 弹出展示 ACP 会话列表（title、updatedAt、cwd）
4. 用户点击某条会话 → 弹出 DialogOverlay 确认（"恢复此会话？将创建新会话并加载历史消息"）
5. 用户确认 → 前端调用 `POST /api/ai/session/acp-load`，展示全屏遮罩 "正在恢复会话..."
6. 后端创建新 ClawBench 会话，执行 `LoadSession`（阻塞等待重放完成），将重放消息批量写入 `chat_history`
7. 请求完成后前端关闭遮罩，`switchSession` → `loadHistory` 一次性展示所有消息

**关键约束**：`@resume` 不向聊天发送任何文本消息，纯 UI 交互命令。

## 后端 API

### GET /api/agents/{agentId}/acp-sessions

获取 ACP agent 的会话列表。路由挂在 `ServeAgentSubRoutes` 子路由分发中（与 `refresh-models` 同级），匹配路径后缀 `/acp-sessions`。

- **请求参数**：`?cursor=xxx`（可选分页）、`?cwd=xxx`（可选，按工作目录过滤）
- **后端逻辑**：
  1. 校验 agent 存在且为 ACP 传输且具有 `ListSessions` 能力（`SessionCapabilities.List`）
  2. 获取该 agent 的活跃 ACP 连接（可通过任一该 agent 的 `ACPConn` 调用）
  3. 若进程已死（`!conn.Alive()`），先调 `EnsureAlive()` 重建连接（仅 Initialize，不 NewSession）
  4. 调用 `conn.ListSessions(cursor, cwd)` 获取 `acp.ListSessionsResponse`
  5. 过滤掉当前活跃的 `acpSID`（避免恢复到自身）
  6. 返回给前端
- **响应格式**：
```json
{
  "sessions": [
    {
      "sessionId": "abc-123",
      "title": "Fix auth bug",
      "cwd": "/home/user/project",
      "updatedAt": "2026-06-10T08:30:00Z"
    }
  ],
  "nextCursor": "xyz-456"
}
```

### POST /api/ai/session/acp-load

创建新会话并执行 LoadSession 恢复 ACP 会话。**同步请求**（非 SSE）。LoadSession RPC 本身阻塞直到重放完成。

- **请求体**：
```json
{
  "agentId": "agent-1",
  "acpSessionId": "abc-123",
  "projectId": "project-1"
}
```
- **后端逻辑**：
  1. 校验 agent 存在且支持 LoadSession（`AgentCapabilities.LoadSession`）
  2. 创建新 `ChatSession`：
     - `Backend: agent.Backend`（与 agent 的 backend 一致，不硬编码 `"acp"`）
     - `AgentID: agentId`
     - `SourceSessionID: "acp:{acpSessionId}"`（前缀区分来源）
  3. 通过 `ACPConnManager.GetOrCreateConnForLoad()` 为新会话创建 ACP 连接，传入 `acpSessionId` 标记
  4. `ensureAliveWithSession()` 检测 `loadTargetSID` 标记 → 调用 `LoadSession` 而非 `NewSession`/`ResumeSession`
  5. `conn.LoadSession(sessionId, cwd, mcpservers)` **阻塞**，期间 agent 通过 `SessionUpdate` 推送重放消息
  6. `SessionUpdate` 回调中收集重放消息到缓冲区（详见"消息收集"章节）
  7. `LoadSession` RPC 返回 `LoadSessionResponse{Modes, ConfigOptions}`，表示重放完成
  8. `acpSID = acpSessionId`（LoadSessionResponse 不返回 sessionId，acpSID 就是请求中传入的）
  9. 缓存 LoadSession 返回的 mode/config 状态
  10. 将收集的重放消息批量写入 `chat_history`
  11. 返回 `{ sessionId: "new-clawbench-session-id" }`
- **超时**：60 秒，超时后返回已收集消息 + 警告
- **并发控制**：同一 agent 同时只允许一个 LoadSession 操作，后续请求返回 409

## 能力检测

### LoadSession 能力

- `AgentCapabilities.LoadSession` 由 ACP `Initialize` 响应返回
- **提取时机**：`spawnLocked()` 中 `conn.Initialize()` 返回后，立即从 `initResp.AgentCapabilities.LoadSession` 提取并缓存到 `AgentCapabilityRegistry`
- `AgentCapability` 结构体新增 `LoadSession bool` 字段
- 持久化到 `agents` 表新增列 `acp_load_session INTEGER NOT NULL DEFAULT 0`
- `AgentCapabilityRegistry.GetLoadSession(agentID)` 返回能力状态
- `GET /api/agents` 响应中包含 `loadSession` 字段

### ListSessions 能力

- `SessionCapabilities.List` 由 ACP `Initialize` 响应返回（`initResp.SessionCapabilities.List`）
- **提取时机**：与 `LoadSession` 同步提取，缓存到 `AgentCapabilityRegistry`
- `AgentCapability` 结构体新增 `ListSessions bool` 字段
- 持久化到 `agents` 表新增列 `acp_list_sessions INTEGER NOT NULL DEFAULT 0`
- `AgentCapabilityRegistry.GetListSessions(agentID)` 返回能力状态
- `GET /api/agents` 响应中包含 `listSessions` 字段

### 前端能力判断

- `@resume` 在 autocomplete 中展示的条件：**ACP 传输 + `loadSession` + `listSessions`**
- 非 ACP agent 或缺少任一能力时，`@resume` 不可见，手动输入时 toast 提示不支持

## 前端组件

### ChatInputBar 修改

在 `@` 命令 autocomplete 列表新增 `@resume`，条件为当前会话的 agent 是 ACP 传输且同时具有 `LoadSession` 和 `ListSessions` 能力。

用户选择或输入 `@resume ` 后，拦截消息发送，触发 AcpSessionDrawer。

### AcpSessionDrawer 组件（新增）

BottomSheet 组件，展示 ACP 会话列表：

- **列表项**：title（主行）、cwd + updatedAt（副行）
- **空状态**：agent 无历史会话时展示提示 "无历史会话"
- **加载状态**：skeleton / spinner
- **分页**：通过 `nextCursor` 支持滚动到底部自动加载更多
- **点击行为**：弹出 `DialogOverlay` 确认 → 调用 `POST /api/ai/session/acp-load`

### useAcpSession composable（新增）

- `loadAcpSessions(agentId, cursor?, cwd?)` — 调用 `GET /api/agents/{agentId}/acp-sessions`
- `acpLoadSession(agentId, acpSessionId, projectId)` — 调用 `POST /api/ai/session/acp-load`
- `acpSessions` ref — 列表数据
- `acpSessionsLoading` ref — 加载状态
- `acpResuming` ref — 恢复中状态（控制遮罩）
- `nextCursor` ref — 分页游标

### @resume 消息拦截

在 `ChatInputBar` 的发送逻辑中，检测消息以 `@resume` 开头时：
1. 不调用后端 chat API
2. 清空输入框
3. 打开 `AcpSessionDrawer` BottomSheet
4. 触发 `loadAcpSessions()` 加载列表

### Badge 渲染

`contentBlocks.ts` 的 `AT_COMMAND_RE` 添加 `@resume`，使其在用户消息中渲染为紫色 badge（fallback 显示）。

### 加载遮罩

- **触发**：用户确认恢复 → `acpResuming = true`，关闭 BottomSheet，展示遮罩 "正在恢复会话..."
- **关闭**：`POST /api/ai/session/acp-load` 成功返回后，`acpResuming = false`，`switchSession` → `loadHistory` 一次性加载消息
- **超时提示**：15 秒无响应追加 "恢复较慢，请稍候..."
- **错误**：请求失败时关闭遮罩，toast 提示错误

### Store 变更

`stores/app.ts` 新增 `acpSessionsOpen` ref 控制 BottomSheet 显示。

## ACP 连接管理

### LoadSession 分支

`ensureAliveWithSession()` 需新增 LoadSession 分支：

**标记传递**：`ACPConn` 新增 `loadTargetSID string` 字段。`GetOrCreateConnForLoad()` 创建 `ACPConn` 时设置此字段。`ensureAliveWithSession()` 检测到 `loadTargetSID != ""` 时走 LoadSession 分支。

- `spawnLocked()` 启动 agent 进程 + Initialize 后，执行 `conn.LoadSession(loadTargetSID, cwd, mcpservers)`
- `conn.LoadSession()` **阻塞**，期间 agent 通过 `SessionUpdate` 推送重放消息，`SessionUpdate` 回调中收集
- `LoadSession` RPC 返回后：`acpSID = loadTargetSID`（LoadSessionResponse 不返回 sessionId，acpSID 就是请求传入的）
- 缓存 `LoadSessionResponse{Modes, ConfigOptions}` 到 `lastLoadSessionResp`
- 清除 `loadTargetSID`
- 后续行为与 `NewSession` 一致（可以正常 Prompt）

### GetOrCreateConnForLoad

新增 `ACPConnManager.GetOrCreateConnForLoad(ctx, agent, clawbenchSID, cwd, acpSessionID)` 方法：
- 创建 `ACPConn` 并设置 `loadTargetSID = acpSessionID`
- 调用 `ensureAliveWithSession(ctx, cwd)`
- `ensureAliveWithSession` 检测到 `loadTargetSID` 后走 LoadSession 分支
- LoadSession 完成后清除 `loadTargetSID`（在 ensureAliveWithSession 内部完成）

### 消息收集

由于 ACP 规范明确 `LoadSession` RPC 阻塞直到重放完成，消息收集机制如下：

1. 在 `ACPConn` 新增 `loadSessionActive bool` 标记，`LoadSession` 调用前设为 true
2. `ClawBenchACPClient.SessionUpdate()` 中检测 `loadSessionActive`，将重放消息路由到收集缓冲区（而非 SSE stream channel）
3. 收集所有重放消息到 `[]CollectedMessage` 缓冲区
4. `LoadSession` RPC 返回后，表示重放完成，收集结束
5. 将收集的重放消息批量写入 `chat_history` 表
6. 清除 `loadSessionActive` 标记

**超时兜底**：虽然 `LoadSession` RPC 本身会阻塞到完成，但仍需 60 秒超时保护，防止 agent 异常导致无限阻塞。超时后使用已收集的消息 + 警告返回。

**注意**：`ListSessions` 不需要 `loadSessionActive` 标记——`ListSessions` 是独立于 LoadSession 的普通 RPC 调用，不涉及消息重放。

### ListSessions 方法

`ACPConn` 新增 `ListSessions(ctx, cursor, cwd)` 方法：
- 需要 `conn` 和 `alive` 状态（不要求有 `acpSID`）
- 若进程已死，调用方需先调用 `EnsureAlive(ctx, cwd)` 仅执行 spawn + Initialize（不 NewSession）
- 调用 `conn.ListSessions(acp.ListSessionsRequest{Cursor, Cwd})`
- 返回 `acp.ListSessionsResponse`
- Agent 未声明 `SessionCapabilities.List` 时会返回 `MethodNotFound` 错误，handler 层捕获并返回 501

### EnsureAlive 方法

`ACPConn` 新增 `EnsureAlive(ctx, cwd)` 方法，仅确保进程存活 + Initialize 完成：
- 如果 `alive && isAliveLocked()`，直接返回
- 否则 `spawnLocked(ctx)` 启动进程
- 不调用 NewSession / ResumeSession / LoadSession

## 错误处理

| 场景 | 处理方式 |
|------|---------|
| Agent 不支持 ListSessions（无 `SessionCapabilities.List`） | 返回 501，toast "该智能体不支持会话列表" |
| Agent 不支持 LoadSession（`AgentCapabilities.LoadSession = false`） | `@resume` 不在 autocomplete，手动输入时 toast 提示 |
| ACP 连接未建立/已断开 | 尝试 `EnsureAlive()` 重建，失败则 toast "智能体连接不可用" |
| LoadSession 调用失败 | 返回错误，toast "会话恢复失败"，清理已创建的空会话 |
| LoadSession RPC 超时（60s） | 返回已收集消息 + 警告，前端提示部分消息可能缺失 |
| 恢复的会话是当前活跃会话 | 列表中过滤掉当前 `acpSID` |
| 同时多个恢复请求 | 同一 agent 返回 409 |
| Agent 进程在重放中崩溃 | 返回已收集消息 + 错误提示 |
| 空列表 | BottomSheet 展示 "无历史会话" |
| LoadSession 后 agent 无重放消息 | 返回空消息列表，前端正常打开空会话（与 NewSession 行为一致） |

## 边界情况

- **重复恢复**：同一 ACP 会话可被多次恢复（每次创建新 ClawBench 会话）
- **项目隔离**：LoadSession 传入当前项目 cwd；ListSessions 可选 cwd 过滤
- **sourceSessionId**：格式 `acp:{acpSessionId}`，与定时任务的 `sourceSessionId` 共存，可渲染紫色 "恢复" badge
- **后续对话**：恢复的会话 acpSID 已建立，后续 Prompt 正常走 `ExecuteStream` → `ensureAliveWithSession` → 复用连接
- **进程生命周期**：LoadSession 创建的新连接与普通 NewSession 连接生命周期一致，受相同 idle-reap 和崩溃恢复机制管理
- **仅支持 LoadSession 不支持 ListSessions**：理论可能但无实际意义（无法选择恢复哪个会话），前端不展示 `@resume`
- **仅支持 ListSessions 不支持 LoadSession**：可列出会话但无法恢复，前端不展示 `@resume`

## 与现有功能的关系

- **SessionDrawer**：恢复的会话正常展示，带 `sourceSessionId` badge
- **Continue Conversation**：恢复的会话后续可正常使用"继续对话"
- **AutoResumeBackend**：`@resume` 仅适用于 ACP，与 AutoResumeBackend 无关
- **session_resume**：现有 `POST /api/ai/session/resume` 是恢复软删除的 ClawBench 会话，与 ACP LoadSession 无关
- **ServeAgentSubRoutes**：`acp-sessions` 路由挂在此子路由分发器中，与 `refresh-models` 同级
- **ResumeSession**：ACP 的 `session/resume` 不重放消息（ClawBench 用它做崩溃恢复），与 `session/load`（重放消息）语义完全不同

## 文件变更清单

### 后端 Go

| 文件 | 变更 |
|------|------|
| `internal/model/agent.go` | `Agent` 结构体新增 `LoadSession bool`（JSON: `loadSession`）、`ListSessions bool`（JSON: `listSessions`）字段 |
| `internal/ai/agent_capability.go` | `AgentCapability` 新增 `LoadSession bool`、`ListSessions bool`；新增 `GetLoadSession(agentID)`、`GetListSessions(agentID)`；`ForceUpdate()` 提取能力；持久化到 `acp_load_session`、`acp_list_sessions` 列 |
| `internal/ai/acp_pool.go` | `ACPConn` 新增 `loadTargetSID`、`loadSessionActive`、`lastLoadSessionResp` 字段；新增 `ListSessions()`、`EnsureAlive()` 方法；`ACPConnManager` 新增 `GetOrCreateConnForLoad()` 方法；`ensureAliveWithSession()` 新增 LoadSession 分支 |
| `internal/ai/acp_backend.go` | `spawnLocked()` 中 Initialize 后提取 `LoadSession` 和 `ListSessions` 能力到 Registry；新增 `cacheLoadSessionState()` |
| `internal/ai/acp_client.go` | `SessionUpdate()` 中识别 LoadSession 重放消息，路由到收集缓冲区而非 SSE stream channel |
| `internal/handler/agent.go` | `ServeAgentSubRoutes` 新增 `acp-sessions` 子路由；新增 `ServeACPSessions` handler |
| `internal/handler/chat.go` | 新增 `ServeACPLoadSession` handler |
| `internal/service/database.go` | 新增 migration：`agents` 表新增 `acp_load_session INTEGER NOT NULL DEFAULT 0` 和 `acp_list_sessions INTEGER NOT NULL DEFAULT 0` 列 |
| `internal/handler/handler.go` | 注册新路由 `POST /api/ai/session/acp-load` |

### 前端 Vue

| 文件 | 变更 |
|------|------|
| `web/src/utils/contentBlocks.ts` | `AT_COMMAND_RE` 添加 `@resume` |
| `web/src/components/chat/ChatInputBar.vue` | autocomplete 添加 `@resume`（条件：ACP + loadSession + listSessions），拦截发送 |
| `web/src/composables/useAcpSession.ts` | **新增** |
| `web/src/components/chat/AcpSessionDrawer.vue` | **新增** |
| `web/src/stores/app.ts` | 新增 `acpSessionsOpen` ref |

### Mock agent

| 文件 | 变更 |
|------|------|
| `cmd/acp-mock/main.go` | `Initialize` 响应中声明 `SessionCapabilities.List`；实现 `ListSessions`（返回模拟会话列表）；实现 `AgentLoader` 接口的 `LoadSession`（推送模拟重放消息后返回 Modes/ConfigOptions） |
