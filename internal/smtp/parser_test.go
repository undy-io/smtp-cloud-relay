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

	if msg.From != "dev@example.com" {
		t.Fatalf("unexpected from: %q", msg.From)
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
