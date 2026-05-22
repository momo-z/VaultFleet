# 快照浏览与选择性恢复

## 概述

在恢复对话框中增加快照内容浏览功能，用户可以查看快照中包含的完整文件树（目录 + 文件），通过勾选框选择要恢复的路径，实现选择性恢复。

## 用户交互流程

1. 快照浏览页 → 点击某个快照的「恢复」按钮 → 打开恢复 Sheet
2. Sheet 中显示快照 ID、快照时间等基本信息
3. 新增「浏览快照内容」可折叠区域，点击展开后向 agent 发起 `restic ls` 请求
4. 加载完成后显示完整文件树（tree 形式），每个条目右侧有勾选框
5. 默认全部不勾选 — 不勾选任何项时点击恢复 = 恢复全部（与现有行为一致）
6. 勾选部分条目后，按钮文案变为「恢复选中的 N 项」，只恢复选中路径
7. 输入目标路径 → 勾选确认 → 提交恢复

## 技术方案

采用一次性加载整棵文件树。`restic ls` 没有深度限制参数，任何方案都需要跑一次全量 ls，因此一次性拿到数据后在前端构建 tree，展开/折叠均为本地操作，体验最好。

## 各层改动

### 1. 协议层 — `pkg/protocol/message.go`

新增消息类型：

```
TypeSnapshotBrowseReq  = "snapshot_browse_req"
TypeSnapshotBrowseResp = "snapshot_browse_resp"
```

新增 payload 结构体：

```go
type SnapshotBrowseReqPayload struct {
    SnapshotID string `json:"snapshot_id"`
}

type SnapshotBrowseRespPayload struct {
    SnapshotID string              `json:"snapshot_id"`
    Entries    []SnapshotFileEntry `json:"entries"`
    Error      string              `json:"error,omitempty"`
}

type SnapshotFileEntry struct {
    Path  string `json:"path"`
    Type  string `json:"type"`   // "file" 或 "dir"
    Size  int64  `json:"size"`
    Mtime string `json:"mtime"`  // ISO8601
}
```

修改 `RestoreReqPayload`，增加字段：

```go
type RestoreReqPayload struct {
    SnapshotID   string   `json:"snapshot_id"`
    Target       string   `json:"target"`
    IncludePaths []string `json:"include_paths,omitempty"`
}
```

`IncludePaths` 为空 → 全量恢复（向后兼容）；有值 → restic 加 `--include` 参数。

### 2. Agent 执行器 — `internal/agent/executor/restic.go`

新增方法：

```go
func (r ResticRunner) LsSnapshot(ctx context.Context, snapshotID string) ([]SnapshotFileEntry, error)
```

- 执行 `restic ls <snapshotID> --json -r <repo> --password-file <file>`
- 解析 JSONL 输出（每行一个 JSON），第一行是 snapshot 元数据（跳过），后续行是文件条目
- 每行格式：`{"name":"...","type":"file|dir","path":"/...","size":123,"mtime":"2026-..."}`
- 返回 `[]SnapshotFileEntry`

修改 `buildRestoreCmd` / `RestoreSnapshot`：

- 增加 `includePaths []string` 参数
- 当 `includePaths` 不为空时，为每个路径加 `--include <path>` 参数

### 3. Agent 消息处理 — `internal/agent/handler.go`

新增 handler：

- `handleSnapshotBrowseReq`：解析 `SnapshotBrowseReqPayload` → 加载 policy 获取 repo 配置 → 调用 `LsSnapshot` → 发回 `snapshot_browse_resp`
- 新增 `SnapshotBrowseRunnerFunc` 类型和 `snapshotBrowseRunner` 字段（支持测试注入）

修改 `handleRestoreReq`：

- 从 `RestoreReqPayload` 读取 `IncludePaths`，传给 `restoreRunner`

修改 `RestoreRunnerFunc` 签名：增加 `includePaths []string` 参数。

在 `Handle` switch 中注册 `TypeSnapshotBrowseReq`。

顶层新增 `runSnapshotBrowse` 函数，实例化 `ResticRunner` 并调用 `LsSnapshot`。

### 4. Master API — 新增 `internal/master/api/snapshot_browse.go`

模式与 `browse.go` 完全对称：

```
POST /agents/:id/snapshot-browse
Body: { "snapshot_id": "xxx" }
```

- 检查 agent 存在且在线
- `resolveSnapshotID`：支持数据库 UUID 或 restic snapshot ID
- 向 agent 发送 `snapshot_browse_req`，通过 `SendAndWait` 等待 `snapshot_browse_resp`
- 超时 60s（restic ls 比目录浏览慢，特别是大快照或远端存储）
- Agent 离线返回 502

新增 `SnapshotBrowseHandler` 结构体，实现 `BrowseHub` 接口（复用已有接口）。

路由注册在 `router.go` 中：`RegisterSnapshotBrowseRoutes(protected, snapshotBrowseHandler)`。

### 5. Master API — 修改 `internal/master/api/restore.go`

`restoreRequest` 增加字段：

```go
type restoreRequest struct {
    SnapshotID   string   `json:"snapshot_id" binding:"required"`
    TargetPath   string   `json:"target_path"`
    Target       string   `json:"target"`
    IncludePaths []string `json:"include_paths"`
}
```

构造 `RestoreReqPayload` 时透传 `IncludePaths`。

### 6. 前端类型 — `web/src/types/snapshot.ts`

新增：

```typescript
export interface SnapshotFileEntry {
  path: string;
  type: "file" | "dir";
  size: number;
  mtime: string;
}

export interface SnapshotBrowseResponse {
  snapshot_id: string;
  entries: SnapshotFileEntry[];
  error?: string;
}
```

修改 `RestoreRequest`：

```typescript
export interface RestoreRequest {
  snapshot_id: string;
  target_path: string;
  include_paths?: string[];
}
```

### 7. 前端 API — `web/src/services/snapshots.ts`

新增：

```typescript
export const browseSnapshot = (agentId: string, body: { snapshot_id: string }) =>
  apiPost<SnapshotBrowseResponse>(`/api/agents/${agentId}/snapshot-browse`, body);
```

### 8. 前端页面 — `web/src/pages/snapshots/snapshots-page.tsx`

恢复 Sheet 中增加快照文件树浏览组件：

**新增组件 `SnapshotTreeBrowser`**（可以内联在 snapshots-page.tsx 或独立文件）：

- 接收 `agentId`、`snapshotId` props
- 调用 `browseSnapshot` API 获取所有条目
- 将扁平的 `SnapshotFileEntry[]` 在前端构建为树形结构
- 渲染为 tree，支持展开/折叠（本地操作，数据已全部加载）
- 每个条目右侧有 Checkbox
- 显示信息：路径名、类型图标（目录/文件）、文件大小、修改时间
- 勾选目录时，该目录下的所有子项自动勾选
- 暴露 `selectedPaths: string[]` 供提交时使用

**恢复按钮逻辑变化**：

- `selectedPaths` 为空 → 按钮文案「恢复全部」，提交时 `include_paths` 不传
- `selectedPaths` 非空 → 按钮文案「恢复选中的 N 项」，提交时携带 `include_paths`

**加载状态处理**：

- 浏览区域默认折叠，带「浏览快照内容」标题，点击展开时触发加载
- 加载中显示 spinner + "正在读取快照内容..."
- 加载失败显示错误提示（agent 离线、超时等）
- Agent 离线时「浏览快照内容」区域显示为禁用状态，提示"需要节点在线"

## 错误处理

| 场景 | 处理方式 |
|------|---------|
| Agent 离线 | 浏览功能不可用，提示"需要节点在线"；恢复仍可排队 |
| restic ls 超时（60s） | 显示错误提示，可重试 |
| restic ls 执行失败 | 显示 agent 返回的错误信息 |
| 快照包含大量文件 | 加载中有 spinner，tree 使用虚拟滚动或限制初始展开层级 |

## 向后兼容

- `IncludePaths` 为 `omitempty`，旧版 agent 不认识此字段会忽略，仍执行全量恢复
- 前端不使用浏览功能时，行为与现有完全一致
- `snapshot_browse_req/resp` 是新增消息类型，不影响现有消息处理

## 测试

- `restic.go`：测试 `LsSnapshot` 的命令构建和 JSONL 解析
- `restic.go`：测试 `buildRestoreCmd` 在有/无 `includePaths` 时的参数差异
- `handler.go`：测试 `handleSnapshotBrowseReq` 的正常流程和错误处理
- `handler.go`：测试 `handleRestoreReq` 透传 `IncludePaths`
- `snapshot_browse.go`：测试 API 层的正常流程、agent 离线、超时
- `restore.go`：测试带 `include_paths` 的恢复请求
