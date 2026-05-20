# VaultFleet 本身能力提升 A+ 设计

> 日期：2026-05-20
> 状态：待确认
> 目标：第一期补齐可靠性基础，同时让记录模型后续可以自然演进为备份/恢复记录体系。

## 1. 背景与判断

VaultFleet 当前已经具备 Master、Agent、WebSocket 通信、策略下发、手动备份、恢复、任务历史、快照列表和存储配置等基础能力。和 BackupX 对比后，当前核心短板不是前端页面数量，而是后端能力的可靠性闭环不够完整：

- Master 下发 `backup_now`、`restore_req` 等命令时，命令本身没有持久化记录。
- Agent 离线时命令无法排队，当前请求会直接失败。
- `TaskHistory` 能记录执行结果，但无法完整表达“命令是否送达、是否超时、是否由哪个 policy/storage 触发”。
- 存储配置缺少保存前/保存后的连接测试。
- 缺少标准健康检查和基础监控指标。

本阶段采用 **A+ 路线**：先做可靠性基础，不照搬 BackupX 的全量模型；但把任务记录设计成后续可演进为备份记录、恢复记录的形状。

## 2. 范围

### 2.1 本期必须完成

1. 新增持久化 Agent 命令记录。
2. 增强任务/运行记录字段，支撑后续备份记录页面。
3. 支持 Agent 离线命令排队和上线后投递。
4. 支持命令超时状态闭环。
5. 新增存储连接测试接口。
6. 新增 `/health`、`/ready`、`/metrics`。
7. 保持现有 API 向后兼容。
8. 补齐 Go 单元测试和关键集成测试。

### 2.2 本期明确不做

1. 不做 RBAC、API Key、2FA、WebAuthn。
2. 不做审计日志。
3. 不做验证记录、复制记录、任务模板。
4. 不做 MySQL、PostgreSQL、SQLite、SAP HANA、Backint 等新增备份类型。
5. 不重写前端页面。
6. 不把所有记录一次性拆成 BackupX 式的多张业务表。

这些能力后续按真实使用场景单独设计，避免第一期膨胀。

## 3. 数据模型

### 3.1 新增 `AgentCommand`

新增 `agent_commands` 表，用来记录 Master 对 Agent 发出的可追踪命令。

字段：

| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | text primary key | 命令 ID |
| `agent_id` | text index | 目标 Agent |
| `type` | text index | 命令类型 |
| `status` | text index | 命令状态 |
| `message_id` | text unique/index | WebSocket message ID |
| `payload` | text | 下发给 Agent 的 JSON payload |
| `result` | text | Agent 返回结果摘要 |
| `error_message` | text | 失败或超时原因 |
| `attempts` | integer | 投递次数 |
| `deadline_at` | datetime nullable | 命令超时截止时间 |
| `dispatched_at` | datetime nullable | 最近一次投递时间 |
| `completed_at` | datetime nullable | 完成时间 |
| `created_at` | datetime | 创建时间 |
| `updated_at` | datetime | 更新时间 |

命令状态固定为：

```text
pending
dispatched
running
succeeded
failed
timeout
cancelled
```

第一期实现 `pending`、`dispatched`、`running`、`succeeded`、`failed`、`timeout`。`cancelled` 只作为字段枚举预留，不实现取消 API。

状态含义：

| 状态 | 含义 |
| :--- | :--- |
| `pending` | 命令已创建，尚未成功写入 Agent WebSocket |
| `dispatched` | 命令已成功写入 Agent WebSocket |
| `running` | Agent 已明确回报开始执行，或该命令对应任务已进入执行中 |
| `succeeded` | Agent 回报执行成功 |
| `failed` | Agent 回报执行失败，或 Master 在投递前遇到不可恢复错误 |
| `timeout` | 命令超过 `deadline_at` 仍未完成 |
| `cancelled` | 预留状态，第一期不产生 |

### 3.2 增强 `TaskHistory`

当前 `TaskHistory` 不删除、不改名，避免迁移和 API 破坏。第一期将它扩展成“运行记录”的形状。

新增字段：

| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `command_id` | text index | 关联 `agent_commands.id` |
| `policy_id` | text index nullable | 触发该任务的策略 |
| `storage_id` | text index nullable | 任务使用的存储 |
| `updated_at` | datetime | 更新时间 |

继续保留现有字段：

```text
id
agent_id
type
status
snapshot_id
message_id
started_at
finished_at
duration_ms
repo_size
error_log
created_at
```

`TaskHistory` 在代码中可以逐步使用 `TaskRun` 语义封装，但数据库表名第一期保持不变。后续如果备份记录复杂到需要独立业务对象，再从 `TaskHistory` 拆出 `backup_records` 和 `restore_records`。

## 4. 命令生命周期

### 4.1 命令类型

第一期纳入持久化命令体系：

- `backup_now`
- `restore_req`
- `policy_push`
- `snapshot_list_req`

第一期不纳入持久化命令体系：

- `heartbeat`：这是 Agent 状态上报，不是 Master 命令。
- `dir_browse_req`：这是交互式浏览请求，短时、频繁、用户等待强，不适合离线排队。

### 4.2 下发流程

Master 处理手动备份、恢复、策略下发、快照刷新时，流程统一为：

1. 校验 Agent、policy、storage 等业务输入。
2. 构造 protocol message。
3. 创建 `AgentCommand`，状态为 `pending`。
4. 创建或更新对应 `TaskHistory`，初始状态为 `pending`。
5. 如果 Agent 在线，立即通过 WebSocket 投递。
6. 投递成功后将命令状态更新为 `dispatched`；需要长时间执行的业务任务同时将 `TaskHistory.status` 更新为 `running`。
7. 如果 Agent 离线，命令保留为 `pending`。
8. Agent 返回结果后更新 `AgentCommand` 和 `TaskHistory`。

### 4.3 Agent 重连投递

Agent WebSocket 连接建立后，Master 除了现有 policy lookup 外，还需要扫描该 Agent 的可投递命令：

```text
status in (pending, dispatched)
deadline_at is null or deadline_at > now
```

投递规则：

- 按 `created_at ASC` 投递，保证命令顺序稳定。
- 每次投递增加 `attempts`。
- 投递成功后写入 `dispatched_at`。
- 投递失败不删除命令，等待下次重连或后台扫描处理。

同一 Agent 同一时间不做复杂并发队列调度。第一期保持顺序投递，避免 Agent 同时执行多个备份/恢复导致资源争用。

### 4.4 结果处理

Agent 已经通过 `task_result` 返回任务结果。第一期结果处理规则：

1. 优先用 `message_id` 找到 `AgentCommand`。
2. 找到命令后，根据结果状态更新命令为 `succeeded` 或 `failed`。
3. 根据 `command_id` 或 `message_id` 找到 `TaskHistory` 并更新状态、耗时、snapshot、repo size、错误日志。
4. 如果结果包含 snapshots，则继续写入现有 `snapshots` 表。
5. 找不到命令时，保留现有兼容行为：仍写入 `TaskHistory`，但不关联 `command_id`。

### 4.5 超时处理

新增后台扫描器，周期性查找：

```text
status in (pending, dispatched, running)
deadline_at <= now
```

命中后：

- `AgentCommand.status = timeout`
- `AgentCommand.error_message = "command timeout"`
- 关联 `TaskHistory.status = timeout`
- 写入 `finished_at`、`duration_ms`、`error_log`

默认超时时间：

- `backup_now`：6 小时
- `restore_req`：6 小时
- `policy_push`：5 分钟
- `snapshot_list_req`：2 分钟

这些默认值第一期写成常量，不做 UI 配置。

## 5. 任务/记录生命周期

### 5.1 手动备份

`POST /api/agents/:id/backup-now`：

1. Agent 存在即可接受请求。
2. 如果 Agent 在线，命令立即下发。
3. 如果 Agent 离线，命令进入 `pending`。
4. API 返回 `202 Accepted`，包含 `command_id` 和 `message_id`。
5. 前端通过 `GET /api/tasks` 或新增命令查询接口查看状态。

手动备份不再因为 Agent 离线直接丢失请求。

### 5.2 恢复

`POST /api/agents/:id/restore`：

1. 校验 `snapshot_id` 和 `target_path`。
2. Agent 存在即可接受请求。
3. 创建 restore 类型的 `AgentCommand` 和 `TaskHistory`。
4. 在线立即投递，离线排队。
5. Agent 返回 `task_result` 后完成状态闭环。

恢复命令具有较高风险，但第一期不做取消和审批。前端仍需要保留确认机制。

### 5.3 策略下发

当前 `policy_push` 是连接后通过 `CurrentPolicyLookup` 查找未同步策略并直接写 WebSocket。第一期改为：

1. policy 变更后仍将 `BackupPolicy.synced = false`。
2. 后台或连接处理流程为未同步策略创建 `policy_push` 命令。
3. Agent ack 成功后：
   - 命令状态更新为 `succeeded`
   - `BackupPolicy.synced = true`
4. Agent ack 失败后：
   - 命令状态更新为 `failed`
   - `BackupPolicy.synced` 保持 `false`

保留现有 ack 安全校验：只允许认证 Agent 确认自己的 policy。

### 5.4 快照刷新

`snapshot_list_req` 当前使用 `SendAndWait` 等待响应。第一期保留同步等待 API 体验，但底层增加命令记录：

1. 创建 `snapshot_list_req` 命令。
2. 在线时投递并等待短超时响应。
3. 响应成功后更新命令为 `succeeded`，写入 snapshots。
4. 超时后更新命令为 `timeout`。

离线时返回 `202 Accepted`，命令保持 `pending`。前端后续通过命令查询接口展示“已排队”，不把离线快照刷新视为立即失败。

## 6. 存储连接测试

### 6.1 API

新增接口：

```text
POST /api/storage/test
POST /api/storage/:id/test
```

`POST /api/storage/test` 用于测试未保存配置，请求体：

```json
{
  "rclone_type": "s3",
  "rclone_config": {
    "provider": "AWS",
    "access_key_id": "...",
    "secret_access_key": "...",
    "region": "ap-east-1"
  }
}
```

`POST /api/storage/:id/test` 用于测试已保存配置，不需要请求体。

响应：

```json
{
  "ok": true,
  "data": {
    "ok": true,
    "latency_ms": 123,
    "checked_at": "2026-05-20T12:00:00Z"
  }
}
```

失败响应：

```json
{
  "ok": true,
  "data": {
    "ok": false,
    "latency_ms": 123,
    "error": "connection refused",
    "checked_at": "2026-05-20T12:00:00Z"
  }
}
```

接口级错误只用于请求非法、存储不存在、解密失败等系统错误。远端连接失败属于测试结果，使用 `200 OK` 返回 `data.ok = false`。

### 6.2 执行方式

存储测试服务负责：

1. 将 rclone 配置写入临时目录。
2. 使用短超时执行最小连接检查。
3. 删除临时配置。
4. 返回耗时和错误摘要。

默认超时：15 秒。

第一期检查命令采用 rclone 的轻量只读操作，例如：

```text
rclone lsd <remote>:
```

具体 remote 名称由测试服务临时生成。测试服务不得把未保存配置写入数据库，不得在错误信息中泄漏 secret、token、password 等敏感字段。

## 7. 健康检查与指标

### 7.1 `/health`

公开接口，不需要登录。

语义：进程存活。

响应：

```json
{
  "ok": true,
  "status": "healthy"
}
```

`/health` 不检查数据库，不读取 master key。

### 7.2 `/ready`

公开接口，不需要登录。

语义：服务是否准备好处理业务请求。

检查项：

- 数据库可 ping。
- AutoMigrate 已完成。
- master key 已加载。
- 数据目录可访问。

响应：

```json
{
  "ok": true,
  "status": "ready"
}
```

任一检查失败返回 `503 Service Unavailable`。

### 7.3 `/metrics`

公开接口，不需要登录。

第一期使用 Prometheus text format，避免后续再改监控生态接口。

指标：

```text
vaultfleet_agents_total
vaultfleet_agents_online
vaultfleet_agent_commands_total{status,type}
vaultfleet_tasks_total{status,type}
vaultfleet_last_successful_backup_timestamp_seconds
```

指标从数据库和 Hub 状态读取。读取失败时返回 `500`，并输出简短错误，不泄漏敏感配置。

## 8. API 兼容性

现有接口保持可用：

```text
POST /api/agents/:id/backup-now
POST /api/agents/:id/restore
GET /api/tasks
```

返回内容增加字段：

```text
command_id
message_id
```

新增接口：

```text
GET /api/commands/:id
GET /api/agents/:id/commands
POST /api/storage/test
POST /api/storage/:id/test
GET /health
GET /ready
GET /metrics
```

`GET /api/commands/:id` 返回单条命令详情。  
`GET /api/agents/:id/commands` 支持 `status`、`type`、`limit` 查询参数。

## 9. 错误处理与安全

1. Agent 离线不再等同于业务失败；可排队命令返回 `202 Accepted`。
2. 命令投递失败记录到 `AgentCommand.error_message`。
3. Agent 执行失败记录到 `TaskHistory.error_log` 和 `AgentCommand.result/error_message`。
4. 存储测试必须脱敏错误信息。
5. API 响应不得返回 `restic_password`、rclone secret、agent token。
6. WebSocket 返回结果必须使用认证 Agent ID，不信任 payload 中的 `agent_id`。
7. 同一 Agent 的命令第一期按创建时间顺序投递，不做并发执行。

## 10. 后续演进边界

第一期完成后，B 路线按真实需要分阶段推进：

1. **备份记录**：高优先级。当前增强后的 `TaskHistory` 已经能支撑基础页面；当需要更丰富的 snapshot、repo、retention、校验信息时，再拆 `backup_records`。
2. **恢复记录**：中高优先级。等恢复进度、文件数、目标路径审计等需求明确后拆出。
3. **验证记录**：等实现 `restic check` 或定期校验后再做。
4. **复制记录**：等实现跨存储复制后再做。
5. **审计/RBAC/API Key**：等多用户和公开部署场景明确后再做。

禁止为了追齐 BackupX 页面数量而提前引入无业务闭环的表和接口。

## 11. 测试要求

必须新增或更新 Go 测试覆盖：

1. Agent 在线时，`backup_now` 先落库再下发。
2. Agent 离线时，`backup_now` 返回 `202`，命令保持 `pending`。
3. Agent 重连后，pending 命令被投递。
4. Agent 返回 `task_result` 后，命令和任务记录同步更新为成功或失败。
5. 命令超时扫描将命令和任务记录更新为 `timeout`。
6. `restore_req` 创建命令和任务记录。
7. `policy_push` ack 成功后命令成功且 policy synced。
8. `policy_push` ack 失败后命令失败且 policy 保持 unsynced。
9. 存储测试未保存配置时不写数据库。
10. 存储测试错误信息不泄漏敏感字段。
11. `/health` 不依赖数据库。
12. `/ready` 在数据库不可用时返回 `503`。
13. `/metrics` 输出 Prometheus 文本并包含基础指标。
14. 现有 `GET /api/tasks` 兼容旧字段。

验收命令：

```bash
go test ./... -count=1
```

如果后续实现改动了前端类型或页面，还需要运行：

```bash
cd web && npm test
cd web && npm run build
```

## 12. 验收标准

1. 手动备份和恢复不再因为 Agent 离线而丢失命令。
2. 用户能查询命令状态，区分排队、已投递、执行中、成功、失败、超时。
3. 任务历史能关联命令、策略和存储。
4. 存储配置能在保存前或保存后测试连接。
5. 健康检查和指标接口可用于容器、反代和监控系统。
6. 现有 API 和测试保持兼容。
7. 数据模型为后续备份记录/恢复记录演进留出空间，但本期不引入无用复杂表。
