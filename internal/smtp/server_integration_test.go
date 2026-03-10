package smtp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"net/textproto"
	"strings"
	"testing"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestServerReadySignal(t *testing.T) {
	addr := freeTCPAddr(t)
	srv, err := NewServer(Config{
		ListenAddr:   addr,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:  true,
	}, testLogger(), MessageHandlerFunc(func(context.Context, email.Message) error { return nil }), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	select {
	case <-srv.Ready():
		t.Fatal("ready channel must not be closed before Start")
	default:
	}

	stop := startServer(t, srv)
	defer stop()

	select {
	case <-srv.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("ready channel was not closed after startup")
	}
}

func TestServerShutdownImmediatelyAfterReady(t *testing.T) {
	addr := freeTCPAddr(t)
	srv, err := NewServer(Config{
		ListenAddr:   addr,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:  true,
	}, testLogger(), MessageHandlerFunc(func(context.Context, email.Message) error { return nil }), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("server failed before ready: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server readiness")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server shutdown error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for immediate shutdown after readiness")
	}
}

func TestServerRejectsSequentialSecondStart(t *testing.T) {
	addr := freeTCPAddr(t)
	srv, err := NewServer(Config{
		ListenAddr:   addr,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:  true,
	}, testLogger(), MessageHandlerFunc(func(context.Context, email.Message) error { return nil }), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	stop := startServer(t, srv)
	stop()

	err = srv.Start(context.Background())
	if !errors.Is(err, ErrServerAlreadyStarted) {
		t.Fatalf("expected ErrServerAlreadyStarted, got %v", err)
	}
}

func TestServerRejectsConcurrentSecondStart(t *testing.T) {
	addr := freeTCPAddr(t)
	srv, err := NewServer(Config{
		ListenAddr:   addr,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:  true,
	}, testLogger(), MessageHandlerFunc(func(context.Context, email.Message) error { return nil }), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startCh := make(chan struct{})
	errCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-startCh
			errCh <- srv.Start(ctx)
		}()
	}
	close(startCh)

	var alreadyStartedCount int
	var runningStart bool
	readyCh := srv.Ready()
	for i := 0; i < 2; i++ {
		select {
		case <-readyCh:
			runningStart = true
			readyCh = nil
			cancel()
		case err := <-errCh:
			if errors.Is(err, ErrServerAlreadyStarted) {
				alreadyStartedCount++
				continue
			}
			if err != nil {
				t.Fatalf("unexpected start error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for start results")
		}
	}

	if !runningStart {
		t.Fatal("expected one start call to win and run the server")
	}
	if alreadyStartedCount != 1 {
		t.Fatalf("expected one ErrServerAlreadyStarted result, got %d", alreadyStartedCount)
	}
}

func TestServerCloseWaitsForStartToExit(t *testing.T) {
	addr := freeTCPAddr(t)
	srv, err := NewServer(Config{
		ListenAddr:   addr,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:  true,
	}, testLogger(), MessageHandlerFunc(func(context.Context, email.Message) error { return nil }), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	startReturned := make(chan struct{})
	startErrCh := make(chan error, 1)
	go func() {
		err := srv.Start(context.Background())
		close(startReturned)
		startErrCh <- err
	}()

	select {
	case <-srv.Ready():
	case err := <-startErrCh:
		t.Fatalf("server failed before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server readiness")
	}

	closeReturned := make(chan struct{})
	closeErrCh := make(chan error, 1)
	go func() {
		closeErrCh <- srv.Close()
		close(closeReturned)
	}()

	select {
	case <-closeReturned:
		select {
		case <-startReturned:
		default:
			t.Fatal("Close returned before Start exited")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Close")
	}

	if err := <-closeErrCh; err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if err := <-startErrCh; err != nil {
		t.Fatalf("Start() error: %v", err)
	}
}

func TestServerStartTLSAndAuthPlainFlow(t *testing.T) {
	addr := freeTCPAddr(t)
	messages := make(chan email.Message, 1)

	srv, err := NewServer(Config{
		ListenAddr:      addr,
		AllowedCIDRs:    []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:     true,
		RequireTLS:      true,
		StartTLSEnabled: true,
		TLSConfig:       testTLSConfig(t),
	}, testLogger(), MessageHandlerFunc(func(_ context.Context, msg email.Message) error {
		messages <- msg
		return nil
	}), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	stop := startServer(t, srv)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn.Close()

	tp := textproto.NewConn(conn)
	defer tp.Close()

	if _, _, err := tp.ReadResponse(220); err != nil {
		t.Fatalf("read greeting: %v", err)
	}

	tp.PrintfLine("EHLO localhost")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("ehlo response: %v", err)
	}

	tp.PrintfLine("MAIL FROM:<from@example.com>")
	if _, _, err := tp.ReadResponse(530); err != nil {
		t.Fatalf("expected MAIL before TLS to be rejected: %v", err)
	}

	tp.PrintfLine("STARTTLS")
	if _, _, err := tp.ReadResponse(220); err != nil {
		t.Fatalf("starttls response: %v", err)
	}

	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("tls handshake: %v", err)
	}

	tp = textproto.NewConn(tlsConn)

	tp.PrintfLine("EHLO localhost")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("ehlo after starttls: %v", err)
	}

	authLine := base64.StdEncoding.EncodeToString([]byte("\x00jira\x00secret"))
	tp.PrintfLine("AUTH PLAIN %s", authLine)
	if _, _, err := tp.ReadResponse(235); err != nil {
		t.Fatalf("auth response: %v", err)
	}

	tp.PrintfLine("MAIL FROM:<from@example.com>")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("mail response: %v", err)
	}
	tp.PrintfLine("RCPT TO:<to@example.com>")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("rcpt response: %v", err)
	}
	tp.PrintfLine("DATA")
	if _, _, err := tp.ReadResponse(354); err != nil {
		t.Fatalf("data response: %v", err)
	}

	tp.PrintfLine("Subject: Integration test")
	tp.PrintfLine("From: from@example.com")
	tp.PrintfLine("To: to@example.com")
	tp.PrintfLine("")
	tp.PrintfLine("hello relay")
	tp.PrintfLine(".")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("queued response: %v", err)
	}

	select {
	case msg := <-messages:
		if msg.Subject != "Integration test" {
			t.Fatalf("unexpected subject: %q", msg.Subject)
		}
		if msg.From != "from@example.com" {
			t.Fatalf("unexpected from: %q", msg.From)
		}
		if len(msg.To) != 1 || msg.To[0] != "to@example.com" {
			t.Fatalf("unexpected to: %#v", msg.To)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message handler")
	}

	tp.PrintfLine("QUIT")
	if _, _, err := tp.ReadResponse(221); err != nil {
		t.Fatalf("quit response: %v", err)
	}
}

func TestServerReturnsHandlerSMTPErrorOnWire(t *testing.T) {
	addr := freeTCPAddr(t)
	handlerErr := &gosmtp.SMTPError{
		Code:         554,
		EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
		Message:      "policy rejection",
	}

	srv, err := NewServer(Config{
		ListenAddr:      addr,
		AllowedCIDRs:    []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:     true,
		RequireTLS:      true,
		StartTLSEnabled: true,
		TLSConfig:       testTLSConfig(t),
	}, testLogger(), MessageHandlerFunc(func(context.Context, email.Message) error {
		return handlerErr
	}), &StaticAuthProvider{
		Username: "jira",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	stop := startServer(t, srv)
	defer stop()

	tp := dialAuthenticatedClient(t, addr)
	defer tp.Close()

	sendTestMessage(t, tp, "policy@example.com", "to@example.com", "Rejected on purpose", "hello relay")

	_, msg, err := tp.ReadResponse(554)
	if err != nil {
		t.Fatalf("expected handler SMTP error response: %v", err)
	}
	if !strings.Contains(msg, "5.7.1") {
		t.Fatalf("expected enhanced status code in response, got %q", msg)
	}
	if !strings.Contains(msg, "policy rejection") {
		t.Fatalf("expected handler message in response, got %q", msg)
	}
}

func startServer(t *testing.T, srv *Server) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("server failed before ready: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server readiness")
	}

	return func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("server shutdown error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for server shutdown")
		}
	}
}

func dialAuthenticatedClient(t *testing.T, addr string) *textproto.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}

	tp := textproto.NewConn(conn)

	if _, _, err := tp.ReadResponse(220); err != nil {
		tp.Close()
		t.Fatalf("read greeting: %v", err)
	}

	tp.PrintfLine("EHLO localhost")
	if _, _, err := tp.ReadResponse(250); err != nil {
		tp.Close()
		t.Fatalf("ehlo response: %v", err)
	}

	tp.PrintfLine("STARTTLS")
	if _, _, err := tp.ReadResponse(220); err != nil {
		tp.Close()
		t.Fatalf("starttls response: %v", err)
	}

	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err := tlsConn.Handshake(); err != nil {
		tp.Close()
		t.Fatalf("tls handshake: %v", err)
	}

	tp = textproto.NewConn(tlsConn)

	tp.PrintfLine("EHLO localhost")
	if _, _, err := tp.ReadResponse(250); err != nil {
		tp.Close()
		t.Fatalf("ehlo after starttls: %v", err)
	}

	authLine := base64.StdEncoding.EncodeToString([]byte("\x00jira\x00secret"))
	tp.PrintfLine("AUTH PLAIN %s", authLine)
	if _, _, err := tp.ReadResponse(235); err != nil {
		tp.Close()
		t.Fatalf("auth response: %v", err)
	}

	return tp
}

func sendTestMessage(t *testing.T, tp *textproto.Conn, from, to, subject, body string) {
	t.Helper()

	tp.PrintfLine("MAIL FROM:<%s>", from)
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("mail response: %v", err)
	}
	tp.PrintfLine("RCPT TO:<%s>", to)
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("rcpt response: %v", err)
	}
	tp.PrintfLine("DATA")
	if _, _, err := tp.ReadResponse(354); err != nil {
		t.Fatalf("data response: %v", err)
	}

	tp.PrintfLine("Subject: %s", subject)
	tp.PrintfLine("From: %s", from)
	tp.PrintfLine("To: %s", to)
	tp.PrintfLine("")
	tp.PrintfLine("%s", body)
	tp.PrintfLine(".")
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp listen unavailable in this test environment: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate() error: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair() error: %v", err)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
}
