package smtp

import (
	"strings"
	"testing"
)

func TestParseMessageMultipart(t *testing.T) {
	raw := strings.Join([]string{
		"From: Dev <dev@example.com>",
		"To: Test <test@example.com>",
		"Subject: Test message",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=abc123",
		"",
		"--abc123",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello world",
		"--abc123",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>hello world</p>",
		"--abc123",
		"Content-Type: text/plain",
		"Content-Disposition: attachment; filename=note.txt",
		"Content-Transfer-Encoding: base64",
		"",
		"aGVsbG8gYXR0YWNobWVudA==",
		"--abc123--",
		"",
	}, "\r\n")

	msg, err := ParseMessage(strings.NewReader(raw), "envelope@example.com", []string{"rcpt@example.com"})
	if err != nil {
		t.Fatalf("ParseMessage() error: %v", err)
	}

	if msg.EnvelopeFrom != "envelope@example.com" {
		t.Fatalf("unexpected envelope from: %q", msg.EnvelopeFrom)
	}
	if msg.HeaderFrom != "dev@example.com" {
		t.Fatalf("unexpected header from: %q", msg.HeaderFrom)
	}
	if len(msg.ReplyTo) != 0 {
		t.Fatalf("unexpected reply-to: %#v", msg.ReplyTo)
	}
	if len(msg.To) != 1 || msg.To[0] != "rcpt@example.com" {
		t.Fatalf("unexpected to: %#v", msg.To)
	}
	if msg.Subject != "Test message" {
		t.Fatalf("unexpected subject: %q", msg.Subject)
	}
	if !strings.Contains(msg.TextBody, "hello world") {
		t.Fatalf("unexpected text body: %q", msg.TextBody)
	}
	if !strings.Contains(msg.HTMLBody, "<p>hello world</p>") {
		t.Fatalf("unexpected html body: %q", msg.HTMLBody)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("unexpected attachment count: %d", len(msg.Attachments))
	}
	if string(msg.Attachments[0].Data) != "hello attachment" {
		t.Fatalf("unexpected attachment body: %q", string(msg.Attachments[0].Data))
	}
}

func TestParseMessageWithoutHeaderFromKeepsEnvelopeOnly(t *testing.T) {
	raw := strings.Join([]string{
		"To: Header To <header-to@example.com>",
		"Subject: No from header",
		"",
		"hello",
	}, "\r\n")

	msg, err := ParseMessage(strings.NewReader(raw), "envelope@example.com", nil)
	if err != nil {
		t.Fatalf("ParseMessage() error: %v", err)
	}

	if msg.EnvelopeFrom != "envelope@example.com" {
		t.Fatalf("unexpected envelope from: %q", msg.EnvelopeFrom)
	}
	if msg.HeaderFrom != "" {
		t.Fatalf("expected empty header from, got %q", msg.HeaderFrom)
	}
	if len(msg.ReplyTo) != 0 {
		t.Fatalf("expected empty reply-to, got %#v", msg.ReplyTo)
	}
	if len(msg.To) != 0 {
		t.Fatalf("expected envelope recipients only, got %#v", msg.To)
	}
}

func TestParseMessageMalformedHeaderFromAndValidReplyTo(t *testing.T) {
	raw := strings.Join([]string{
		"From: not a mailbox",
		"Reply-To: Reply <reply@example.com>, Other <other@example.com>",
		"Subject: malformed from",
		"",
		"hello",
	}, "\r\n")

	msg, err := ParseMessage(strings.NewReader(raw), "envelope@example.com", []string{"rcpt@example.com"})
	if err != nil {
		t.Fatalf("ParseMessage() error: %v", err)
	}

	if msg.EnvelopeFrom != "envelope@example.com" {
		t.Fatalf("unexpected envelope from: %q", msg.EnvelopeFrom)
	}
	if msg.HeaderFrom != "" {
		t.Fatalf("expected empty header from, got %q", msg.HeaderFrom)
	}
	if len(msg.ReplyTo) != 2 || msg.ReplyTo[0] != "reply@example.com" || msg.ReplyTo[1] != "other@example.com" {
		t.Fatalf("unexpected reply-to: %#v", msg.ReplyTo)
	}
}

func TestParseMessageMIMEEncodedDisplayName(t *testing.T) {
	raw := strings.Join([]string{
		"From: =?UTF-8?Q?D=C3=A9v?= <dev@example.com>",
		"Reply-To: =?UTF-8?Q?R=C3=A9ply?= <reply@example.com>",
		"Subject: Encoded header",
		"",
		"hello",
	}, "\r\n")

	msg, err := ParseMessage(strings.NewReader(raw), "envelope@example.com", []string{"rcpt@example.com"})
	if err != nil {
		t.Fatalf("ParseMessage() error: %v", err)
	}

	if msg.HeaderFrom != "dev@example.com" {
		t.Fatalf("unexpected header from: %q", msg.HeaderFrom)
	}
	if len(msg.ReplyTo) != 1 || msg.ReplyTo[0] != "reply@example.com" {
		t.Fatalf("unexpected reply-to: %#v", msg.ReplyTo)
	}
}
