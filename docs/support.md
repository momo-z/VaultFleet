# 问题反馈和日志收集指南 / Support and Log Collection Guide

**语言 / Language:** 中文 | [English](#english)

这份文档说明如何向 VaultFleet 提交问题，以及提交前应该收集哪些日志。VaultFleet 不会连接、
读取或保存你的 GitHub 账号。提交 issue 时使用的是你浏览器里已经登录的 GitHub 账号。

## 选择反馈类型

- 可稳定复现的程序缺陷，请提交 [Bug report](https://github.com/momo-z/VaultFleet/issues/new?template=bug_report.yml)。
- 安装、配置、存储后端、备份策略或恢复操作的问题，请提交 [Support request](https://github.com/momo-z/VaultFleet/issues/new?template=support_request.yml)。
- 不确定选哪一个时，打开 [Issue chooser](https://github.com/momo-z/VaultFleet/issues/new/choose)。

## 提交前请收集的信息

尽量提供：

- VaultFleet 版本、Docker 镜像 tag 或 commit。
- Master 的部署方式，例如 Docker Compose、Docker 或源码运行。
- Master 系统、CPU 架构、是否使用反向代理。
- Agent 系统、CPU 架构、init system 和安装方式。
- 复现步骤或你已经尝试过的操作。
- 失败任务的 `error_log`，尤其是备份、恢复、快照刷新和策略同步问题。
- Master 日志和 Agent 日志。

## Master 日志

Docker Compose 部署：

```bash
docker compose logs --tail=300 vaultfleet
```

Docker 直接部署：

```bash
docker logs --tail=300 vaultfleet
```

如果你用源码或自定义进程管理器运行 Master，请复制对应进程管理器里的最近 300 行日志。

## Agent 日志

systemd：

```bash
journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager
```

OpenRC：

```bash
rc-service vaultfleet-agent status
```

没有受支持 init system 时，安装脚本会用 `nohup` 启动 Agent，并写入 fallback 日志：

```bash
tail -n 300 /var/log/vaultfleet-agent.log
```

## 任务历史和 error_log

如果 Web UI 可用，请打开任务历史，复制相关失败任务的 `error_log`。

如果需要通过 API 查看任务历史，请在已登录的浏览器或已带认证 cookie 的请求中访问：

```text
GET /api/tasks
GET /api/tasks?agent_id=<agent-id>&status=failed
```

只粘贴相关失败任务，不要上传完整数据库。

## 必须脱敏的内容

提交 issue 前，请不要上传或粘贴：

- `/data/master.key`
- 完整的 `/data/vaultfleet.db`
- 完整的 `/etc/vaultfleet/agent.yaml`

请脱敏：

- enrollment token，例如 `ek_xxx`
- agent token
- 登录 cookie
- restic password
- rclone access key 和 secret key
- WebDAV、SFTP、对象存储和通知服务凭据
- 敏感的私有 endpoint、内网地址或路径

推荐写法：

```text
agent_token: <redacted>
secret_access_key: <redacted>
endpoint: https://<redacted>.example.com
```

## English

This document explains how to report VaultFleet issues and what logs to collect first.
VaultFleet does not connect to, read, or store your GitHub account. GitHub issues are
submitted with the GitHub account already signed in through your browser.

## Choose The Issue Type

- For reproducible product defects, open a [Bug report](https://github.com/momo-z/VaultFleet/issues/new?template=bug_report.yml).
- For setup, configuration, storage backend, backup policy, or restore questions, open a [Support request](https://github.com/momo-z/VaultFleet/issues/new?template=support_request.yml).
- If unsure, use the [Issue chooser](https://github.com/momo-z/VaultFleet/issues/new/choose).

## Collect Before Posting

Try to include:

- VaultFleet version, Docker image tag, or commit.
- Master deployment method, such as Docker Compose, Docker, or source build.
- Master OS, CPU architecture, and reverse proxy if any.
- Agent OS, CPU architecture, init system, and installation method.
- Reproduction steps or actions already tried.
- Failed task `error_log`, especially for backup, restore, snapshot refresh, and policy sync issues.
- Master logs and Agent logs.

## Master Logs

Docker Compose:

```bash
docker compose logs --tail=300 vaultfleet
```

Docker:

```bash
docker logs --tail=300 vaultfleet
```

If you run Master from source or a custom process manager, copy the latest 300 log lines from that process manager.

## Agent Logs

systemd:

```bash
journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager
```

OpenRC:

```bash
rc-service vaultfleet-agent status
```

When no supported init system is available, the installer starts the Agent with `nohup` and writes fallback logs:

```bash
tail -n 300 /var/log/vaultfleet-agent.log
```

## Task History And error_log

If the Web UI is available, open task history and copy the relevant failed task `error_log`.

If you need to use the API, call these endpoints from an authenticated browser/session:

```text
GET /api/tasks
GET /api/tasks?agent_id=<agent-id>&status=failed
```

Paste only relevant failed tasks. Do not upload the full database.

## Redaction Rules

Do not upload or paste:

- `/data/master.key`
- The full `/data/vaultfleet.db`
- The full `/etc/vaultfleet/agent.yaml`

Redact:

- enrollment tokens, such as `ek_xxx`
- agent tokens
- login cookies
- restic passwords
- rclone access keys and secret keys
- WebDAV, SFTP, object storage, and notification credentials
- sensitive private endpoints, internal addresses, or paths

Recommended format:

```text
agent_token: <redacted>
secret_access_key: <redacted>
endpoint: https://<redacted>.example.com
```
