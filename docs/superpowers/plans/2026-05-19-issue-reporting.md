# VaultFleet 问题反馈流程 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 VaultFleet 增加 GitHub Issue Forms、问题反馈支持文档和 README 入口，让用户不用在 VaultFleet 内登录 GitHub 也能提交结构化问题报告。

**Architecture:** 这是一个纯仓库元数据和文档改动，不改 Go 运行时代码。`.github/ISSUE_TEMPLATE` 负责 GitHub issue chooser 和表单字段，`docs/support.md` 负责日志收集与脱敏指南，`README.md` 只保留入口链接，避免内容重复。

**Tech Stack:** GitHub Issue Forms YAML、Markdown、Python + PyYAML 用于本地 YAML 语法验证。

---

## 范围检查

该 spec 只覆盖一个子系统：问题反馈入口和静态文档。它不涉及 GitHub OAuth、Web UI、Master-Agent 协议、诊断包导出或运行时日志改造，因此适合一个实施计划完成。

## 文件结构

- 创建 `.github/ISSUE_TEMPLATE/bug_report.yml`：GitHub Bug report 表单，字段中英双语，自动打 `bug` 和 `needs-triage` 标签。
- 创建 `.github/ISSUE_TEMPLATE/support_request.yml`：GitHub Support request 表单，字段中英双语，自动打 `support` 和 `needs-triage` 标签。
- 创建 `.github/ISSUE_TEMPLATE/config.yml`：Issue chooser 配置，禁用空白 issue，并链接支持文档。
- 创建 `docs/support.md`：中英双语问题提交、日志收集和脱敏指南。
- 修改 `README.md`：新增简短“反馈问题 / Report an issue”入口，链接到支持文档和 GitHub issue chooser。

---

### Task 1: 新增 GitHub Issue Forms

**Files:**
- Create: `.github/ISSUE_TEMPLATE/bug_report.yml`
- Create: `.github/ISSUE_TEMPLATE/support_request.yml`
- Create: `.github/ISSUE_TEMPLATE/config.yml`

- [ ] **Step 1: 创建 Issue Forms 目录**

Run:

```bash
mkdir -p .github/ISSUE_TEMPLATE
```

Expected: command exits with status 0.

- [ ] **Step 2: 添加 Bug report 表单**

Use `apply_patch`:

```diff
*** Begin Patch
*** Add File: .github/ISSUE_TEMPLATE/bug_report.yml
+name: Bug report / 缺陷反馈
+description: Report a reproducible VaultFleet bug. / 提交可复现的 VaultFleet 缺陷。
+title: "[Bug]: "
+labels:
+  - bug
+  - needs-triage
+body:
+  - type: markdown
+    attributes:
+      value: |
+        Thanks for reporting a VaultFleet bug.
+
+        感谢反馈 VaultFleet 缺陷。请尽量填写完整信息，并在提交前脱敏 token、密码、cookie、rclone 凭据和私有 endpoint。
+
+  - type: input
+    id: summary
+    attributes:
+      label: Summary / 问题摘要
+      description: What broke? / 哪个功能出问题了？
+      placeholder: Backup fails when pushing to an S3-compatible backend / 备份写入 S3 兼容存储时失败
+    validations:
+      required: true
+
+  - type: input
+    id: version
+    attributes:
+      label: VaultFleet version / VaultFleet 版本
+      description: Provide the Docker image tag, release tag, or commit SHA. / 请填写 Docker 镜像 tag、release tag 或 commit SHA。
+      placeholder: ghcr.io/momo-z/vaultfleet:v0.1.0 or commit abc1234
+    validations:
+      required: true
+
+  - type: dropdown
+    id: area
+    attributes:
+      label: Affected area / 影响范围
+      description: Pick the closest area. / 选择最接近的问题范围。
+      options:
+        - Master / 主控
+        - Agent / 节点
+        - Web UI / 网页界面
+        - Installation / 安装
+        - Backup / 备份
+        - Restore / 恢复
+        - Snapshots / 快照
+        - Policy sync / 策略同步
+        - Storage backend / 存储后端
+        - Notifications / 通知
+        - Other / 其他
+    validations:
+      required: true
+
+  - type: dropdown
+    id: deployment
+    attributes:
+      label: Deployment method / 部署方式
+      description: How is VaultFleet running? / VaultFleet 是如何运行的？
+      options:
+        - Docker Compose
+        - Docker
+        - Source build / 源码运行
+        - Other / 其他
+    validations:
+      required: true
+
+  - type: textarea
+    id: master_environment
+    attributes:
+      label: Master environment / Master 环境
+      description: Include OS, architecture, reverse proxy, and browser if this is a UI issue. / 请包含系统、架构、反向代理；如果是 UI 问题，也请包含浏览器。
+      placeholder: |
+        OS: Ubuntu 24.04
+        Architecture: amd64
+        Reverse proxy: nginx + HTTPS
+        Browser: Chrome 125
+    validations:
+      required: true
+
+  - type: textarea
+    id: agent_environment
+    attributes:
+      label: Agent environment / Agent 环境
+      description: Include OS, architecture, init system, and installation method. / 请包含系统、架构、init system 和安装方式。
+      placeholder: |
+        OS: Debian 12
+        Architecture: arm64
+        Init system: systemd
+        Install method: install.sh from Master
+    validations:
+      required: false
+
+  - type: textarea
+    id: reproduction
+    attributes:
+      label: Reproduction steps / 复现步骤
+      description: List exact steps. / 请列出明确步骤。
+      placeholder: |
+        1. Create a storage config with ...
+        2. Create a backup policy for ...
+        3. Click "Backup now"
+        4. See the failure in task history
+    validations:
+      required: true
+
+  - type: textarea
+    id: expected
+    attributes:
+      label: Expected behavior / 预期行为
+      description: What should have happened? / 你预期应该发生什么？
+      placeholder: Backup should finish successfully and create a snapshot. / 备份应成功完成并创建快照。
+    validations:
+      required: true
+
+  - type: textarea
+    id: actual
+    attributes:
+      label: Actual behavior / 实际行为
+      description: What actually happened? / 实际发生了什么？
+      placeholder: Task failed with repository lock error. / 任务因仓库锁错误失败。
+    validations:
+      required: true
+
+  - type: textarea
+    id: master_logs
+    attributes:
+      label: Master logs / Master 日志
+      description: Paste relevant logs from Docker, Docker Compose, or your process manager. / 粘贴 Docker、Docker Compose 或进程管理器中的相关日志。
+      render: shell
+      placeholder: docker compose logs --tail=300 vaultfleet
+    validations:
+      required: false
+
+  - type: textarea
+    id: agent_logs
+    attributes:
+      label: Agent logs / Agent 日志
+      description: Paste relevant Agent logs from systemd, OpenRC, or fallback log file. / 粘贴 systemd、OpenRC 或 fallback 日志文件中的相关 Agent 日志。
+      render: shell
+      placeholder: journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager
+    validations:
+      required: false
+
+  - type: textarea
+    id: task_error_log
+    attributes:
+      label: Task error_log / 任务 error_log
+      description: If the bug involves backup, restore, snapshots, or policy sync, paste the failed task error_log. / 如果问题涉及备份、恢复、快照或策略同步，请粘贴失败任务的 error_log。
+      render: shell
+      placeholder: backup: run restic backup: exit status 1: ...
+    validations:
+      required: false
+
+  - type: textarea
+    id: screenshots
+    attributes:
+      label: Screenshots or extra context / 截图或补充信息
+      description: Add screenshots or other context if useful. / 如有帮助，请补充截图或其他上下文。
+    validations:
+      required: false
+
+  - type: checkboxes
+    id: redaction
+    attributes:
+      label: Redaction confirmation / 脱敏确认
+      description: Confirm that secrets were removed before submitting. / 提交前请确认已经移除敏感信息。
+      options:
+        - label: I have redacted enrollment tokens, agent tokens, cookies, restic passwords, rclone credentials, storage secrets, and private endpoints where needed. / 我已经按需脱敏 enrollment token、agent token、cookie、restic password、rclone 凭据、存储密钥和私有 endpoint。
+          required: true
+*** End Patch
```

- [ ] **Step 3: 添加 Support request 表单**

Use `apply_patch`:

```diff
*** Begin Patch
*** Add File: .github/ISSUE_TEMPLATE/support_request.yml
+name: Support request / 使用支持
+description: Ask for help with setup, configuration, storage, backup, or restore. / 请求安装、配置、存储、备份或恢复相关帮助。
+title: "[Support]: "
+labels:
+  - support
+  - needs-triage
+body:
+  - type: markdown
+    attributes:
+      value: |
+        Use this form when you are not sure whether the behavior is a product bug.
+
+        如果你不确定当前问题是否是产品缺陷，请使用这个表单。提交前请先脱敏 token、密码、cookie、rclone 凭据和私有 endpoint。
+
+  - type: input
+    id: goal
+    attributes:
+      label: Goal / 你想完成什么
+      description: Describe the operation or setup you are trying to complete. / 描述你正在尝试完成的操作或部署目标。
+      placeholder: Configure WebDAV storage and run the first backup / 配置 WebDAV 存储并执行第一次备份
+    validations:
+      required: true
+
+  - type: input
+    id: version
+    attributes:
+      label: VaultFleet version / VaultFleet 版本
+      description: Provide the Docker image tag, release tag, or commit SHA. / 请填写 Docker 镜像 tag、release tag 或 commit SHA。
+      placeholder: ghcr.io/momo-z/vaultfleet:latest or commit abc1234
+    validations:
+      required: true
+
+  - type: dropdown
+    id: topic
+    attributes:
+      label: Topic / 问题类型
+      description: Pick the closest topic. / 选择最接近的问题类型。
+      options:
+        - Installation / 安装
+        - Master setup / Master 配置
+        - Agent enrollment / Agent 注册
+        - Storage configuration / 存储配置
+        - Backup policy / 备份策略
+        - Restore / 恢复
+        - Notifications / 通知
+        - Release or upgrade / 发布或升级
+        - Other / 其他
+    validations:
+      required: true
+
+  - type: dropdown
+    id: deployment
+    attributes:
+      label: Deployment method / 部署方式
+      description: How is VaultFleet running? / VaultFleet 是如何运行的？
+      options:
+        - Docker Compose
+        - Docker
+        - Source build / 源码运行
+        - Other / 其他
+    validations:
+      required: true
+
+  - type: textarea
+    id: current_state
+    attributes:
+      label: Current state / 当前状态
+      description: What has already been installed or configured? / 当前已经安装或配置到哪一步？
+      placeholder: |
+        Master is running in Docker Compose.
+        One Agent was created in the UI.
+        Agent installation command fails during enrollment.
+    validations:
+      required: true
+
+  - type: textarea
+    id: storage_backend
+    attributes:
+      label: Storage backend / 存储后端
+      description: Name the backend type only. Do not paste access keys or secrets. / 只填写后端类型，不要粘贴 access key 或 secret。
+      placeholder: S3-compatible storage, Cloudflare R2, MinIO, WebDAV, SFTP, or other
+    validations:
+      required: false
+
+  - type: textarea
+    id: environment
+    attributes:
+      label: Environment / 环境信息
+      description: Include Master and Agent OS, architecture, and init system when relevant. / 请包含相关 Master 和 Agent 的系统、架构和 init system。
+      placeholder: |
+        Master: Ubuntu 24.04, amd64, Docker Compose
+        Agent: Debian 12, arm64, systemd
+    validations:
+      required: true
+
+  - type: textarea
+    id: tried
+    attributes:
+      label: What you already tried / 已经尝试过什么
+      description: Include commands, UI actions, or documentation steps. / 请列出执行过的命令、UI 操作或文档步骤。
+      placeholder: |
+        1. Ran docker compose up -d
+        2. Created an Agent in the UI
+        3. Ran the install.sh command on the target server
+    validations:
+      required: true
+
+  - type: textarea
+    id: logs
+    attributes:
+      label: Relevant logs / 相关日志
+      description: Paste Master logs, Agent logs, or task error_log if relevant. / 如相关，请粘贴 Master 日志、Agent 日志或任务 error_log。
+      render: shell
+      placeholder: journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager
+    validations:
+      required: false
+
+  - type: textarea
+    id: extra_context
+    attributes:
+      label: Extra context / 补充信息
+      description: Add any other detail that may help. / 补充其他可能有帮助的信息。
+    validations:
+      required: false
+
+  - type: checkboxes
+    id: redaction
+    attributes:
+      label: Redaction confirmation / 脱敏确认
+      description: Confirm that secrets were removed before submitting. / 提交前请确认已经移除敏感信息。
+      options:
+        - label: I have redacted enrollment tokens, agent tokens, cookies, restic passwords, rclone credentials, storage secrets, and private endpoints where needed. / 我已经按需脱敏 enrollment token、agent token、cookie、restic password、rclone 凭据、存储密钥和私有 endpoint。
+          required: true
+*** End Patch
```

- [ ] **Step 4: 添加 Issue chooser 配置**

Use `apply_patch`:

```diff
*** Begin Patch
*** Add File: .github/ISSUE_TEMPLATE/config.yml
+blank_issues_enabled: false
+contact_links:
+  - name: Troubleshooting and log collection guide / 排障和日志收集指南
+    url: https://github.com/momo-z/VaultFleet/blob/main/docs/support.md
+    about: Read this before opening an issue. / 提交 issue 前请先阅读这份指南。
+*** End Patch
```

- [ ] **Step 5: 验证 Issue Form YAML 可解析并包含关键字段**

Run:

```bash
python3 - <<'PY'
from pathlib import Path
import yaml

files = [
    Path(".github/ISSUE_TEMPLATE/bug_report.yml"),
    Path(".github/ISSUE_TEMPLATE/support_request.yml"),
    Path(".github/ISSUE_TEMPLATE/config.yml"),
]

for path in files:
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise SystemExit(f"{path} did not parse to a mapping")
    if path.name.endswith(".yml") and path.name != "config.yml":
        for key in ("name", "description", "title", "labels", "body"):
            if key not in data:
                raise SystemExit(f"{path} missing {key}")
        body_ids = {item.get("id") for item in data["body"] if isinstance(item, dict)}
        if "redaction" not in body_ids:
            raise SystemExit(f"{path} missing redaction checkbox")

config = yaml.safe_load(Path(".github/ISSUE_TEMPLATE/config.yml").read_text(encoding="utf-8"))
if config.get("blank_issues_enabled") is not False:
    raise SystemExit("blank issues must be disabled")
if not config.get("contact_links"):
    raise SystemExit("config.yml must include contact_links")

print("issue template YAML OK")
PY
```

Expected:

```text
issue template YAML OK
```

- [ ] **Step 6: 提交 Issue Forms**

Run:

```bash
git add .github/ISSUE_TEMPLATE/bug_report.yml .github/ISSUE_TEMPLATE/support_request.yml .github/ISSUE_TEMPLATE/config.yml
git commit -m "docs: add issue report templates"
```

Expected: commit succeeds and includes the three `.github/ISSUE_TEMPLATE` files.

---

### Task 2: 新增支持和日志收集文档

**Files:**
- Create: `docs/support.md`

- [ ] **Step 1: 创建 `docs/support.md`**

Use `apply_patch`:

```diff
*** Begin Patch
*** Add File: docs/support.md
+# 问题反馈和日志收集指南 / Support and Log Collection Guide
+
+**语言 / Language:** 中文 | [English](#english)
+
+这份文档说明如何向 VaultFleet 提交问题，以及提交前应该收集哪些日志。VaultFleet 不会连接、
+读取或保存你的 GitHub 账号。提交 issue 时使用的是你浏览器里已经登录的 GitHub 账号。
+
+## 选择反馈类型
+
+- 可稳定复现的程序缺陷，请提交 [Bug report](https://github.com/momo-z/VaultFleet/issues/new?template=bug_report.yml)。
+- 安装、配置、存储后端、备份策略或恢复操作的问题，请提交 [Support request](https://github.com/momo-z/VaultFleet/issues/new?template=support_request.yml)。
+- 不确定选哪一个时，打开 [Issue chooser](https://github.com/momo-z/VaultFleet/issues/new/choose)。
+
+## 提交前请收集的信息
+
+尽量提供：
+
+- VaultFleet 版本、Docker 镜像 tag 或 commit。
+- Master 的部署方式，例如 Docker Compose、Docker 或源码运行。
+- Master 系统、CPU 架构、是否使用反向代理。
+- Agent 系统、CPU 架构、init system 和安装方式。
+- 复现步骤或你已经尝试过的操作。
+- 失败任务的 `error_log`，尤其是备份、恢复、快照刷新和策略同步问题。
+- Master 日志和 Agent 日志。
+
+## Master 日志
+
+Docker Compose 部署：
+
+```bash
+docker compose logs --tail=300 vaultfleet
+```
+
+Docker 直接部署：
+
+```bash
+docker logs --tail=300 vaultfleet
+```
+
+如果你用源码或自定义进程管理器运行 Master，请复制对应进程管理器里的最近 300 行日志。
+
+## Agent 日志
+
+systemd：
+
+```bash
+journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager
+```
+
+OpenRC：
+
+```bash
+rc-service vaultfleet-agent status
+```
+
+没有受支持 init system 时，安装脚本会用 `nohup` 启动 Agent，并写入 fallback 日志：
+
+```bash
+tail -n 300 /var/log/vaultfleet-agent.log
+```
+
+## 任务历史和 error_log
+
+如果 Web UI 可用，请打开任务历史，复制相关失败任务的 `error_log`。
+
+如果需要通过 API 查看任务历史，请在已登录的浏览器或已带认证 cookie 的请求中访问：
+
+```text
+GET /api/tasks
+GET /api/tasks?agent_id=<agent-id>&status=failed
+```
+
+只粘贴相关失败任务，不要上传完整数据库。
+
+## 必须脱敏的内容
+
+提交 issue 前，请不要上传或粘贴：
+
+- `/data/master.key`
+- 完整的 `/data/vaultfleet.db`
+- 完整的 `/etc/vaultfleet/agent.yaml`
+
+请脱敏：
+
+- enrollment token，例如 `ek_xxx`
+- agent token
+- 登录 cookie
+- restic password
+- rclone access key 和 secret key
+- WebDAV、SFTP、对象存储和通知服务凭据
+- 敏感的私有 endpoint、内网地址或路径
+
+推荐写法：
+
+```text
+agent_token: <redacted>
+secret_access_key: <redacted>
+endpoint: https://<redacted>.example.com
+```
+
+## English
+
+This document explains how to report VaultFleet issues and what logs to collect first.
+VaultFleet does not connect to, read, or store your GitHub account. GitHub issues are
+submitted with the GitHub account already signed in through your browser.
+
+## Choose The Issue Type
+
+- For reproducible product defects, open a [Bug report](https://github.com/momo-z/VaultFleet/issues/new?template=bug_report.yml).
+- For setup, configuration, storage backend, backup policy, or restore questions, open a [Support request](https://github.com/momo-z/VaultFleet/issues/new?template=support_request.yml).
+- If unsure, use the [Issue chooser](https://github.com/momo-z/VaultFleet/issues/new/choose).
+
+## Collect Before Posting
+
+Try to include:
+
+- VaultFleet version, Docker image tag, or commit.
+- Master deployment method, such as Docker Compose, Docker, or source build.
+- Master OS, CPU architecture, and reverse proxy if any.
+- Agent OS, CPU architecture, init system, and installation method.
+- Reproduction steps or actions already tried.
+- Failed task `error_log`, especially for backup, restore, snapshot refresh, and policy sync issues.
+- Master logs and Agent logs.
+
+## Master Logs
+
+Docker Compose:
+
+```bash
+docker compose logs --tail=300 vaultfleet
+```
+
+Docker:
+
+```bash
+docker logs --tail=300 vaultfleet
+```
+
+If you run Master from source or a custom process manager, copy the latest 300 log lines from that process manager.
+
+## Agent Logs
+
+systemd:
+
+```bash
+journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager
+```
+
+OpenRC:
+
+```bash
+rc-service vaultfleet-agent status
+```
+
+When no supported init system is available, the installer starts the Agent with `nohup` and writes fallback logs:
+
+```bash
+tail -n 300 /var/log/vaultfleet-agent.log
+```
+
+## Task History And error_log
+
+If the Web UI is available, open task history and copy the relevant failed task `error_log`.
+
+If you need to use the API, call these endpoints from an authenticated browser/session:
+
+```text
+GET /api/tasks
+GET /api/tasks?agent_id=<agent-id>&status=failed
+```
+
+Paste only relevant failed tasks. Do not upload the full database.
+
+## Redaction Rules
+
+Do not upload or paste:
+
+- `/data/master.key`
+- The full `/data/vaultfleet.db`
+- The full `/etc/vaultfleet/agent.yaml`
+
+Redact:
+
+- enrollment tokens, such as `ek_xxx`
+- agent tokens
+- login cookies
+- restic passwords
+- rclone access keys and secret keys
+- WebDAV, SFTP, object storage, and notification credentials
+- sensitive private endpoints, internal addresses, or paths
+
+Recommended format:
+
+```text
+agent_token: <redacted>
+secret_access_key: <redacted>
+endpoint: https://<redacted>.example.com
+```
+*** End Patch
```

- [ ] **Step 2: 验证支持文档包含关键命令和脱敏规则**

Run:

```bash
rg -n "docker compose logs --tail=300 vaultfleet|docker logs --tail=300 vaultfleet|journalctl -u vaultfleet-agent|/var/log/vaultfleet-agent.log|/data/master.key|/data/vaultfleet.db|/etc/vaultfleet/agent.yaml|issues/new/choose" docs/support.md
```

Expected: output includes each searched command, sensitive file path, and issue chooser link.

- [ ] **Step 3: 提交支持文档**

Run:

```bash
git add docs/support.md
git commit -m "docs: add support and log collection guide"
```

Expected: commit succeeds and includes `docs/support.md`.

---

### Task 3: 在 README 增加反馈入口

**Files:**
- Modify: `README.md`

- [ ] **Step 1: 在中文 README 正文添加反馈入口**

Use `apply_patch` to insert this section after the existing “详细设计见” list and before `## 参考`:

```diff
*** Begin Patch
*** Update File: README.md
@@
 - `docs/superpowers/specs/2026-05-18-vaultfleet-design.md`
 - `docs/superpowers/specs/2026-05-19-vaultfleet-e2e-acceptance-test.md`
 
+## 反馈问题 / Report an issue
+
+遇到 bug 或需要排障支持时，请先阅读 [问题反馈和日志收集指南](docs/support.md)。
+
+提交问题时使用 GitHub Issue 表单：
+
+- [选择 Issue 类型](https://github.com/momo-z/VaultFleet/issues/new/choose)
+- [Bug report](https://github.com/momo-z/VaultFleet/issues/new?template=bug_report.yml)
+- [Support request](https://github.com/momo-z/VaultFleet/issues/new?template=support_request.yml)
+
+VaultFleet 不会连接或保存你的 GitHub 账号；提交账号由浏览器里的 GitHub 登录态决定。发布日志前请按指南脱敏 token、密码和存储凭据。
+
 ## 参考
*** End Patch
```

- [ ] **Step 2: 验证 README 链接入口存在**

Run:

```bash
rg -n "反馈问题 / Report an issue|docs/support.md|issues/new/choose|bug_report.yml|support_request.yml" README.md
```

Expected: output includes the new heading, support guide link, issue chooser link, and both direct template links.

- [ ] **Step 3: 提交 README 更新**

Run:

```bash
git add README.md
git commit -m "docs: link issue reporting guide from readme"
```

Expected: commit succeeds and includes only `README.md`.

---

### Task 4: 最终验证

**Files:**
- Verify: `.github/ISSUE_TEMPLATE/bug_report.yml`
- Verify: `.github/ISSUE_TEMPLATE/support_request.yml`
- Verify: `.github/ISSUE_TEMPLATE/config.yml`
- Verify: `docs/support.md`
- Verify: `README.md`

- [ ] **Step 1: 重新验证所有 Issue Form YAML**

Run:

```bash
python3 - <<'PY'
from pathlib import Path
import yaml

templates = [
    Path(".github/ISSUE_TEMPLATE/bug_report.yml"),
    Path(".github/ISSUE_TEMPLATE/support_request.yml"),
]

for path in templates:
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    assert data["name"]
    assert data["description"]
    assert data["title"]
    assert isinstance(data["labels"], list) and data["labels"]
    assert isinstance(data["body"], list) and data["body"]
    ids = {item.get("id") for item in data["body"] if isinstance(item, dict)}
    assert "redaction" in ids

config = yaml.safe_load(Path(".github/ISSUE_TEMPLATE/config.yml").read_text(encoding="utf-8"))
assert config["blank_issues_enabled"] is False
assert config["contact_links"][0]["url"].endswith("/docs/support.md")

print("issue reporting files OK")
PY
```

Expected:

```text
issue reporting files OK
```

- [ ] **Step 2: 检查文档链接和日志命令**

Run:

```bash
rg -n "issues/new/choose|issues/new\\?template=bug_report.yml|issues/new\\?template=support_request.yml|docker compose logs --tail=300 vaultfleet|journalctl -u vaultfleet-agent|/data/master.key|/etc/vaultfleet/agent.yaml" README.md docs/support.md .github/ISSUE_TEMPLATE
```

Expected: output includes references in README, support docs, and issue templates; it must not show any real token or real credential value.

- [ ] **Step 3: 检查 Markdown/YAML 空白错误**

Run:

```bash
git diff --check HEAD~3..HEAD
```

Expected: no output and exit status 0.

- [ ] **Step 4: 确认工作区干净**

Run:

```bash
git status --short
```

Expected: no output.

## 参考资料

- GitHub Issue Forms 语法：`https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/syntax-for-issue-forms`
- GitHub Issue URL query parameters：`https://docs.github.com/articles/about-automation-for-issues-and-pull-requests-with-query-parameters`
