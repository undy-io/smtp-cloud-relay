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
	"github.com/undy-io/smtp-cloud-relay/internal/providers"
	relaysvc "github.com/undy-io/smtp-cloud-relay/internal/relay"
	smtprelay "github.com/undy-io/smtp-cloud-relay/internal/smtp"
	"github.com/undy-io/smtp-cloud-relay/internal/spool"
)

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

	if err := smtpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to close smtp server", "error", err)
	}
	if err := obsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown observability server", "error", err)
	}
}

func buildMessageHandler(cfg config.Config, logger *slog.Logger) (smtprelay.MessageHandler, time.Duration, error) {
	deliveryRuntime, err := providers.Build(cfg, logger)
	if err != nil {
		return nil, 0, err
	}

	senderPolicy, err := buildSenderPolicy(cfg)
	if err != nil {
		return nil, 0, err
	}

	handlerTimeout := deliveryRuntime.HandlerTimeout
	return newDirectSendMessageHandler(cfg, logger, senderPolicy, deliveryRuntime.Provider.Send, deliveryRuntime.SendTimeout), handlerTimeout, nil
}

func buildSenderPolicy(cfg config.Config) (email.SenderPolicy, error) {
	return email.NewSenderPolicy(email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyMode(cfg.SenderPolicyMode),
		AllowedDomainPatterns: cfg.SenderAllowedDomains,
	})
}

// buildRelayHandler constructs the durable-enqueue relay service without wiring it into SMTP yet.
func buildRelayHandler(cfg config.Config, logger *slog.Logger, store spool.Store) (*relaysvc.Handler, error) {
	senderPolicy, err := buildSenderPolicy(cfg)
	if err != nil {
		return nil, err
	}

	return relaysvc.NewHandler(logger, senderPolicy, store, cfg.SMTPMaxInflightSends)
}

func newDirectSendMessageHandler(cfg config.Config, logger *slog.Logger, senderPolicy email.SenderPolicy, sendFunc func(context.Context, email.Message) error, sendTimeout time.Duration) smtprelay.MessageHandler {
	if logger == nil {
		logger = slog.Default()
	}

	inflight := make(chan struct{}, cfg.SMTPMaxInflightSends)
	relayBusyError := &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 2}, Message: "relay busy, try again later"}
	senderPolicyRejectedError := &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "sender rejected by relay policy"}

	return smtprelay.MessageHandlerFunc(func(ctx context.Context, msg email.Message) error {
		policyResult, err := email.ApplySenderPolicy(msg, senderPolicy)
		if err != nil {
			if policyErr, ok := email.AsSenderPolicyError(err); ok {
				logger.Warn("smtp sender rejected by policy",
					"sender_policy_mode", cfg.SenderPolicyMode,
					"sender_policy_reason", policyErr.Reason,
					"envelope_from", msg.EnvelopeFrom,
					"header_from", msg.HeaderFrom,
					"original_sender", policyResult.OriginalSender,
					"effective_reply_to_count", len(policyResult.EffectiveReplyTo),
				)
				return senderPolicyRejectedError
			}

			logger.Error("failed to apply sender policy",
				"sender_policy_mode", cfg.SenderPolicyMode,
				"envelope_from", msg.EnvelopeFrom,
				"header_from", msg.HeaderFrom,
				"error", err,
			)
			return smtprelay.MapDeliveryError(err)
		}

		if policyResult.DecisionReason != "" {
			logger.Info("smtp sender policy dropped original sender intent",
				"sender_policy_mode", cfg.SenderPolicyMode,
				"sender_policy_reason", policyResult.DecisionReason,
				"envelope_from", msg.EnvelopeFrom,
				"header_from", msg.HeaderFrom,
				"original_sender", policyResult.OriginalSender,
				"effective_reply_to_count", len(policyResult.EffectiveReplyTo),
			)
		}

		msg = policyResult.Message

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
			logArgs := []any{
				"mode", cfg.DeliveryMode,
				"sender_policy_mode", cfg.SenderPolicyMode,
				"envelope_from", msg.EnvelopeFrom,
				"header_from", msg.HeaderFrom,
				"original_sender", policyResult.OriginalSender,
				"effective_reply_to_count", len(msg.ReplyTo),
				"to_count", len(msg.To),
				"subject", msg.Subject,
				"error", err,
			}
			if policyResult.DecisionReason != "" {
				logArgs = append(logArgs, "sender_policy_reason", policyResult.DecisionReason)
			}
			if deliveryErr, ok := email.AsDeliveryError(err); ok {
				logArgs = append(logArgs,
					"provider", deliveryErr.ProviderName(),
					"temporary", deliveryErr.Temporary(),
					"status_code", deliveryErr.HTTPStatusCode(),
				)
			}
			logger.Error("outbound delivery failed", logArgs...)
			return smtprelay.MapDeliveryError(err)
		}
		return nil
	})
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
