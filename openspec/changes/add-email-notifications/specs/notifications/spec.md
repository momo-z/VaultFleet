# Notifications Spec Delta

## ADDED Requirements

### Requirement: Email Notification Channel

VaultFleet SHALL support `email` as a notification type.

#### Scenario: Send email notification

- **GIVEN** an enabled email notification config subscribed to an event
- **WHEN** the event is dispatched
- **THEN** VaultFleet sends an SMTP email rendered from the configured templates

### Requirement: SMTP Configuration

VaultFleet SHALL allow operators to configure SMTP host, port, security mode, credentials, sender, and recipients for email notification configs.

#### Scenario: Store SMTP password securely

- **GIVEN** an email notification config contains an SMTP password
- **WHEN** the config is saved
- **THEN** the password is encrypted at rest and redacted from API responses

### Requirement: Email Templates

VaultFleet SHALL allow configuring subject and body templates for email notifications.

#### Scenario: Render notification template

- **GIVEN** a template references notification fields such as title, level, agent name, timestamp, and body
- **WHEN** an email notification is sent
- **THEN** those fields are rendered into the outgoing email
