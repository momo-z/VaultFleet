package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailNotifierRenderMessageAppliesTemplatesAndHeaders(t *testing.T) {
	notifier := NewEmailNotifier(validEmailConfig())
	msg := NotifyMessage{
		Title:     "Backup Failed",
		Body:      "repository locked",
		Level:     LevelError,
		AgentName: "Tokyo-1",
		Timestamp: time.Date(2026, 5, 18, 3, 0, 15, 0, time.UTC),
	}

	data, err := notifier.renderMessage(msg)

	require.NoError(t, err)
	raw := string(data)
	assert.Contains(t, raw, `From: "VaultFleet" <ops@example.com>`)
	assert.Contains(t, raw, "To: <admin@example.com>")
	assert.Contains(t, raw, "Subject: [VaultFleet] Backup Failed - Tokyo-1")
	assert.Contains(t, raw, `Content-Type: text/plain; charset="UTF-8"`)
	assert.Contains(t, raw, "Level: error")
	assert.Contains(t, raw, "repository locked")
}

func TestEmailNotifierSendUsesSMTPRecipientsAndMessage(t *testing.T) {
	server := newFakeSMTPServer(t)
	config := validEmailConfig()
	config.SMTPHost = server.host
	config.SMTPPort = server.port
	config.SMTPSecurity = "none"
	config.Cc = []string{"cc@example.com"}
	config.Bcc = []string{"bcc@example.com"}
	config.SMTPUsername = ""
	config.SMTPPassword = ""
	notifier := NewEmailNotifier(config)

	err := notifier.Send(context.Background(), NotifyMessage{
		Title:     "Agent Offline",
		Body:      "Agent Tokyo-1 is offline.",
		Level:     LevelWarning,
		AgentName: "Tokyo-1",
		Timestamp: time.Date(2026, 5, 18, 3, 0, 15, 0, time.UTC),
	})

	require.NoError(t, err)
	transcript := server.transcript()
	assert.Contains(t, transcript, "MAIL FROM:<ops@example.com>")
	assert.Contains(t, transcript, "RCPT TO:<admin@example.com>")
	assert.Contains(t, transcript, "RCPT TO:<cc@example.com>")
	assert.Contains(t, transcript, "RCPT TO:<bcc@example.com>")
	assert.Contains(t, transcript, "Agent Offline")
	assert.Contains(t, transcript, "Agent Tokyo-1 is offline.")
}

func TestValidateEmailConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EmailConfig)
		want   string
	}{
		{name: "missing host", mutate: func(c *EmailConfig) { c.SMTPHost = "" }, want: "smtp_host"},
		{name: "bad port", mutate: func(c *EmailConfig) { c.SMTPPort = 70000 }, want: "smtp_port"},
		{name: "bad security", mutate: func(c *EmailConfig) { c.SMTPSecurity = "ssl3" }, want: "smtp_security"},
		{name: "password without username", mutate: func(c *EmailConfig) { c.SMTPUsername = ""; c.SMTPPassword = "secret" }, want: "smtp_username"},
		{name: "bad from", mutate: func(c *EmailConfig) { c.From = "not an address" }, want: "from"},
		{name: "missing to", mutate: func(c *EmailConfig) { c.To = nil }, want: "to"},
		{name: "bad recipient", mutate: func(c *EmailConfig) { c.To = []string{"bad"} }, want: "recipient"},
		{name: "bad subject template", mutate: func(c *EmailConfig) { c.SubjectTemplate = "{{" }, want: "subject_template"},
		{name: "bad body format", mutate: func(c *EmailConfig) { c.BodyFormat = "markdown" }, want: "body_format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validEmailConfig()
			tt.mutate(&config)

			err := ValidateEmailConfig(config)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestEmailNotifierErrorsDoNotLeakPassword(t *testing.T) {
	config := validEmailConfig()
	config.SMTPHost = "127.0.0.1"
	config.SMTPPort = 1
	config.SMTPUsername = "ops@example.com"
	config.SMTPPassword = "secret-smtp-password"
	notifier := NewEmailNotifier(config)

	err := notifier.Send(context.Background(), NotifyMessage{Title: "Test"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect smtp server")
	assert.NotContains(t, err.Error(), "secret-smtp-password")
}

func validEmailConfig() EmailConfig {
	return EmailConfig{
		SMTPHost:        "smtp.example.com",
		SMTPPort:        587,
		SMTPSecurity:    "starttls",
		SMTPUsername:    "ops@example.com",
		SMTPPassword:    "secret",
		From:            "ops@example.com",
		FromName:        "VaultFleet",
		To:              []string{"admin@example.com"},
		SubjectTemplate: "[VaultFleet] {{.Title}} - {{.AgentName}}",
		BodyTemplate:    "{{.Title}}\nLevel: {{.Level}}\nAgent: {{.AgentName}}\nTime: {{.Timestamp}}\n\n{{.Body}}",
		BodyFormat:      "text",
	}
}

type fakeSMTPServer struct {
	listener net.Listener
	host     string
	port     int
	done     chan struct{}
	lines    []string
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host, portValue, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := net.LookupPort("tcp", portValue)
	require.NoError(t, err)

	server := &fakeSMTPServer{
		listener: listener,
		host:     host,
		port:     port,
		done:     make(chan struct{}),
	}
	go server.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		<-server.done
	})
	return server
}

func (s *fakeSMTPServer) serve() {
	defer close(s.done)

	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeSMTPLine(writer, "220 fake smtp")
	inData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				s.lines = append(s.lines, err.Error())
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.lines = append(s.lines, line)
		if inData {
			if line == "." {
				inData = false
				writeSMTPLine(writer, "250 ok")
			}
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			writeSMTPLine(writer, "250-localhost")
			writeSMTPLine(writer, "250 OK")
		case strings.HasPrefix(upper, "MAIL FROM"):
			writeSMTPLine(writer, "250 ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			writeSMTPLine(writer, "250 ok")
		case strings.HasPrefix(upper, "DATA"):
			inData = true
			writeSMTPLine(writer, "354 go ahead")
		case strings.HasPrefix(upper, "QUIT"):
			writeSMTPLine(writer, "221 bye")
			return
		default:
			writeSMTPLine(writer, "250 ok")
		}
	}
}

func writeSMTPLine(writer *bufio.Writer, line string) {
	_, _ = writer.WriteString(line + "\r\n")
	_ = writer.Flush()
}

func (s *fakeSMTPServer) transcript() string {
	<-s.done
	return strings.Join(s.lines, "\n")
}

func TestDefaultEmailDialerCanCreateTLSClientArgumentShape(t *testing.T) {
	var _ emailDialer = func(ctx context.Context, network, address string, timeout time.Duration, tlsConfig *tls.Config) (*smtp.Client, error) {
		return defaultEmailDialer(ctx, network, address, timeout, tlsConfig)
	}
}
