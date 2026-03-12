package providers

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/config"
	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/acs"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/ses"
)

func TestBuildNoop(t *testing.T) {
	rt, err := Build(config.Config{
		DeliveryMode: "noop",
	}, testLogger())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if rt.Provider == nil {
		t.Fatal("expected provider")
	}
	if rt.SendTimeout != 0 {
		t.Fatalf("unexpected send timeout: %s", rt.SendTimeout)
	}
	if rt.HandlerTimeout != 0 {
		t.Fatalf("unexpected handler timeout: %s", rt.HandlerTimeout)
	}
	if err := rt.Provider.Send(context.Background(), email.Message{To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	result, err := rt.Provider.Submit(context.Background(), email.Message{To: []string{"to@example.com"}}, "noop-op")
	if err != nil {
		t.Fatalf("Submit() error: %v", err)
	}
	if result.State != email.SubmissionStateSucceeded {
		t.Fatalf("unexpected submit state: %q", result.State)
	}
	status, err := rt.Provider.Poll(context.Background(), "noop-op")
	if err != nil {
		t.Fatalf("Poll() error: %v", err)
	}
	if status.State != email.SubmissionStateSucceeded {
		t.Fatalf("unexpected poll state: %q", status.State)
	}
}

func TestBuildACS(t *testing.T) {
	accessKey := base64.StdEncoding.EncodeToString([]byte("test-access-key"))
	rt, err := Build(config.Config{
		DeliveryMode:                    "acs",
		ACSConnectionString:             "endpoint=https://example.communication.azure.us;accesskey=" + accessKey,
		ACSSender:                       "no-reply@example.com",
		DeliveryRetryAttempts:           3,
		DeliveryRetryBaseDelayMS:        1000,
		DeliveryHTTPTimeoutMS:           30000,
		DeliveryHTTPMaxIdleConns:        200,
		DeliveryHTTPMaxIdleConnsPerHost: 50,
		DeliveryHTTPIdleConnTimeoutMS:   90000,
	}, testLogger())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if rt.Provider == nil {
		t.Fatal("expected provider")
	}
	if _, ok := rt.Provider.(*acs.Provider); !ok {
		t.Fatalf("unexpected provider type: %T", rt.Provider)
	}
	if rt.SendTimeout <= 0 {
		t.Fatalf("expected send timeout > 0, got %s", rt.SendTimeout)
	}
	if rt.HandlerTimeout <= rt.SendTimeout {
		t.Fatalf("expected handler timeout > send timeout, got send=%s handler=%s", rt.SendTimeout, rt.HandlerTimeout)
	}
}

func TestBuildSES(t *testing.T) {
	rt, err := Build(config.Config{
		DeliveryMode:                    "ses",
		SESRegion:                       "us-gov-west-1",
		SESSender:                       "no-reply@example.com",
		SESEndpoint:                     "https://email.us-gov-west-1.amazonaws.com",
		SESConfigurationSet:             "relay-config",
		SESAccessKeyID:                  "AKIA_TEST",
		SESSecretAccessKey:              "secret",
		SESSessionToken:                 "token",
		DeliveryRetryAttempts:           3,
		DeliveryRetryBaseDelayMS:        1000,
		DeliveryHTTPTimeoutMS:           30000,
		DeliveryHTTPMaxIdleConns:        200,
		DeliveryHTTPMaxIdleConnsPerHost: 50,
		DeliveryHTTPIdleConnTimeoutMS:   90000,
	}, testLogger())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if rt.Provider == nil {
		t.Fatal("expected provider")
	}
	if _, ok := rt.Provider.(*ses.Provider); !ok {
		t.Fatalf("unexpected provider type: %T", rt.Provider)
	}
	if rt.SendTimeout <= 0 {
		t.Fatalf("expected send timeout > 0, got %s", rt.SendTimeout)
	}
	if rt.HandlerTimeout <= rt.SendTimeout {
		t.Fatalf("expected handler timeout > send timeout, got send=%s handler=%s", rt.SendTimeout, rt.HandlerTimeout)
	}
}

func TestBuildUsesOutboundTLSConfig(t *testing.T) {
	accessKey := base64.StdEncoding.EncodeToString([]byte("test-access-key"))

	tests := []struct {
		name string
		cfg  config.Config
	}{
		{
			name: "acs",
			cfg: config.Config{
				DeliveryMode:                    "acs",
				ACSConnectionString:             "endpoint=https://example.communication.azure.us;accesskey=" + accessKey,
				ACSSender:                       "no-reply@example.com",
				DeliveryRetryAttempts:           3,
				DeliveryRetryBaseDelayMS:        1000,
				DeliveryHTTPTimeoutMS:           30000,
				DeliveryHTTPMaxIdleConns:        200,
				DeliveryHTTPMaxIdleConnsPerHost: 50,
				DeliveryHTTPIdleConnTimeoutMS:   90000,
				OutboundTLSCAPEM:                "not a cert",
			},
		},
		{
			name: "ses",
			cfg: config.Config{
				DeliveryMode:                    "ses",
				SESRegion:                       "us-gov-west-1",
				SESSender:                       "no-reply@example.com",
				SESEndpoint:                     "https://email.us-gov-west-1.amazonaws.com",
				DeliveryRetryAttempts:           3,
				DeliveryRetryBaseDelayMS:        1000,
				DeliveryHTTPTimeoutMS:           30000,
				DeliveryHTTPMaxIdleConns:        200,
				DeliveryHTTPMaxIdleConnsPerHost: 50,
				DeliveryHTTPIdleConnTimeoutMS:   90000,
				OutboundTLSCAPEM:                "not a cert",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.cfg, testLogger())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "tls ca pem does not contain valid certificates") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestBuildUnknownMode(t *testing.T) {
	_, err := Build(config.Config{
		DeliveryMode: "unknown",
	}, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported delivery mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRetryBackoffTotal(t *testing.T) {
	got := retryBackoffTotal(time.Second, 3)
	if got != 3*time.Second {
		t.Fatalf("unexpected total backoff: %s", got)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
