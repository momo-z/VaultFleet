# VaultFleet 版本发布指南 / Release Guide

**语言 / Language:** 中文 | [English](#english)

这份文档记录 VaultFleet 的标准发布流程，避免后续忘记 Docker 镜像、
GitHub Release 和 Agent 安装脚本之间的关系。

## 发布产物

一次版本发布会产生两类产物：

- Master Docker 镜像，发布到 GitHub Container Registry：
  - `ghcr.io/momo-z/vaultfleet:latest`
  - `ghcr.io/momo-z/vaultfleet:vX.Y.Z`
  - `ghcr.io/momo-z/vaultfleet:sha-xxxxxxx`
- Agent 二进制，上传到 GitHub Releases：
  - `vaultfleet-agent-linux-amd64`
  - `vaultfleet-agent-linux-arm64`
  - `checksums.txt`

Agent 安装脚本默认从 GitHub Releases 下载：

```text
https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-amd64
https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-arm64
```

## 首次发布前准备

1. 先把 release workflow 推送到 GitHub。
2. 第一次 workflow 发布 GHCR package 后，到 GitHub Packages 设置里把
   `ghcr.io/momo-z/vaultfleet` 改成 Public。
3. 确认默认分支是 `main` 或 `master`。workflow 只会在默认分支推送时发布
   `latest` 镜像。

## 发布 v0.1.0

版本 tag 使用小写 `v`，因为 workflow 匹配的是 `v*`。

```bash
# 1. 查看本地改动。
git status

# 2. 运行和 CI 一致的测试命令。
go test ./... -v -race -count=1

# 3. 提交准备发布的改动。
git add .
git commit -m "Prepare v0.1.0 release"

# 4. 推送默认分支。这一步会更新 latest Docker 镜像。
git push origin main
# 如果默认分支是 master：
# git push origin master

# 5. 创建并推送版本 tag。
git tag -a v0.1.0 -m "VaultFleet v0.1.0"
git push origin v0.1.0
```

推送 tag 后，GitHub Actions 会创建 GitHub Release 资产，并发布版本号 Docker 镜像。

## 发布后验证

等待 GitHub Actions 执行完成后：

```bash
# 检查 Master 镜像。
docker pull ghcr.io/momo-z/vaultfleet:v0.1.0
docker pull ghcr.io/momo-z/vaultfleet:latest

# 检查 Agent Release 资产。
curl -I https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-amd64
curl -I https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-arm64
curl -I https://github.com/momo-z/VaultFleet/releases/latest/download/checksums.txt
```

简单烟测：

```bash
docker run --rm -p 8080:8080 ghcr.io/momo-z/vaultfleet:v0.1.0
```

打开 `http://localhost:8080`，确认初始化页面可以加载。

## Agent 下载参数

普通安装：

```bash
curl -fsSL http://MASTER_HOST:8080/install.sh | bash -s -- \
  --server http://MASTER_HOST:8080 \
  --token ek_xxxxxxxxxxxxxxxxxxxxxxxx
```

通过 GitHub 代理安装：

```bash
curl -fsSL http://MASTER_HOST:8080/install.sh | bash -s -- \
  --server http://MASTER_HOST:8080 \
  --token ek_xxxxxxxxxxxxxxxxxxxxxxxx \
  --github-proxy https://gh-proxy.example.com
```

只有需要覆盖完整 Agent 二进制下载地址时才使用 `--agent-url`。它主要用于测试
未发布版本、私有镜像、内网 CDN 或临时下载源。

```bash
curl -fsSL http://MASTER_HOST:8080/install.sh | bash -s -- \
  --server http://MASTER_HOST:8080 \
  --token ek_xxxxxxxxxxxxxxxxxxxxxxxx \
  --agent-url https://example.com/vaultfleet-agent-linux-amd64
```

## 处理错误发布

如果 tag 推错了，并且还没有用户使用这个版本：

```bash
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0
```

然后修复问题、重新提交、重新创建 tag 并推送。

如果用户可能已经拉取或安装了这个版本，不要重写 tag，直接发布新的补丁版本，
例如 `v0.1.1`。

## English

This document records the standard VaultFleet release flow, including the
relationship between Docker images, GitHub Releases, and the Agent installer.

## Release Outputs

A version release publishes two kinds of artifacts:

- Master Docker images on GitHub Container Registry:
  - `ghcr.io/momo-z/vaultfleet:latest`
  - `ghcr.io/momo-z/vaultfleet:vX.Y.Z`
  - `ghcr.io/momo-z/vaultfleet:sha-xxxxxxx`
- Agent binaries on GitHub Releases:
  - `vaultfleet-agent-linux-amd64`
  - `vaultfleet-agent-linux-arm64`
  - `checksums.txt`

The Agent installer downloads from GitHub Releases by default:

```text
https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-amd64
https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-arm64
```

## Before The First Release

1. Push the release workflow to GitHub.
2. After the first workflow publishes the GHCR package, open the GitHub
   Packages settings and make `ghcr.io/momo-z/vaultfleet` public.
3. Confirm the default branch is `main` or `master`. The workflow publishes
   the `latest` image only when the default branch is pushed.

## Release v0.1.0

Use lowercase `v` tags because the workflow matches `v*`.

```bash
# 1. Inspect local changes.
git status

# 2. Run the same test command used by CI.
go test ./... -v -race -count=1

# 3. Commit release-ready changes.
git add .
git commit -m "Prepare v0.1.0 release"

# 4. Push the default branch. This updates the latest Docker image.
git push origin main
# If the default branch is master:
# git push origin master

# 5. Create and push the release tag.
git tag -a v0.1.0 -m "VaultFleet v0.1.0"
git push origin v0.1.0
```

Pushing the tag creates the GitHub Release assets and the versioned Docker
image.

## Verify The Release

After GitHub Actions completes:

```bash
# Check Master images.
docker pull ghcr.io/momo-z/vaultfleet:v0.1.0
docker pull ghcr.io/momo-z/vaultfleet:latest

# Check Agent release assets.
curl -I https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-amd64
curl -I https://github.com/momo-z/VaultFleet/releases/latest/download/vaultfleet-agent-linux-arm64
curl -I https://github.com/momo-z/VaultFleet/releases/latest/download/checksums.txt
```

Smoke test:

```bash
docker run --rm -p 8080:8080 ghcr.io/momo-z/vaultfleet:v0.1.0
```

Open `http://localhost:8080` and confirm the initialization page loads.

## Agent Download Options

Normal install:

```bash
curl -fsSL http://MASTER_HOST:8080/install.sh | bash -s -- \
  --server http://MASTER_HOST:8080 \
  --token ek_xxxxxxxxxxxxxxxxxxxxxxxx
```

Install through a GitHub proxy:

```bash
curl -fsSL http://MASTER_HOST:8080/install.sh | bash -s -- \
  --server http://MASTER_HOST:8080 \
  --token ek_xxxxxxxxxxxxxxxxxxxxxxxx \
  --github-proxy https://gh-proxy.example.com
```

Use `--agent-url` only when you want to override the full Agent binary URL.
This is useful for testing an unreleased build, using a private mirror, using
an internal CDN, or serving a temporary binary.

```bash
curl -fsSL http://MASTER_HOST:8080/install.sh | bash -s -- \
  --server http://MASTER_HOST:8080 \
  --token ek_xxxxxxxxxxxxxxxxxxxxxxxx \
  --agent-url https://example.com/vaultfleet-agent-linux-amd64
```

## Fix A Bad Release

If the tag was pushed by mistake and users have not used the release yet:

```bash
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0
```

Then fix the issue, commit again, recreate the tag, and push it.

If users may already have pulled or installed the release, do not rewrite the
tag. Publish a new patch version instead, for example `v0.1.1`.
