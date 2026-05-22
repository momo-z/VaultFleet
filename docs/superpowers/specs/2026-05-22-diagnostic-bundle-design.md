# 诊断包功能设计文档

## 概述

为 VaultFleet 新增"诊断包"功能，用户可一键生成并下载包含系统状态、Master 日志和 Agent 日志的 ZIP 文件，所有内容自动收集并脱敏。替代当前需要手动从 Docker/journalctl 复制日志粘贴到 GitHub issue 的繁琐流程。

## 背景

当前的 bug 反馈流程（`/system` → "提交 Issue" → GitHub）要求用户手动运行 `docker compose logs` 或 `journalctl`，复制输出后粘贴到 issue 模板中。这一过程容易出错、耗时，且用户经常直接跳过日志部分。一键诊断包可以解决这个问题。

## 用户流程

1. 用户进入 `/system` 页面
2. 看到新的"诊断包"卡片，包含：
   - **在线 Agent** 列表（带勾选框，离线 Agent 灰显不可选）
   - "生成诊断包"按钮
3. 用户可选择需要收集日志的 Agent，点击"生成诊断包"
4. UI 显示进度（正在收集 Master 数据 → 正在收集 Agent 日志 → 正在打包）
5. 浏览器自动下载 `vaultfleet-diagnostic-<timestamp>.zip`
6. 用户将 ZIP 附加到 GitHub issue 或通过其他渠道发送

## 诊断包内容

### ZIP 结构

```
vaultfleet-diagnostic-20260522T143000.zip
├── meta.json                    # 生成时间、VaultFleet 版本、OS/架构
├── master/
│   ├── logs.txt                 # Master 进程日志（环形缓冲区最近 24h）
│   ├── nodes.json               # 所有注册节点及在线状态
│   ├── storage.json             # 存储后端配置（已脱敏）
│   ├── policies.json            # 备份策略列表
│   └── recent_errors.json       # 最近 50 条失败 task 的 error_log
└── agents/
    ├── <agent-name-1>/
    │   └── logs.txt             # Agent 1 最近 24h 日志（最大 5MB，已脱敏）
    └── <agent-name-2>/
        └── logs.txt             # Agent 2 最近 24h 日志（最大 5MB，已脱敏）
```

### 数据来源

| 数据项 | 来源 | 是否已有？ |
|--------|------|-----------|
| 版本、OS/架构 | `GET /api/system/version` + runtime | 已有 |
| 节点列表及状态 | 数据库 `Agent` 模型 | 已有 |
| 存储后端配置 | 数据库 `StorageBackend` 模型 | 已有 |
| 备份策略 | 数据库 `BackupPolicy` 模型 | 已有 |
| 最近任务错误 | 数据库 `TaskHistory` 模型（status=failed） | 已有 |
| Master 日志 | **新增**：环形缓冲区捕获 stdout | 需实现 |
| Agent 日志 | **新增**：WebSocket 命令 `collect_logs` | 需实现 |

## 后端改动

### 1. Master 日志环形缓冲区

**文件：** 新建 `internal/master/logbuf/logbuf.go`

- 实现内存环形缓冲区（`[]byte`，约 2MB 容量），捕获所有 `log` 标准库输出
- Master 启动时，通过 `log.SetOutput` 设置 `MultiWriter`，同时写入 `os.Stdout` 和环形缓冲区
- 提供 `ReadAll() []byte` 方法导出缓冲区内容（从最旧到最新）
- 通过 `sync.Mutex` 保证线程安全

### 2. 新增 API 端点

**文件：** `internal/master/api/diagnostic.go` + 在 `router.go` 中注册

`GET /api/system/diagnostic?agents=<id1>,<id2>`

- 需要认证的受保护路由
- 查询参数 `agents` 为可选的逗号分隔 Agent ID 列表
- 响应：`Content-Type: application/zip`，流式返回 ZIP 文件
- 执行步骤：
  1. 收集本地数据（版本、节点、存储、策略、最近错误）
  2. 读取 Master 日志缓冲区
  3. 对每个请求的 Agent ID：通过 WebSocket 发送 `collect_logs` 命令，等待响应（30 秒超时）
  4. 对所有文本内容执行脱敏
  5. 构建 ZIP 归档并流式返回

### 3. WebSocket 命令：`collect_logs`

**Master 端**（`internal/master/ws/` 或 `internal/master/commands/`）：
- 新增命令类型 `collect_logs`
- 向指定 Agent 发送命令，等待响应，30 秒超时
- 响应载荷：`{ "logs": "<日志文本>" }`

**Agent 端**（`internal/agent/`）：
- 新增 `collect_logs` 命令处理器
- 自动检测 init system：
  - systemd：`journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager`
  - 日志文件回退：读取 `/var/log/vaultfleet-agent.log`，过滤最近 24h
- 截断到最大 5MB
- 发送前执行脱敏
- 通过 WebSocket 回传结果

### 4. 脱敏处理

**共享工具**（Master 和 Agent 共用）：

使用正则表达式将敏感值替换为 `[REDACTED]`：
- `(token|password|passwd|secret|cookie|credential|api_key|access_key|secret_key|private_key|auth)(\s*[=:]\s*)(\S+)` → 保留第 1、2 组，替换第 3 组
- 存储配置 JSON：脱敏 `accessKey`、`secretKey`、`endpoint`、`password` 字段
- Bearer 令牌：`Bearer \S+` → `Bearer [REDACTED]`

## 前端改动

### 系统页 — 新增"诊断包"卡片

**文件：** `web/src/pages/system/system-page.tsx`

**位置：** 与现有"问题反馈"卡片并列，放在 `/system` 页面。

卡片内容：
- 标题："诊断包"，副标题"自动收集系统信息和日志，用于问题排查"
- 在线 Agent 列表，带勾选框（离线 Agent 显示但禁用，带"离线"标签）
- "生成诊断包"按钮
- 生成中：进度指示器 + 状态文本（"正在收集 Master 数据..."、"正在收集 Agent-X 日志..."、"正在打包..."）
- 完成后：自动触发文件下载
- 出错时：toast 通知显示错误信息

### 新增 API 服务

**文件：** `web/src/services/diagnostic.ts`

```typescript
export async function downloadDiagnosticBundle(agentIds: string[]): Promise<Blob> {
  const params = agentIds.length > 0 ? `?agents=${agentIds.join(',')}` : '';
  const response = await fetch(`/api/system/diagnostic${params}`);
  return response.blob();
}
```

## 错误处理与超时

- Agent 日志收集超时：每个 Agent **30 秒**
- Agent 超时：在 ZIP 中该 Agent 目录下放置 `timeout.txt` 标记文件
- Agent 失败：在其目录下放置 `error.txt` 并包含错误信息
- 离线 Agent 不可选（UI 层面阻止）
- Master 数据收集失败：尽量包含可用数据，在 `meta.json` 中记录失败信息
- 总请求超时：60 秒（考虑多个 Agent 的情况）

## 安全考虑

- 诊断包端点需要认证（与其他受保护路由相同）
- 对所有日志内容自动脱敏处理
- 存储配置在导出前剥离凭据信息
- Agent 日志在 Agent 端脱敏后再传输（纵深防御）
- ZIP 文件名仅包含时间戳，不含敏感标识

## 未来扩展

- 下载前预览诊断包内容
- 直接上传到 GitHub issue（需要 GitHub OAuth）
- 在反复失败时自动收集诊断信息
- 包含 Agent 端的 restic/rclone 配置文件（脱敏后）
