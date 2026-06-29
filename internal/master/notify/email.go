package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"sort"
	"strings"
	"text/template"
	"time"
)

const (
	emailSecurityNone     = "none"
	emailSecuritySTARTTLS = "starttls"
	emailSecurityTLS      = "tls"

	emailBodyFormatText = "text"
	emailBodyFormatHTML = "html"
)

type EmailConfig struct {
	SMTPHost        string   `json:"smtp_host"`
	SMTPPort        int      `json:"smtp_port"`
	SMTPSecurity    string   `json:"smtp_security"`
	SMTPUsername    string   `json:"smtp_username,omitempty"`
	SMTPPassword    string   `json:"smtp_password,omitempty"`
	From            string   `json:"from"`
	FromName        string   `json:"from_name,omitempty"`
	To              []string `json:"to"`
	Cc              []string `json:"cc,omitempty"`
	Bcc             []string `json:"bcc,omitempty"`
	SubjectTemplate string   `json:"subject_template"`
	BodyTemplate    string   `json:"body_template"`
	BodyFormat      string   `json:"body_format,omitempty"`
}

type EmailNotifier struct {
	config EmailConfig
	dialer emailDialer
}

type emailDialer func(ctx context.Context, network, address string, timeout time.Duration, tlsConfig *tls.Config) (*smtp.Client, error)

func NewEmailNotifier(config EmailConfig) *EmailNotifier {
	normalizeEmailConfig(&config)
	return &EmailNotifier{
		config: config,
		dialer: defaultEmailDialer,
	}
}

func (n *EmailNotifier) Send(ctx context.Context, msg NotifyMessage) error {
	if ctx == nil {
		ctx = context.Background()
	}

	data, err := n.renderMessage(msg)
	if err != nil {
		return fmt.Errorf("render email message: %w", err)
	}

	host := strings.TrimSpace(n.config.SMTPHost)
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", n.config.SMTPPort))
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var dialTLSConfig *tls.Config
	if n.config.SMTPSecurity == emailSecurityTLS {
		dialTLSConfig = tlsConfig
	}
	client, err := n.dialer(ctx, "tcp", addr, defaultHTTPTimeout, dialTLSConfig)
	if err != nil {
		return sanitizedSendError{op: "connect smtp server", err: err}
	}
	defer client.Close()

	if n.config.SMTPSecurity == emailSecuritySTARTTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return errors.New("smtp server does not support STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return sanitizedSendError{op: "start smtp tls", err: err}
		}
	}

	if strings.TrimSpace(n.config.SMTPUsername) != "" {
		auth := smtp.PlainAuth("", n.config.SMTPUsername, n.config.SMTPPassword, host)
		if err := client.Auth(auth); err != nil {
			return sanitizedSendError{op: "authenticate smtp user", err: err}
		}
	}

	fromAddress, err := parseEmailAddress(n.config.From)
	if err != nil {
		return fmt.Errorf("invalid email from address")
	}
	if err := client.Mail(fromAddress.Address); err != nil {
		return sanitizedSendError{op: "set email sender", err: err}
	}

	for _, recipient := range n.recipients() {
		address, err := parseEmailAddress(recipient)
		if err != nil {
			return fmt.Errorf("invalid email recipient")
		}
		if err := client.Rcpt(address.Address); err != nil {
			return sanitizedSendError{op: "set email recipient", err: err}
		}
	}

	writer, err := client.Data()
	if err != nil {
		return sanitizedSendError{op: "open smtp data writer", err: err}
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return sanitizedSendError{op: "write email message", err: err}
	}
	if err := writer.Close(); err != nil {
		return sanitizedSendError{op: "close email message", err: err}
	}
	if err := client.Quit(); err != nil {
		return sanitizedSendError{op: "quit smtp session", err: err}
	}

	return nil
}

func (n *EmailNotifier) Type() string {
	return "email"
}

func (n *EmailNotifier) renderMessage(msg NotifyMessage) ([]byte, error) {
	timestamp := msg.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	data := emailTemplateData{
		Title:     msg.Title,
		Body:      msg.Body,
		Level:     string(msg.Level),
		AgentName: msg.AgentName,
		Timestamp: timestamp.UTC().Format(time.RFC3339),
	}

	subject, err := renderEmailTemplate("subject", n.config.SubjectTemplate, data)
	if err != nil {
		return nil, err
	}
	body, err := renderEmailTemplate("body", n.config.BodyTemplate, data)
	if err != nil {
		return nil, err
	}

	headers := map[string]string{
		"From":                      n.formattedFrom(),
		"To":                        formattedEmailList(n.config.To),
		"Subject":                   mime.QEncoding.Encode("utf-8", strings.TrimSpace(subject)),
		"Date":                      timestamp.UTC().Format(time.RFC1123Z),
		"MIME-Version":              "1.0",
		"Content-Type":              emailContentType(n.config.BodyFormat),
		"Content-Transfer-Encoding": "8bit",
	}
	if len(n.config.Cc) > 0 {
		headers["Cc"] = formattedEmailList(n.config.Cc)
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteString(": ")
		out.WriteString(sanitizeEmailHeaderValue(headers[key]))
		out.WriteString("\r\n")
	}
	out.WriteString("\r\n")
	out.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		out.WriteString("\r\n")
	}
	return []byte(out.String()), nil
}

func (n *EmailNotifier) formattedFrom() string {
	address, err := parseEmailAddress(n.config.From)
	if err != nil {
		return n.config.From
	}
	if strings.TrimSpace(n.config.FromName) == "" {
		return address.String()
	}
	address.Name = strings.TrimSpace(n.config.FromName)
	return address.String()
}

func (n *EmailNotifier) recipients() []string {
	recipients := make([]string, 0, len(n.config.To)+len(n.config.Cc)+len(n.config.Bcc))
	recipients = append(recipients, n.config.To...)
	recipients = append(recipients, n.config.Cc...)
	recipients = append(recipients, n.config.Bcc...)
	return recipients
}

func ValidateEmailConfig(config EmailConfig) error {
	normalizeEmailConfig(&config)
	if strings.TrimSpace(config.SMTPHost) == "" {
		return errors.New("email smtp_host is required")
	}
	if strings.ContainsAny(config.SMTPHost, "\r\n") {
		return errors.New("email smtp_host is invalid")
	}
	if config.SMTPPort <= 0 || config.SMTPPort > 65535 {
		return errors.New("email smtp_port must be between 1 and 65535")
	}
	switch config.SMTPSecurity {
	case emailSecurityNone, emailSecuritySTARTTLS, emailSecurityTLS:
	default:
		return errors.New("email smtp_security must be none, starttls, or tls")
	}
	if strings.TrimSpace(config.SMTPUsername) == "" && strings.TrimSpace(config.SMTPPassword) != "" {
		return errors.New("email smtp_username is required when smtp_password is set")
	}
	if _, err := parseEmailAddress(config.From); err != nil {
		return errors.New("email from is invalid")
	}
	if len(config.To) == 0 {
		return errors.New("email to is required")
	}
	for _, recipient := range append(append([]string{}, config.To...), append(config.Cc, config.Bcc...)...) {
		if _, err := parseEmailAddress(recipient); err != nil {
			return errors.New("email recipient is invalid")
		}
	}
	if strings.TrimSpace(config.SubjectTemplate) == "" {
		return errors.New("email subject_template is required")
	}
	if strings.TrimSpace(config.BodyTemplate) == "" {
		return errors.New("email body_template is required")
	}
	if _, err := template.New("subject").Parse(config.SubjectTemplate); err != nil {
		return fmt.Errorf("email subject_template is invalid: %w", err)
	}
	if _, err := template.New("body").Parse(config.BodyTemplate); err != nil {
		return fmt.Errorf("email body_template is invalid: %w", err)
	}
	switch config.BodyFormat {
	case emailBodyFormatText, emailBodyFormatHTML:
	default:
		return errors.New("email body_format must be text or html")
	}
	return nil
}

func normalizeEmailConfig(config *EmailConfig) {
	config.SMTPHost = strings.TrimSpace(config.SMTPHost)
	config.SMTPSecurity = strings.ToLower(strings.TrimSpace(config.SMTPSecurity))
	if config.SMTPSecurity == "" {
		config.SMTPSecurity = emailSecuritySTARTTLS
	}
	config.SMTPUsername = strings.TrimSpace(config.SMTPUsername)
	config.From = strings.TrimSpace(config.From)
	config.FromName = strings.TrimSpace(config.FromName)
	config.BodyFormat = strings.ToLower(strings.TrimSpace(config.BodyFormat))
	if config.BodyFormat == "" {
		config.BodyFormat = emailBodyFormatText
	}
	config.To = normalizeEmailList(config.To)
	config.Cc = normalizeEmailList(config.Cc)
	config.Bcc = normalizeEmailList(config.Bcc)
}

func normalizeEmailList(values []string) []string {
	next := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			next = append(next, trimmed)
		}
	}
	return next
}

func renderEmailTemplate(name, source string, data emailTemplateData) (string, error) {
	tpl, err := template.New(name).Parse(source)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

type emailTemplateData struct {
	Title     string
	Body      string
	Level     string
	AgentName string
	Timestamp string
}

func parseEmailAddress(value string) (*mail.Address, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(address.Address, "\r\n") {
		return nil, errors.New("invalid address")
	}
	return address, nil
}

func formattedEmailList(values []string) string {
	formatted := make([]string, 0, len(values))
	for _, value := range values {
		address, err := parseEmailAddress(value)
		if err != nil {
			continue
		}
		formatted = append(formatted, address.String())
	}
	return strings.Join(formatted, ", ")
}

func sanitizeEmailHeaderValue(value string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(value)
}

func emailContentType(format string) string {
	if format == emailBodyFormatHTML {
		return `text/html; charset="UTF-8"`
	}
	return `text/plain; charset="UTF-8"`
}

func defaultEmailDialer(ctx context.Context, network, address string, timeout time.Duration, tlsConfig *tls.Config) (*smtp.Client, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil && strings.EqualFold(tlsConfig.ServerName, "") {
		tlsConfig = tlsConfig.Clone()
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr == nil {
			tlsConfig.ServerName = host
		}
	}
	if tlsConfig != nil {
		if _, _, splitErr := net.SplitHostPort(address); splitErr == nil {
			conn = tls.Client(conn, tlsConfig)
		}
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return smtp.NewClient(conn, host)
}
