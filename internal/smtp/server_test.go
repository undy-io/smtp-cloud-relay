package smtp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestStaticAuthProvider(t *testing.T) {
	p := &StaticAuthProvider{Username: "jira", Password: "secret"}

	if err := p.AuthPlain("jira", "secret"); err != nil {
		t.Fatalf("AuthPlain() success error: %v", err)
	}
	if err := p.AuthPlain("jira", "bad"); err == nil {
		t.Fatal("expected auth error for bad password")
	}
	if err := p.AuthPlain("bad", "secret"); err == nil {
		t.Fatal("expected auth error for bad username")
	}
}

func TestIsAddrAllowed(t *testing.T) {
	allowed := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}

	if !isAddrAllowed(netip.MustParseAddr("127.0.0.1"), allowed) {
		t.Fatal("expected loopback to be allowed")
	}
	if !isAddrAllowed(netip.MustParseAddr("10.2.3.4"), allowed) {
		t.Fatal("expected RFC1918 address to be allowed")
	}
	if isAddrAllowed(netip.MustParseAddr("192.168.1.1"), allowed) {
		t.Fatal("expected address to be denied")
	}
}

func TestSessionEnforcePolicy(t *testing.T) {
	t.Run("requires auth", func(t *testing.T) {
		s := &session{requireAuth: true}
		err := s.Mail("from@example.com", nil)
		assertSMTPErrorCode(t, err, 530)
	})

	t.Run("requires tls", func(t *testing.T) {
		s := &session{requireAuth: false, requireTLS: true}
		err := s.Mail("from@example.com", nil)
		assertSMTPErrorCode(t, err, 530)
		if !strings.Contains(err.Error(), "STARTTLS") {
			t.Fatalf("expected STARTTLS error, got: %v", err)
		}
	})

	t.Run("auth and tls satisfied", func(t *testing.T) {
		s := &session{requireAuth: true, authed: true}
		if err := s.Mail("from@example.com", nil); err != nil {
			t.Fatalf("Mail() error: %v", err)
		}
	})
}

func TestSessionAuthPlain(t *testing.T) {
	s := &session{
		requireAuth:  true,
		authProvider: &StaticAuthProvider{Username: "jira", Password: "secret"},
	}

	if got := s.AuthMechanisms(); len(got) != 1 || got[0] != "PLAIN" {
		t.Fatalf("unexpected auth mechanisms: %#v", got)
	}

	auth, err := s.Auth("PLAIN")
	if err != nil {
		t.Fatalf("Auth() error: %v", err)
	}

	_, done, err := auth.Next([]byte("\x00jira\x00secret"))
	if err != nil {
		t.Fatalf("plain auth failed: %v", err)
	}
	if !done {
		t.Fatal("expected auth exchange to be done")
	}
	if !s.authed {
		t.Fatal("expected session to be marked authed")
	}

	s.authed = false
	auth, err = s.Auth("PLAIN")
	if err != nil {
		t.Fatalf("Auth() error: %v", err)
	}

	_, _, err = auth.Next([]byte("\x00jira\x00bad"))
	assertSMTPErrorCode(t, err, 535)

	_, err = s.Auth("LOGIN")
	if !errors.Is(err, gosmtp.ErrAuthUnknownMechanism) {
		t.Fatalf("expected unknown mechanism error, got: %v", err)
	}
}

func TestSessionDataPassesSMTPError(t *testing.T) {
	wantErr := &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "busy"}

	s := &session{
		requireAuth: false,
		handler: MessageHandlerFunc(func(_ context.Context, _ email.Message) error {
			return wantErr
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		from:   "from@example.com",
		to:     []string{"to@example.com"},
	}

	raw := strings.NewReader("From: from@example.com\r\nTo: to@example.com\r\nSubject: test\r\n\r\nhello\r\n")
	err := s.Data(raw)
	assertSMTPErrorCode(t, err, 451)
}

func assertSMTPErrorCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SMTP error %d, got nil", want)
	}
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) {
		t.Fatalf("expected *SMTPError, got %T (%v)", err, err)
	}
	if smtpErr.Code != want {
		t.Fatalf("unexpected smtp code: got %d want %d", smtpErr.Code, want)
	}
}
