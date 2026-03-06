package smtp

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

const (
	defaultDomain         = "smtp-cloud-relay.local"
	defaultMaxMessageSize = 25 << 20
	defaultMaxRecipients  = 100
	defaultReadTimeout    = 30 * time.Second
	defaultWriteTimeout   = 30 * time.Second
	defaultHandlerTimeout = 45 * time.Second
)

// MessageHandler handles parsed SMTP messages.
type MessageHandler interface {
	HandleMessage(ctx context.Context, msg email.Message) error
}

// MessageHandlerFunc adapts a function into a MessageHandler.
type MessageHandlerFunc func(ctx context.Context, msg email.Message) error

func (f MessageHandlerFunc) HandleMessage(ctx context.Context, msg email.Message) error {
	return f(ctx, msg)
}

// AuthProvider validates SMTP AUTH PLAIN credentials.
type AuthProvider interface {
	AuthPlain(username, password string) error
}

// StaticAuthProvider provides static username/password SMTP auth.
type StaticAuthProvider struct {
	Username string
	Password string
}

func (p *StaticAuthProvider) AuthPlain(username, password string) error {
	if subtle.ConstantTimeCompare([]byte(username), []byte(p.Username)) != 1 {
		return errors.New("invalid username or password")
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(p.Password)) != 1 {
		return errors.New("invalid username or password")
	}
	return nil
}

// Config controls SMTP listener runtime behavior.
type Config struct {
	ListenAddr      string
	SMTPSListenAddr string
	Domain          string

	AllowedCIDRs []netip.Prefix

	RequireAuth bool
	RequireTLS  bool

	StartTLSEnabled bool
	TLSConfig       *tls.Config

	MaxMessageBytes int64
	MaxRecipients   int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	HandlerTimeout  time.Duration
}

type Server struct {
	smtpServer  *gosmtp.Server
	smtpsServer *gosmtp.Server
	logger      *slog.Logger
	readyCh     chan struct{}
	readyOnce   sync.Once
}

func NewServer(cfg Config, logger *slog.Logger, handler MessageHandler, authProvider AuthProvider) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if handler == nil {
		return nil, fmt.Errorf("smtp handler is required")
	}
	if len(cfg.AllowedCIDRs) == 0 {
		return nil, fmt.Errorf("at least one allowed CIDR is required")
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" && strings.TrimSpace(cfg.SMTPSListenAddr) == "" {
		return nil, fmt.Errorf("at least one SMTP listener address is required")
	}
	if cfg.RequireAuth && authProvider == nil {
		return nil, fmt.Errorf("auth provider is required when auth is enforced")
	}
	if (cfg.StartTLSEnabled || strings.TrimSpace(cfg.SMTPSListenAddr) != "") && cfg.TLSConfig == nil {
		return nil, fmt.Errorf("tls config is required when STARTTLS or SMTPS is enabled")
	}

	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		domain = defaultDomain
	}
	maxMessageBytes := cfg.MaxMessageBytes
	if maxMessageBytes <= 0 {
		maxMessageBytes = defaultMaxMessageSize
	}
	maxRecipients := cfg.MaxRecipients
	if maxRecipients <= 0 {
		maxRecipients = defaultMaxRecipients
	}
	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = defaultReadTimeout
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	handlerTimeout := cfg.HandlerTimeout
	if handlerTimeout <= 0 {
		handlerTimeout = defaultHandlerTimeout
	}

	policy := sessionPolicy{
		allowedCIDRs:   cfg.AllowedCIDRs,
		requireAuth:    cfg.RequireAuth,
		requireTLS:     cfg.RequireTLS,
		authProvider:   authProvider,
		handler:        handler,
		logger:         logger,
		handlerTimeout: handlerTimeout,
	}
	backend := &backend{policy: policy, logger: logger}

	var smtpServer *gosmtp.Server
	if strings.TrimSpace(cfg.ListenAddr) != "" {
		smtpServer = newSMTPListener(cfg.ListenAddr, domain, backend, !cfg.RequireTLS, maxMessageBytes, maxRecipients, readTimeout, writeTimeout)
		if cfg.StartTLSEnabled {
			smtpServer.TLSConfig = cfg.TLSConfig
		}
	}

	var smtpsServer *gosmtp.Server
	if strings.TrimSpace(cfg.SMTPSListenAddr) != "" {
		smtpsServer = newSMTPListener(cfg.SMTPSListenAddr, domain, backend, false, maxMessageBytes, maxRecipients, readTimeout, writeTimeout)
		smtpsServer.TLSConfig = cfg.TLSConfig
	}

	return &Server{
		smtpServer:  smtpServer,
		smtpsServer: smtpsServer,
		logger:      logger,
		readyCh:     make(chan struct{}),
	}, nil
}

func newSMTPListener(addr, domain string, backend *backend, allowInsecureAuth bool, maxMessageBytes int64, maxRecipients int, readTimeout, writeTimeout time.Duration) *gosmtp.Server {
	srv := gosmtp.NewServer(backend)
	srv.Addr = addr
	srv.Domain = domain
	srv.AllowInsecureAuth = allowInsecureAuth
	srv.ReadTimeout = readTimeout
	srv.WriteTimeout = writeTimeout
	srv.MaxMessageBytes = maxMessageBytes
	srv.MaxRecipients = maxRecipients
	return srv
}

// Ready returns a channel closed after all configured listeners are bound.
func (s *Server) Ready() <-chan struct{} {
	return s.readyCh
}

// Start runs configured SMTP listeners until context cancellation or fatal error.
func (s *Server) Start(ctx context.Context) error {
	if s.smtpServer == nil && s.smtpsServer == nil {
		return fmt.Errorf("no smtp listeners configured")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type boundListener struct {
		name string
		srv  *gosmtp.Server
		ln   net.Listener
	}
	listeners := make([]boundListener, 0, 2)

	if s.smtpServer != nil {
		ln, err := net.Listen("tcp", s.smtpServer.Addr)
		if err != nil {
			return fmt.Errorf("bind smtp listener %q: %w", s.smtpServer.Addr, err)
		}
		listeners = append(listeners, boundListener{name: "smtp", srv: s.smtpServer, ln: ln})
	}

	if s.smtpsServer != nil {
		ln, err := net.Listen("tcp", s.smtpsServer.Addr)
		if err != nil {
			for _, l := range listeners {
				_ = l.ln.Close()
			}
			return fmt.Errorf("bind smtps listener %q: %w", s.smtpsServer.Addr, err)
		}
		tlsLn := tls.NewListener(ln, s.smtpsServer.TLSConfig)
		listeners = append(listeners, boundListener{name: "smtps", srv: s.smtpsServer, ln: tlsLn})
	}

	s.readyOnce.Do(func() { close(s.readyCh) })

	errCh := make(chan error, 2)
	var wg sync.WaitGroup

	for _, l := range listeners {
		l := l
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch l.name {
			case "smtp":
				s.logger.Info("starting smtp server", "addr", l.srv.Addr, "starttls_enabled", l.srv.TLSConfig != nil)
			case "smtps":
				s.logger.Info("starting smtps server", "addr", l.srv.Addr)
			}
			if err := l.srv.Serve(l.ln); err != nil && !isClosedServerError(err) {
				errCh <- fmt.Errorf("%s serve: %w", l.name, err)
			}
		}()
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		if err := s.Close(); err != nil && !isClosedServerError(err) {
			s.logger.Warn("error while closing smtp servers", "error", err)
		}
	}()

	wait := make(chan struct{})
	go func() {
		wg.Wait()
		close(wait)
	}()

	select {
	case err := <-errCh:
		cancel()
		<-stopped
		<-wait
		return err
	case <-wait:
		if ctx.Err() != nil {
			return nil
		}
		return nil
	case <-ctx.Done():
		<-stopped
		<-wait
		return nil
	}
}

func (s *Server) Close() error {
	var firstErr error
	if s.smtpServer != nil {
		if err := s.smtpServer.Close(); err != nil && !isClosedServerError(err) {
			firstErr = err
		}
	}
	if s.smtpsServer != nil {
		if err := s.smtpsServer.Close(); err != nil && !isClosedServerError(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type sessionPolicy struct {
	allowedCIDRs   []netip.Prefix
	requireAuth    bool
	requireTLS     bool
	authProvider   AuthProvider
	handler        MessageHandler
	logger         *slog.Logger
	handlerTimeout time.Duration
}

type backend struct {
	policy sessionPolicy
	logger *slog.Logger
}

func (b *backend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	remoteIP, remoteAddr, err := remoteAddrIP(c)
	if err != nil {
		b.logger.Warn("rejecting smtp connection with invalid remote address", "error", err)
		return nil, &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "access denied"}
	}
	if !isAddrAllowed(remoteIP, b.policy.allowedCIDRs) {
		b.logger.Warn("rejecting smtp connection from non-allowlisted address", "remote_addr", remoteAddr)
		return nil, &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "access denied"}
	}

	return &session{
		conn:         c,
		remoteAddr:   remoteAddr,
		handler:      b.policy.handler,
		authProvider: b.policy.authProvider,
		requireAuth:  b.policy.requireAuth,
		requireTLS:   b.policy.requireTLS,
		logger:       b.policy.logger,
		timeout:      b.policy.handlerTimeout,
	}, nil
}

type session struct {
	conn         *gosmtp.Conn
	from         string
	to           []string
	authed       bool
	remoteAddr   string
	handler      MessageHandler
	authProvider AuthProvider
	requireAuth  bool
	requireTLS   bool
	logger       *slog.Logger
	timeout      time.Duration
}

func (s *session) AuthMechanisms() []string {
	if !s.requireAuth || s.authProvider == nil {
		return nil
	}
	return []string{sasl.Plain}
}

func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.requireAuth || s.authProvider == nil {
		return nil, gosmtp.ErrAuthUnsupported
	}
	if !strings.EqualFold(mech, sasl.Plain) {
		return nil, gosmtp.ErrAuthUnknownMechanism
	}

	return sasl.NewPlainServer(func(_ string, username string, password string) error {
		if err := s.authProvider.AuthPlain(username, password); err != nil {
			return &gosmtp.SMTPError{Code: 535, EnhancedCode: gosmtp.EnhancedCode{5, 7, 8}, Message: "authentication failed"}
		}
		s.authed = true
		return nil
	}), nil
}

func (s *session) Mail(from string, _ *gosmtp.MailOptions) error {
	if err := s.enforceSessionPolicy(); err != nil {
		return err
	}
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if err := s.enforceSessionPolicy(); err != nil {
		return err
	}
	s.to = append(s.to, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if err := s.enforceSessionPolicy(); err != nil {
		return err
	}

	msg, err := ParseMessage(r, s.from, s.to)
	if err != nil {
		return &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 6, 0}, Message: "invalid message content"}
	}

	s.logger.Info("smtp message received",
		"from", msg.From,
		"to", msg.To,
		"subject", msg.Subject,
		"attachments", len(msg.Attachments),
		"remote_addr", s.remoteAddr,
	)

	handleCtx := context.Background()
	cancel := func() {}
	if s.timeout > 0 {
		handleCtx, cancel = context.WithTimeout(handleCtx, s.timeout)
	}
	defer cancel()

	if err := s.handler.HandleMessage(handleCtx, msg); err != nil {
		var smtpErr *gosmtp.SMTPError
		if errors.As(err, &smtpErr) {
			return smtpErr
		}
		s.logger.Error("message handler failed", "error", err, "subject", msg.Subject)
		return fmt.Errorf("handle parsed message: %w", err)
	}
	return nil
}

func (s *session) Reset() {
	s.from = ""
	s.to = nil
}

func (s *session) Logout() error {
	return nil
}

func (s *session) enforceSessionPolicy() error {
	if s.requireTLS && !s.isTLSActive() {
		return &gosmtp.SMTPError{Code: 530, EnhancedCode: gosmtp.EnhancedCode{5, 7, 0}, Message: "Must issue STARTTLS first"}
	}
	if s.requireAuth && !s.authed {
		return &gosmtp.SMTPError{Code: 530, EnhancedCode: gosmtp.EnhancedCode{5, 7, 0}, Message: "Authentication required"}
	}
	return nil
}

func (s *session) isTLSActive() bool {
	if s.conn == nil {
		return false
	}
	_, ok := s.conn.TLSConnectionState()
	return ok
}

func remoteAddrIP(c *gosmtp.Conn) (netip.Addr, string, error) {
	if c == nil || c.Conn() == nil || c.Conn().RemoteAddr() == nil {
		return netip.Addr{}, "", fmt.Errorf("missing remote address")
	}

	remote := c.Conn().RemoteAddr()
	remoteAddr := remote.String()

	switch a := remote.(type) {
	case *net.TCPAddr:
		addr, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return netip.Addr{}, remoteAddr, fmt.Errorf("invalid tcp remote ip")
		}
		return addr.Unmap(), remoteAddr, nil
	case *net.UDPAddr:
		addr, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return netip.Addr{}, remoteAddr, fmt.Errorf("invalid udp remote ip")
		}
		return addr.Unmap(), remoteAddr, nil
	case *net.IPAddr:
		addr, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return netip.Addr{}, remoteAddr, fmt.Errorf("invalid ip remote ip")
		}
		return addr.Unmap(), remoteAddr, nil
	}

	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, remoteAddr, fmt.Errorf("parse remote ip: %w", err)
	}
	return addr.Unmap(), remoteAddr, nil
}

func isAddrAllowed(addr netip.Addr, allowed []netip.Prefix) bool {
	for _, p := range allowed {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func isClosedServerError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if errors.Is(err, gosmtp.ErrServerClosed) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "closed")
}
