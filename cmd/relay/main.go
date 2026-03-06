package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/config"
	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/observability"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/acs"
	smtprelay "github.com/undy-io/smtp-cloud-relay/internal/smtp"
)

const maxDuration = time.Duration(1<<63 - 1)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	allowedCIDRs, err := config.ParseCIDRs(cfg.SMTPAllowedCIDRs)
	if err != nil {
		logger.Error("failed to parse SMTP allowlist", "error", err)
		os.Exit(1)
	}

	var tlsCfg *tls.Config
	if cfg.SMTPStartTLSEnabled || strings.TrimSpace(cfg.SMTPSListenAddr) != "" {
		tlsCfg, err = loadSMTPServerTLS(cfg.SMTPTLSCertFile, cfg.SMTPTLSKeyFile)
		if err != nil {
			logger.Error("failed to load SMTP TLS certificate", "error", err)
			os.Exit(1)
		}
	}

	handler, handlerTimeout, err := buildMessageHandler(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize delivery handler", "error", err)
		os.Exit(1)
	}

	authProvider := &smtprelay.StaticAuthProvider{
		Username: cfg.SMTPAuthUsername,
		Password: cfg.SMTPAuthPassword,
	}

	smtpServer, err := smtprelay.NewServer(smtprelay.Config{
		ListenAddr:      cfg.SMTPListenAddr,
		SMTPSListenAddr: cfg.SMTPSListenAddr,
		AllowedCIDRs:    allowedCIDRs,
		RequireAuth:     cfg.SMTPRequireAuth,
		RequireTLS:      cfg.SMTPRequireTLS,
		StartTLSEnabled: cfg.SMTPStartTLSEnabled,
		TLSConfig:       tlsCfg,
		HandlerTimeout:  handlerTimeout,
	}, logger, handler, authProvider)
	if err != nil {
		logger.Error("failed to create smtp server", "error", err)
		os.Exit(1)
	}

	var ready atomic.Bool
	ready.Store(false)
	obsServer := observability.NewServer(cfg.HTTPListenAddr, logger, ready.Load)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() {
		if err := smtpServer.Start(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		if err := obsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-smtpServer.Ready():
		ready.Store(true)
		logger.Info("smtp listeners are ready")
	case <-ctx.Done():
		logger.Info("shutdown signal received during startup")
	case err := <-errCh:
		logger.Error("server failed during startup", "error", err)
		stop()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("server failed", "error", err)
		ready.Store(false)
		stop()
	}

	ready.Store(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := smtpServer.Close(); err != nil {
		logger.Error("failed to close smtp server", "error", err)
	}
	if err := obsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown observability server", "error", err)
	}
}

func buildMessageHandler(cfg config.Config, logger *slog.Logger) (smtprelay.MessageHandler, time.Duration, error) {
	inflight := make(chan struct{}, cfg.SMTPMaxInflightSends)
	sendTimeout := time.Duration(0)
	handlerTimeout := time.Duration(0)

	relayBusyError := &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 2}, Message: "relay busy, try again later"}
	temporarySendError := &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "temporary relay failure"}

	sendFunc := func(context.Context, email.Message) error { return nil }
	switch cfg.DeliveryMode {
	case "noop":
		sendFunc = func(_ context.Context, msg email.Message) error {
			logger.Info("noop delivery accepted",
				"from", msg.From,
				"to", msg.To,
				"subject", msg.Subject,
				"attachments", len(msg.Attachments),
			)
			return nil
		}
	case "acs":
		baseDelay := time.Duration(cfg.ACSRetryBaseDelayMS) * time.Millisecond
		httpTimeout := time.Duration(cfg.ACSHTTPTimeoutMS) * time.Millisecond
		retryTransportBudget := multiplyDuration(httpTimeout, cfg.ACSRetryAttempts)
		retryBackoffBudget := retryBackoffTotal(baseDelay, cfg.ACSRetryAttempts)
		sendTimeout = saturatingAdd(retryTransportBudget, retryBackoffBudget, 2*time.Second)
		handlerTimeout = saturatingAdd(sendTimeout, 5*time.Second)

		opts := []acs.Option{
			acs.WithRetry(cfg.ACSRetryAttempts, baseDelay),
			acs.WithHTTPTransportConfig(acs.HTTPTransportConfig{
				Timeout:             httpTimeout,
				MaxIdleConns:        cfg.ACSHTTPMaxIdleConns,
				MaxIdleConnsPerHost: cfg.ACSHTTPMaxIdleConnsPerHost,
				IdleConnTimeout:     time.Duration(cfg.ACSHTTPIdleConnTimeoutMS) * time.Millisecond,
			}),
		}
		if strings.TrimSpace(cfg.ACSTLSCAFile) != "" {
			opts = append(opts, acs.WithTLSCAFile(cfg.ACSTLSCAFile))
		}
		if strings.TrimSpace(cfg.ACSTLSCAPEM) != "" {
			opts = append(opts, acs.WithTLSCAPEM(cfg.ACSTLSCAPEM))
		}

		provider, err := acs.NewProvider(cfg.ACSEndpoint, cfg.ACSConnectionString, cfg.ACSSender, logger, opts...)
		if err != nil {
			return nil, 0, err
		}
		sendFunc = provider.Send
	default:
		return nil, 0, errors.New("unsupported delivery mode")
	}

	return smtprelay.MessageHandlerFunc(func(ctx context.Context, msg email.Message) error {
		select {
		case inflight <- struct{}{}:
			defer func() { <-inflight }()
		default:
			logger.Warn("smtp send rejected due to inflight saturation", "max_inflight", cap(inflight))
			return relayBusyError
		}

		sendCtx := ctx
		cancel := func() {}
		if sendTimeout > 0 {
			sendCtx, cancel = context.WithTimeout(ctx, sendTimeout)
		}
		defer cancel()

		if err := sendFunc(sendCtx, msg); err != nil {
			logger.Error("outbound delivery failed",
				"mode", cfg.DeliveryMode,
				"from", msg.From,
				"to_count", len(msg.To),
				"subject", msg.Subject,
				"error", err,
			)
			return temporarySendError
		}
		return nil
	}), handlerTimeout, nil
}

func loadSMTPServerTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(strings.TrimSpace(certFile), strings.TrimSpace(keyFile))
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}, nil
}

func retryBackoffTotal(base time.Duration, attempts int) time.Duration {
	if attempts <= 1 || base <= 0 {
		return 0
	}

	total := time.Duration(0)
	backoff := base
	for i := 1; i < attempts; i++ {
		total = saturatingAdd(total, backoff)
		if backoff > maxDuration/2 {
			backoff = maxDuration
		} else {
			backoff *= 2
		}
	}
	return total
}

func multiplyDuration(d time.Duration, n int) time.Duration {
	if d <= 0 || n <= 0 {
		return 0
	}
	if d > maxDuration/time.Duration(n) {
		return maxDuration
	}
	return d * time.Duration(n)
}

func saturatingAdd(values ...time.Duration) time.Duration {
	total := time.Duration(0)
	for _, v := range values {
		if v <= 0 {
			continue
		}
		if total > maxDuration-v {
			return maxDuration
		}
		total += v
	}
	return total
}
