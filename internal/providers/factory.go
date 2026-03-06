package providers

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/config"
	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/acs"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/httpclient"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/noop"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/ses"
)

const maxDuration = time.Duration(1<<63 - 1)

// Runtime bundles the selected provider with runtime timeout budgets.
type Runtime struct {
	Provider       email.Provider
	SendTimeout    time.Duration
	HandlerTimeout time.Duration
}

// Build selects and configures the outbound provider for the configured delivery mode.
func Build(cfg config.Config, logger *slog.Logger) (Runtime, error) {
	if logger == nil {
		logger = slog.Default()
	}

	switch strings.ToLower(strings.TrimSpace(cfg.DeliveryMode)) {
	case "noop":
		return Runtime{
			Provider: noop.NewProvider(logger),
		}, nil
	case "acs":
		baseDelay := time.Duration(cfg.DeliveryRetryBaseDelayMS) * time.Millisecond
		httpTimeout := time.Duration(cfg.DeliveryHTTPTimeoutMS) * time.Millisecond
		sendTimeout, handlerTimeout := runtimeBudgets(httpTimeout, baseDelay, cfg.DeliveryRetryAttempts)

		client, err := httpclient.Build(httpclient.Config{
			Timeout:             httpTimeout,
			MaxIdleConns:        cfg.DeliveryHTTPMaxIdleConns,
			MaxIdleConnsPerHost: cfg.DeliveryHTTPMaxIdleConnsPerHost,
			IdleConnTimeout:     time.Duration(cfg.DeliveryHTTPIdleConnTimeoutMS) * time.Millisecond,
			TLSCAFile:           cfg.OutboundTLSCAFile,
			TLSCAPEM:            cfg.OutboundTLSCAPEM,
		})
		if err != nil {
			return Runtime{}, fmt.Errorf("build acs http client: %w", err)
		}

		opts := []acs.Option{
			acs.WithRetry(cfg.DeliveryRetryAttempts, baseDelay),
			acs.WithHTTPClient(client),
		}

		p, err := acs.NewProvider(cfg.ACSEndpoint, cfg.ACSConnectionString, cfg.ACSSender, logger, opts...)
		if err != nil {
			return Runtime{}, err
		}

		return Runtime{
			Provider:       p,
			SendTimeout:    sendTimeout,
			HandlerTimeout: handlerTimeout,
		}, nil
	case "ses":
		baseDelay := time.Duration(cfg.DeliveryRetryBaseDelayMS) * time.Millisecond
		httpTimeout := time.Duration(cfg.DeliveryHTTPTimeoutMS) * time.Millisecond
		sendTimeout, handlerTimeout := runtimeBudgets(httpTimeout, baseDelay, cfg.DeliveryRetryAttempts)

		client, err := httpclient.Build(httpclient.Config{
			Timeout:             httpTimeout,
			MaxIdleConns:        cfg.DeliveryHTTPMaxIdleConns,
			MaxIdleConnsPerHost: cfg.DeliveryHTTPMaxIdleConnsPerHost,
			IdleConnTimeout:     time.Duration(cfg.DeliveryHTTPIdleConnTimeoutMS) * time.Millisecond,
			TLSCAFile:           cfg.OutboundTLSCAFile,
			TLSCAPEM:            cfg.OutboundTLSCAPEM,
		})
		if err != nil {
			return Runtime{}, fmt.Errorf("build ses http client: %w", err)
		}

		opts := []ses.Option{
			ses.WithHTTPClient(client),
			ses.WithRetry(cfg.DeliveryRetryAttempts, baseDelay),
		}
		if strings.TrimSpace(cfg.SESAccessKeyID) != "" {
			opts = append(opts, ses.WithStaticCredentials(cfg.SESAccessKeyID, cfg.SESSecretAccessKey, cfg.SESSessionToken))
		}

		p, err := ses.NewProvider(cfg.SESRegion, cfg.SESSender, cfg.SESEndpoint, cfg.SESConfigurationSet, logger, opts...)
		if err != nil {
			return Runtime{}, err
		}

		return Runtime{
			Provider:       p,
			SendTimeout:    sendTimeout,
			HandlerTimeout: handlerTimeout,
		}, nil
	default:
		return Runtime{}, fmt.Errorf("unsupported delivery mode %q", cfg.DeliveryMode)
	}
}

func runtimeBudgets(httpTimeout, baseDelay time.Duration, attempts int) (time.Duration, time.Duration) {
	retryTransportBudget := multiplyDuration(httpTimeout, attempts)
	retryBackoffBudget := retryBackoffTotal(baseDelay, attempts)
	sendTimeout := saturatingAdd(retryTransportBudget, retryBackoffBudget, 2*time.Second)
	handlerTimeout := saturatingAdd(sendTimeout, 5*time.Second)
	return sendTimeout, handlerTimeout
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
