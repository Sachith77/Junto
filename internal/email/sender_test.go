package email

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/junto/junto/internal/domain"
)

// TestHeaderInjectionIsStripped is the security-relevant test in this package.
//
// If any attacker-influenced value reaches a header unsanitised — a display name flowing into
// a Subject, say — a CR/LF pair lets them terminate that header and start a new one. Adding
// "Bcc:" turns the application into an open relay for spam sent from our domain and our
// reputation. It is one of the oldest bugs in mail handling and still routinely shipped.
func TestHeaderInjectionIsStripped(t *testing.T) {
	msg := domain.EmailMessage{
		To:       "victim@example.com\r\nBcc: everyone@spam.test",
		Subject:  "Hello\r\nBcc: also-everyone@spam.test\nX-Injected: yes",
		TextBody: "body",
	}

	built := buildMessage("Junto <no-reply@junto.test>", msg)

	headerBlock, body, found := strings.Cut(built, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no header/body separator:\n%s", built)
	}
	if body != "body" {
		t.Errorf("body = %q, want %q", body, "body")
	}

	// The property that matters is that no NEW HEADER LINE was created.
	//
	// The text "Bcc:" survives inside the To and Subject VALUES, and that is fine — a header
	// value containing the characters B, c, c and a colon is inert. The attack only works if
	// the CR/LF survives and starts a new line, so the assertion is about line structure, not
	// about the substring appearing anywhere.
	lines := strings.Split(headerBlock, "\r\n")
	wantPrefixes := []string{"From:", "To:", "Subject:", "MIME-Version:", "Content-Type:"}
	if len(lines) != len(wantPrefixes) {
		t.Fatalf("expected exactly %d header lines, got %d — a header was injected:\n%s",
			len(wantPrefixes), len(lines), headerBlock)
	}
	for i, prefix := range wantPrefixes {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("header line %d = %q, want prefix %q", i, lines[i], prefix)
		}
	}

	// And no line may BEGIN with an injected header name.
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, forbidden := range []string{"bcc:", "cc:", "x-injected:"} {
			if strings.HasPrefix(lower, forbidden) {
				t.Errorf("injected header line %q survived sanitisation", line)
			}
		}
	}

	// The raw message must contain no stray CR or LF beyond the structural ones we wrote:
	// 5 header terminators plus the blank line separating headers from the body.
	if got := strings.Count(built, "\r\n"); got != len(wantPrefixes)+1 {
		t.Errorf("message contains %d CRLF sequences, want %d:\n%q", got, len(wantPrefixes)+1, built)
	}
	// No BARE CR or LF: strip the legitimate CRLF terminators, and nothing should remain.
	// A bare "\n" is enough to inject a header in many SMTP implementations even though RFC
	// 5322 requires CRLF.
	stripped := strings.ReplaceAll(headerBlock, "\r\n", "")
	if strings.ContainsAny(stripped, "\r\n") {
		t.Errorf("a bare CR or LF survived in the header block:\n%q", headerBlock)
	}
}

func TestSanitizeHeaderRemovesLineBreaks(t *testing.T) {
	cases := map[string]string{
		"plain":              "plain",
		"with\rcarriage":     "withcarriage",
		"with\nnewline":      "withnewline",
		"with\r\nboth":       "withboth",
		"\r\nleading":        "leading",
		"trailing\r\n":       "trailing",
		"multi\r\nline\r\nx": "multilinex",
		"unicode é ok":       "unicode é ok",
	}
	for in, want := range cases {
		if got := sanitizeHeader(in); got != want {
			t.Errorf("sanitizeHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEnvelopeAddressStripsDisplayName is a regression test for a bug that only surfaced
// when the server was actually run.
//
// SMTP's MAIL FROM / RCPT TO require a bare addr-spec; the From:/To: HEADERS may carry the
// display form. Passing "Junto <no-reply@junto.local>" to MAIL FROM makes a conforming server
// answer 501 and refuse the message. Nothing in the unit tests could see it — signup still
// returned 201, because email delivery is best-effort by design — but in production no
// verification email would ever have arrived, and login requires a verified address.
func TestEnvelopeAddressStripsDisplayName(t *testing.T) {
	cases := map[string]string{
		"Junto <no-reply@junto.local>": "no-reply@junto.local",
		"<no-reply@junto.local>":       "no-reply@junto.local",
		"no-reply@junto.local":         "no-reply@junto.local",
		`"Junto Team" <team@junto.io>`: "team@junto.io",
		"  spaced@junto.io  ":          "spaced@junto.io",
	}
	for in, want := range cases {
		if got := envelopeAddress(in); got != want {
			t.Errorf("envelopeAddress(%q) = %q, want %q", in, got, want)
		}
	}

	// An unparseable value is passed through sanitised. The SMTP server is the authority on
	// deliverability; failing here would turn a config typo into a startup-time mystery.
	if got := envelopeAddress("not an address\r\nBcc: x@y.z"); strings.ContainsAny(got, "\r\n") {
		t.Errorf("an unparseable address must still be stripped of line breaks, got %q", got)
	}
}

func TestBuildMessagePrefersHTMLBody(t *testing.T) {
	textOnly := buildMessage("from@test", domain.EmailMessage{
		To: "to@test", Subject: "s", TextBody: "plain text",
	})
	if !strings.Contains(textOnly, "Content-Type: text/plain; charset=UTF-8") {
		t.Errorf("expected a plain-text content type:\n%s", textOnly)
	}
	if !strings.HasSuffix(textOnly, "plain text") {
		t.Errorf("expected the text body at the end:\n%s", textOnly)
	}

	withHTML := buildMessage("from@test", domain.EmailMessage{
		To: "to@test", Subject: "s", TextBody: "plain text", HTMLBody: "<p>rich</p>",
	})
	if !strings.Contains(withHTML, "Content-Type: text/html; charset=UTF-8") {
		t.Errorf("expected an HTML content type:\n%s", withHTML)
	}
	if !strings.HasSuffix(withHTML, "<p>rich</p>") {
		t.Errorf("expected the HTML body at the end:\n%s", withHTML)
	}
}

// TestLogSenderDoesNotLogBodies is a privacy requirement, not a formatting preference.
//
// Bodies carry live verification and password-reset links. Writing them to a log would make
// log access equivalent to account takeover — and logs are copied, shipped and retained far
// more casually than a database.
func TestLogSenderDoesNotLogBodies(t *testing.T) {
	var captured strings.Builder
	logger := slog.New(slog.NewTextHandler(&captured, nil))

	sender := NewLogSender(logger)
	err := sender.Send(context.Background(), domain.EmailMessage{
		To:       "user@example.com",
		Subject:  "Reset your password",
		TextBody: "https://junto.test/reset-password?token=SUPER-SECRET-TOKEN",
		HTMLBody: "<a href=\"https://junto.test/reset-password?token=SUPER-SECRET-TOKEN\">reset</a>",
	})
	if err != nil {
		t.Fatalf("sending: %v", err)
	}

	out := captured.String()
	if strings.Contains(out, "SUPER-SECRET-TOKEN") {
		t.Errorf("the log leaked a live token:\n%s", out)
	}
	// Metadata is still useful and safe.
	if !strings.Contains(out, "user@example.com") || !strings.Contains(out, "Reset your password") {
		t.Errorf("recipient and subject should be logged:\n%s", out)
	}
}

func TestLogSenderDefaultsToPackageLogger(t *testing.T) {
	if NewLogSender(nil) == nil {
		t.Fatal("a nil logger must fall back to the default, not panic later")
	}
}

func TestSMTPSenderHonoursCancelledContext(t *testing.T) {
	sender := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "a@b.test"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Fails fast on the context rather than attempting a doomed dial.
	if err := sender.Send(ctx, domain.EmailMessage{To: "x@y.test"}); err == nil {
		t.Error("a cancelled context must abort the send")
	}
}

func TestSMTPSenderReportsDialFailure(t *testing.T) {
	// Port 1 is not going to be listening. The point is that the failure surfaces as an
	// error rather than a panic or a silent success — the service logs it and continues,
	// which is only safe if it is actually reported.
	sender := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "a@b.test"})

	err := sender.Send(context.Background(), domain.EmailMessage{To: "x@y.test", Subject: "s"})
	if err == nil {
		t.Fatal("dialling a closed port must return an error")
	}
	if !strings.Contains(err.Error(), "email:") {
		t.Errorf("error should be attributed to this package, got %v", err)
	}
}

var _ io.Writer = (*strings.Builder)(nil)
