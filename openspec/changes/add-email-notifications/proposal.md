# Add Email Notifications

## Why

VaultFleet currently supports Telegram and webhook notification channels. Operators often rely on email as a baseline alerting mechanism, especially in environments where chat bots or webhook receivers are not available.

## What Changes

- Add `email` as a supported notification type.
- Store SMTP host, port, security mode, username, password, sender, recipients, and templates in the existing encrypted notification config.
- Send notification events through SMTP using the existing dispatcher.
- Extend the notification settings UI to configure email channels.
- Add tests for validation, redaction, template rendering, and send behavior.

## Impact

- Backend notification factory accepts `email`.
- Notification API redaction and redacted-secret preservation logic includes SMTP passwords.
- Frontend notification type union and form support `email`.
- No database schema change is required.
