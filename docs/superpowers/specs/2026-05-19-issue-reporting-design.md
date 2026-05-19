# VaultFleet Issue Reporting Design

> Date: 2026-05-19
> Status: Approved for implementation planning

## Goal

Make it straightforward for users to report VaultFleet problems through GitHub
without giving VaultFleet access to their GitHub account. The first version
uses GitHub's own logged-in browser session, issue templates, and clear support
documentation. It does not implement GitHub OAuth, GitHub Apps, automatic issue
creation, or diagnostic ZIP upload.

## Context

VaultFleet already records useful troubleshooting information in several places:

- Master process logs, usually available through Docker or the hosting process.
- Agent process logs, usually available through systemd, OpenRC, or the fallback
  `/var/log/vaultfleet-agent.log`.
- Task history in the Master database, exposed by `GET /api/tasks`, including
  `error_log` for failed backup and restore work.
- Agent status and system information exposed by the existing Agent API and UI.

The missing piece is a public, consistent reporting path that tells users what
to include, how to collect it, and what must be redacted.

## User Flow

1. A user encounters a bug or needs support.
2. The user opens the README "Report an issue" link or the support document.
3. The user chooses either "Bug report" or "Support request" in GitHub's issue
   chooser.
4. GitHub uses the user's existing browser login. VaultFleet never sees or stores
   a GitHub token.
5. The selected issue form asks for environment details, reproduction steps,
   logs, task `error_log`, and a redaction confirmation.
6. The maintainer receives a structured issue with enough context to start
   triage without first asking for the basic deployment and log information.

## Approach

Use GitHub Issue Forms instead of plain Markdown issue templates.

Issue Forms provide required fields, dropdowns, text areas, default hints, and
labels while remaining simple repository metadata. They fit this feature because
the goal is structured troubleshooting intake, not custom automation.

Add these files:

- `.github/ISSUE_TEMPLATE/bug_report.yml`
- `.github/ISSUE_TEMPLATE/support_request.yml`
- `.github/ISSUE_TEMPLATE/config.yml`
- `docs/support.md`

Update:

- `README.md`

## Issue Templates

### Bug Report

The bug report form is bilingual, with Chinese and English in labels and
descriptions. It collects:

- A short summary.
- VaultFleet version, image tag, or commit.
- Deployment type: Docker Compose, Docker, source build, or other.
- Master environment: OS, architecture, reverse proxy if any, browser if UI
  related.
- Agent environment: OS, architecture, init system, installation method.
- Reproduction steps.
- Expected behavior.
- Actual behavior.
- Relevant Master logs.
- Relevant Agent logs.
- Task history `error_log` if the issue involves backup, restore, snapshots, or
  policy sync.
- Screenshots or extra context.
- Confirmation that secrets were redacted.

The form should apply labels such as `bug` and `needs-triage`.

### Support Request

The support request form is also bilingual. It handles setup problems,
configuration questions, storage backend trouble, restore questions, and other
usage problems that are not clearly product defects.

It collects:

- What the user is trying to do.
- Current deployment state.
- Storage backend type, without secrets.
- Master and Agent environment summaries.
- Commands or UI actions already tried.
- Logs and task `error_log` where relevant.
- The same redaction confirmation.

The form should apply labels such as `support` and `needs-triage`.

### Issue Chooser

The issue chooser should:

- Show the two issue forms.
- Disable or discourage blank issues.
- Link to `docs/support.md` as the troubleshooting and log collection guide.

## Support Document

`docs/support.md` should be bilingual and concise. It should include:

- When to open a bug report versus a support request.
- A reminder that the GitHub account is determined by the user's GitHub browser
  session.
- Direct links:
  - `https://github.com/momo-z/VaultFleet/issues/new/choose`
  - A direct bug report template URL.
  - A direct support request template URL.
- Log collection commands:
  - `docker compose logs --tail=300 vaultfleet`
  - `docker logs --tail=300 vaultfleet`
  - `journalctl -u vaultfleet-agent --since "24 hours ago" --no-pager`
  - `rc-service vaultfleet-agent status`
  - `tail -n 300 /var/log/vaultfleet-agent.log`
- How to include task history:
  - Use the Web UI task history if available.
  - Or call `GET /api/tasks` from an authenticated browser/session and copy the
    relevant failed task `error_log`.
- Redaction rules:
  - Do not upload `master.key`.
  - Do not upload the full `vaultfleet.db`.
  - Do not paste full `/etc/vaultfleet/agent.yaml`.
  - Redact enrollment tokens, agent tokens, cookies, restic passwords, rclone
    access keys, secret keys, WebDAV credentials, SFTP credentials, and private
    endpoints if sensitive.

## README Update

Add a short "反馈问题 / Report an issue" section near the development status or
reference sections. It should point to:

- `docs/support.md`
- `https://github.com/momo-z/VaultFleet/issues/new/choose`

The README should not duplicate the full troubleshooting guide.

## Non-Goals

This design intentionally excludes:

- GitHub OAuth or GitHub App authorization.
- Creating issues from inside VaultFleet.
- Uploading diagnostic ZIP files to GitHub.
- Collecting Agent logs through the Master-Agent protocol.
- Adding a Web UI diagnostics page.
- Changing runtime logging behavior.

These can be added later if support volume justifies the extra security and
maintenance cost.

## Error Handling

Because the feature is static repository metadata and documentation, runtime
error handling is limited:

- Invalid issue form YAML would be caught by review and GitHub's issue template
  rendering.
- Broken support links should be avoided by using repository-relative links in
  README where possible and canonical GitHub URLs in support docs where needed.
- Commands in docs should include alternatives because users may run Master in
  Docker Compose, Docker, or a custom process, and Agents may run under systemd,
  OpenRC, or fallback `nohup`.

## Testing And Verification

Verification should include:

- Review the YAML syntax for both issue forms.
- Confirm `.github/ISSUE_TEMPLATE/config.yml` points to the support guide.
- Confirm README links are correct.
- Confirm all log commands match current deployment behavior in
  `docker-compose.yml` and `build/install.sh`.
- Confirm the templates do not ask users to paste known secret files or
  credentials.

No Go tests are required because this change does not affect runtime code.
