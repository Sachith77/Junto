// Package email contains adapters implementing domain.EmailSender.
//
// Like internal/repository and internal/security, it is an outer layer: it implements a port
// the domain declares and nothing depends on it inwardly. The domain knows a verification
// link must reach a user; it does not know SMTP exists.
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/junto/junto/internal/domain"
)

// SMTPSender delivers mail over SMTP.
type SMTPSender struct {
	addr     string
	from     string
	auth     smtp.Auth
	useTLS   bool
	host     string
	timeout  time.Duration
	dialFunc func(network, addr string) (net.Conn, error)
}

// SMTPConfig configures an SMTPSender.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	UseTLS   bool
	Timeout  time.Duration
}

// NewSMTPSender builds an SMTPSender.
func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	s := &SMTPSender{
		addr:    net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)),
		from:    cfg.From,
		useTLS:  cfg.UseTLS,
		host:    cfg.Host,
		timeout: cfg.Timeout,
	}
	if s.timeout == 0 {
		s.timeout = 10 * time.Second
	}
	// Credentials are optional: Mailpit accepts anything locally, and configs.Validate
	// enforces TLS in production where they are not.
	if cfg.Username != "" {
		s.auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	return s
}

var _ domain.EmailSender = (*SMTPSender)(nil)

// Send delivers a message.
func (s *SMTPSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dialer := &net.Dialer{Timeout: s.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("email: dialing %s: %w", s.addr, err)
	}

	if s.useTLS {
		// Implicit TLS. Wrapping the connection before the SMTP conversation begins means
		// credentials never traverse plaintext, unlike STARTTLS which can be stripped by an
		// active attacker sitting between us and the server.
		conn = tls.Client(conn, &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12})
	}

	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("email: opening smtp session: %w", err)
	}
	defer func() { _ = client.Quit() }()

	if s.auth != nil {
		if err := client.Auth(s.auth); err != nil {
			return fmt.Errorf("email: authenticating: %w", err)
		}
	}
	// The SMTP ENVELOPE and the message HEADERS are different things, and they take different
	// forms of an address.
	//
	// MAIL FROM / RCPT TO require a bare addr-spec ("no-reply@junto.test"). The From: and To:
	// headers may carry the RFC 5322 display form ("Junto <no-reply@junto.test>"). Passing the
	// display form to MAIL FROM makes a conforming server answer 501 and refuse the message —
	// which is exactly what happened the first time this ran against Mailpit, with SMTP_FROM
	// configured in its natural, human-readable form.
	//
	// The failure was invisible to unit tests and survived a 201 from signup, because email is
	// best-effort by design. In production it would have meant no verification email ever
	// arrived, and since login requires a verified address, no account could ever be activated.
	if err := client.Mail(envelopeAddress(s.from)); err != nil {
		return fmt.Errorf("email: setting sender: %w", err)
	}
	if err := client.Rcpt(envelopeAddress(msg.To)); err != nil {
		return fmt.Errorf("email: setting recipient: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("email: opening data: %w", err)
	}
	if _, err := w.Write([]byte(buildMessage(s.from, msg))); err != nil {
		_ = w.Close()
		return fmt.Errorf("email: writing body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: closing data: %w", err)
	}
	return nil
}

// buildMessage renders RFC 5322 headers and body.
//
// Header values are sanitised of CR and LF. Without that, an attacker who controls any part
// of a header — a display name reaching a Subject, say — could inject a newline followed by
// "Bcc:" and turn the application into an open relay. This is header injection, and it is
// the reason the subject is not simply interpolated.
func buildMessage(from string, msg domain.EmailMessage) string {
	var b strings.Builder
	b.WriteString("From: " + sanitizeHeader(from) + "\r\n")
	b.WriteString("To: " + sanitizeHeader(msg.To) + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader(msg.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")

	if msg.HTMLBody != "" {
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.HTMLBody)
	} else {
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.TextBody)
	}
	return b.String()
}

func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

// envelopeAddress reduces an address to the bare addr-spec required by MAIL FROM and RCPT TO.
//
//	"Junto <no-reply@junto.test>"  ->  "no-reply@junto.test"
//	"no-reply@junto.test"          ->  "no-reply@junto.test"
//
// An unparseable value is returned sanitised rather than rejected: the SMTP server is the
// authority on whether an address is deliverable, and failing here would turn a configuration
// typo into a startup-time mystery instead of a clear 5xx from the server that saw it.
func envelopeAddress(v string) string {
	if parsed, err := mail.ParseAddress(v); err == nil {
		return parsed.Address
	}
	return sanitizeHeader(strings.TrimSpace(v))
}

// LogSender writes messages to the log instead of sending them.
//
// For development without a mail container, and for tests that need the flow to complete
// without asserting on delivery. It logs the SUBJECT and RECIPIENT but never the body,
// because bodies contain live verification and password-reset links — writing those to a log
// would make log access equivalent to account takeover.
type LogSender struct{ log *slog.Logger }

// NewLogSender builds a LogSender.
func NewLogSender(log *slog.Logger) *LogSender {
	if log == nil {
		log = slog.Default()
	}
	return &LogSender{log: log}
}

var _ domain.EmailSender = (*LogSender)(nil)

// Send logs the message metadata.
func (l *LogSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	l.log.InfoContext(ctx, "email not sent (LogSender active)",
		"to", msg.To, "subject", msg.Subject)
	return nil
}
