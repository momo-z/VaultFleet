# Design

## Data Shape

Email notification settings are stored in `notification_configs.config` as encrypted JSON:

```json
{
  "smtp_host": "smtp.example.com",
  "smtp_port": 587,
  "smtp_security": "starttls",
  "smtp_username": "ops@example.com",
  "smtp_password": "secret",
  "from": "ops@example.com",
  "from_name": "VaultFleet",
  "to": ["admin@example.com"],
  "cc": [],
  "bcc": [],
  "subject_template": "[VaultFleet] {{.Title}}",
  "body_template": "{{.Title}}\nLevel: {{.Level}}\nAgent: {{.AgentName}}\nTime: {{.Timestamp}}\n\n{{.Body}}",
  "body_format": "text"
}
```

## Backend

- Implement `EmailNotifier` in `internal/master/notify/email.go`.
- Use standard-library SMTP primitives to avoid adding a dependency.
- Support `none`, `starttls`, and `tls` SMTP security modes.
- Render templates using `text/template`; reject invalid templates during config validation.
- Return sanitized errors that do not include SMTP credentials.

## Frontend

- Add `email` to notification types.
- Add SMTP, recipients, and template fields to the notification drawer.
- Preserve `[redacted]` password behavior when editing existing configs.

## Validation

- SMTP host, port, sender, at least one `to` recipient, subject template, and body template are required.
- Recipient lists must contain valid email addresses.
- SMTP security must be one of `none`, `starttls`, or `tls`.
- Body format must be `text` or `html`.
