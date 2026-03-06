package acs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestNewProviderValid(t *testing.T) {
	connectionString := fmt.Sprintf(
		"endpoint=https://example.communication.azure.us/;accesskey=%s",
		base64.StdEncoding.EncodeToString([]byte("test-access-key")),
	)

	p, err := NewProvider("", connectionString, "no-reply@example.com", testLogger())
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}

	if p.endpoint.String() != "https://example.communication.azure.us" {
		t.Fatalf("unexpected endpoint: %q", p.endpoint.String())
	}
	if p.retryAttempts != defaultRetryAttempts {
		t.Fatalf("unexpected retryAttempts: %d", p.retryAttempts)
	}
	if p.retryBaseDelay != defaultRetryBaseDelay {
		t.Fatalf("unexpected retryBaseDelay: %s", p.retryBaseDelay)
	}
}

func TestNewProviderInvalid(t *testing.T) {
	validConn := fmt.Sprintf(
		"endpoint=https://example.communication.azure.us/;accesskey=%s",
		base64.StdEncoding.EncodeToString([]byte("test-access-key")),
	)

	tests := []struct {
		name             string
		endpoint         string
		connectionString string
		sender           string
		substr           string
	}{
		{
			name:             "invalid endpoint",
			endpoint:         "://bad",
			connectionString: validConn,
			sender:           "no-reply@example.com",
			substr:           "parse acs endpoint",
		},
		{
			name:             "non-https endpoint",
			endpoint:         "http://example.communication.azure.us",
			connectionString: validConn,
			sender:           "no-reply@example.com",
			substr:           "must use https",
		},
		{
			name:             "missing access key",
			endpoint:         "",
			connectionString: "endpoint=https://example.communication.azure.us/",
			sender:           "no-reply@example.com",
			substr:           "missing accesskey",
		},
		{
			name:             "missing sender",
			endpoint:         "",
			connectionString: validConn,
			sender:           " ",
			substr:           "sender cannot be empty",
		},
		{
			name:             "invalid option",
			endpoint:         "",
			connectionString: validConn,
			sender:           "no-reply@example.com",
			substr:           "retry attempts must be >= 1",
		},
		{
			name:             "retry option too high",
			endpoint:         "",
			connectionString: validConn,
			sender:           "no-reply@example.com",
			substr:           "retry attempts must be <=",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opts []Option
			if tc.name == "invalid option" {
				opts = append(opts, WithRetry(0, time.Second))
			}
			if tc.name == "retry option too high" {
				opts = append(opts, WithRetry(11, time.Second))
			}

			_, err := NewProvider(tc.endpoint, tc.connectionString, tc.sender, testLogger(), opts...)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Fatalf("expected error to contain %q, got %q", tc.substr, err.Error())
			}
		})
	}
}

func TestSendMapsPayload(t *testing.T) {
	var captured sendRequest
	var method, path, apiVersion, requestID string

	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithRetry(1, time.Millisecond))
	p.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			method = req.Method
			path = req.URL.Path
			apiVersion = req.URL.Query().Get("api-version")
			requestID = req.Header.Get("x-ms-client-request-id")

			if req.Header.Get("Authorization") == "" {
				t.Errorf("Authorization header is missing")
			}
			if req.Header.Get("x-ms-date") == "" {
				t.Errorf("x-ms-date header is missing")
			}
			if req.Header.Get("x-ms-content-sha256") == "" {
				t.Errorf("x-ms-content-sha256 header is missing")
			}

			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if err := json.Unmarshal(bodyBytes, &captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}

			return newResponse(req, http.StatusAccepted, ""), nil
		}),
	}

	msg := email.Message{
		From:     "ignored@example.com",
		To:       []string{"one@example.com", " ", "two@example.com"},
		Subject:  "Test Subject",
		TextBody: "Text body",
		HTMLBody: "<p>HTML body</p>",
		Attachments: []email.Attachment{
			{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello note")},
			{Data: []byte("hello default")},
		},
	}

	if err := p.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if method != http.MethodPost {
		t.Fatalf("unexpected method: %q", method)
	}
	if path != emailSendPath {
		t.Fatalf("unexpected path: %q", path)
	}
	if apiVersion != emailSendAPIVersion {
		t.Fatalf("unexpected api-version: %q", apiVersion)
	}
	if requestID == "" {
		t.Fatalf("expected non-empty x-ms-client-request-id")
	}

	if captured.SenderAddress != "no-reply@example.com" {
		t.Fatalf("unexpected senderAddress: %q", captured.SenderAddress)
	}
	if len(captured.Recipients.To) != 2 {
		t.Fatalf("unexpected recipient count: %d", len(captured.Recipients.To))
	}
	if captured.Recipients.To[0].Address != "one@example.com" || captured.Recipients.To[1].Address != "two@example.com" {
		t.Fatalf("unexpected recipients: %#v", captured.Recipients.To)
	}
	if captured.Content.Subject != "Test Subject" {
		t.Fatalf("unexpected subject: %q", captured.Content.Subject)
	}
	if captured.Content.PlainText != "Text body" {
		t.Fatalf("unexpected plainText: %q", captured.Content.PlainText)
	}
	if captured.Content.HTML != "<p>HTML body</p>" {
		t.Fatalf("unexpected html: %q", captured.Content.HTML)
	}

	if len(captured.Attachments) != 2 {
		t.Fatalf("unexpected attachment count: %d", len(captured.Attachments))
	}
	if captured.Attachments[0].Name != "note.txt" {
		t.Fatalf("unexpected attachment[0] name: %q", captured.Attachments[0].Name)
	}
	if captured.Attachments[0].ContentType != "text/plain" {
		t.Fatalf("unexpected attachment[0] contentType: %q", captured.Attachments[0].ContentType)
	}
	if captured.Attachments[1].Name != "attachment-2" {
		t.Fatalf("unexpected attachment[1] name: %q", captured.Attachments[1].Name)
	}
	if captured.Attachments[1].ContentType != "application/octet-stream" {
		t.Fatalf("unexpected attachment[1] contentType: %q", captured.Attachments[1].ContentType)
	}

	a0, err := base64.StdEncoding.DecodeString(captured.Attachments[0].ContentInBase64)
	if err != nil {
		t.Fatalf("decode attachment[0]: %v", err)
	}
	if string(a0) != "hello note" {
		t.Fatalf("unexpected attachment[0] data: %q", string(a0))
	}
	a1, err := base64.StdEncoding.DecodeString(captured.Attachments[1].ContentInBase64)
	if err != nil {
		t.Fatalf("decode attachment[1]: %v", err)
	}
	if string(a1) != "hello default" {
		t.Fatalf("unexpected attachment[1] data: %q", string(a1))
	}
}

func TestSendContentVariants(t *testing.T) {
	tests := []struct {
		name      string
		msg       email.Message
		wantPlain string
		wantHTML  string
	}{
		{
			name:      "plain text only",
			msg:       email.Message{To: []string{"to@example.com"}, Subject: "plain", TextBody: "plain body"},
			wantPlain: "plain body",
			wantHTML:  "",
		},
		{
			name:      "html only",
			msg:       email.Message{To: []string{"to@example.com"}, Subject: "html", HTMLBody: "<strong>html</strong>"},
			wantPlain: "",
			wantHTML:  "<strong>html</strong>",
		},
		{
			name:      "empty content fallback",
			msg:       email.Message{To: []string{"to@example.com"}, Subject: "empty"},
			wantPlain: "(empty message)",
			wantHTML:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured sendRequest
			p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithRetry(1, time.Millisecond))
			p.httpClient = &http.Client{
				Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					bodyBytes, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatalf("read request body: %v", err)
					}
					if err := json.Unmarshal(bodyBytes, &captured); err != nil {
						t.Fatalf("decode request body: %v", err)
					}
					return newResponse(req, http.StatusAccepted, ""), nil
				}),
			}

			if err := p.Send(context.Background(), tc.msg); err != nil {
				t.Fatalf("Send() error = %v", err)
			}

			if captured.Content.PlainText != tc.wantPlain {
				t.Fatalf("unexpected plainText: got %q want %q", captured.Content.PlainText, tc.wantPlain)
			}
			if captured.Content.HTML != tc.wantHTML {
				t.Fatalf("unexpected html: got %q want %q", captured.Content.HTML, tc.wantHTML)
			}
		})
	}
}

func TestSendRetriesOn500(t *testing.T) {
	callCount := 0
	requestIDs := make([]string, 0, 3)

	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithRetry(3, time.Millisecond))
	p.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			requestIDs = append(requestIDs, req.Header.Get("x-ms-client-request-id"))
			if callCount < 3 {
				return newResponse(req, http.StatusInternalServerError, "temporary failure"), nil
			}
			return newResponse(req, http.StatusAccepted, ""), nil
		}),
	}

	err := p.Send(context.Background(), email.Message{To: []string{"to@example.com"}, Subject: "retry"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if callCount != 3 {
		t.Fatalf("expected 3 attempts, got %d", callCount)
	}
	if len(requestIDs) != 3 {
		t.Fatalf("expected 3 request IDs, got %d", len(requestIDs))
	}
	if requestIDs[0] == "" {
		t.Fatalf("request id should not be empty")
	}
	if requestIDs[0] != requestIDs[1] || requestIDs[1] != requestIDs[2] {
		t.Fatalf("expected stable request id across retries, got %#v", requestIDs)
	}
}

func TestSendRetriesOn429(t *testing.T) {
	callCount := 0

	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithRetry(3, time.Millisecond))
	p.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return newResponse(req, http.StatusTooManyRequests, "too many requests"), nil
			}
			return newResponse(req, http.StatusAccepted, ""), nil
		}),
	}

	err := p.Send(context.Background(), email.Message{To: []string{"to@example.com"}, Subject: "retry429"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 attempts, got %d", callCount)
	}
}

func TestSendDoesNotRetryOn400AndReturnsTypedError(t *testing.T) {
	callCount := 0

	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithRetry(5, time.Millisecond))
	p.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			return newResponse(req, http.StatusBadRequest, "bad request details"), nil
		}),
	}

	err := p.Send(context.Background(), email.Message{To: []string{"to@example.com"}, Subject: "noretry"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var sendErr *SendError
	if !errors.As(err, &sendErr) {
		t.Fatalf("expected *SendError, got %T", err)
	}
	var deliveryErr email.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("expected email.DeliveryError, got %T", err)
	}
	if deliveryErr.ProviderName() != "acs" {
		t.Fatalf("unexpected provider name: %q", deliveryErr.ProviderName())
	}
	if deliveryErr.Temporary() {
		t.Fatalf("expected temporary=false for 400")
	}
	if deliveryErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("unexpected delivery status code: %d", deliveryErr.HTTPStatusCode())
	}
	if sendErr.RequestID == "" {
		t.Fatalf("expected request id")
	}
	if sendErr.Attempt != 1 || sendErr.Attempts != 5 {
		t.Fatalf("unexpected attempt metadata: %d/%d", sendErr.Attempt, sendErr.Attempts)
	}
	if sendErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected status code: %d", sendErr.StatusCode)
	}
	if sendErr.Retryable {
		t.Fatalf("expected retryable=false for 400")
	}
	if strings.Contains(sendErr.Error(), "bad request details") {
		t.Fatalf("error must not leak response body: %q", sendErr.Error())
	}
	if callCount != 1 {
		t.Fatalf("expected 1 attempt, got %d", callCount)
	}
}

func TestSendRetriesTransportErrors(t *testing.T) {
	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithRetry(3, time.Millisecond))

	callCount := 0
	p.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount < 3 {
				return nil, errors.New("dial tcp: temporary failure")
			}
			return newResponse(req, http.StatusAccepted, ""), nil
		}),
	}

	err := p.Send(context.Background(), email.Message{To: []string{"to@example.com"}, Subject: "transport retry"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if callCount != 3 {
		t.Fatalf("expected 3 attempts, got %d", callCount)
	}
}

func TestNewProviderWithTLSCAPEM(t *testing.T) {
	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithTLSCAPEM(validTestCertPEM(t)))

	transport, ok := p.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected *http.Transport, got %T", p.httpClient.Transport)
	}
	if transport.Proxy == nil {
		t.Fatalf("expected proxy func to be set")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatalf("expected RootCAs to be configured")
	}
}

func TestNewProviderWithTLSCAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte(validTestCertPEM(t)), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithTLSCAFile(path))
	transport, ok := p.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatalf("expected tls transport with RootCAs")
	}
}

func TestNewProviderWithTLSCAFileAndPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte(validTestCertPEM(t)), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_ = newProviderForEndpoint(
		t,
		"https://example.communication.azure.us",
		WithTLSCAFile(path),
		WithTLSCAPEM(validTestCertPEM(t)),
	)
}

func TestNewProviderWithInvalidTLSCAInputs(t *testing.T) {
	t.Run("invalid pem", func(t *testing.T) {
		_, err := NewProvider(
			"",
			fmt.Sprintf("endpoint=%s;accesskey=%s", "https://example.communication.azure.us", base64.StdEncoding.EncodeToString([]byte("test-access-key"))),
			"no-reply@example.com",
			testLogger(),
			WithTLSCAPEM("not a cert"),
		)
		if err == nil || !strings.Contains(err.Error(), "does not contain valid certificates") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := NewProvider(
			"",
			fmt.Sprintf("endpoint=%s;accesskey=%s", "https://example.communication.azure.us", base64.StdEncoding.EncodeToString([]byte("test-access-key"))),
			"no-reply@example.com",
			testLogger(),
			WithTLSCAFile("/no/such/path.pem"),
		)
		if err == nil || !strings.Contains(err.Error(), "read tls ca file") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestNewProviderHTTPClientAndCAConflict(t *testing.T) {
	_, err := NewProvider(
		"",
		fmt.Sprintf("endpoint=%s;accesskey=%s", "https://example.communication.azure.us", base64.StdEncoding.EncodeToString([]byte("test-access-key"))),
		"no-reply@example.com",
		testLogger(),
		WithHTTPClient(&http.Client{Timeout: time.Second}),
		WithTLSCAPEM(validTestCertPEM(t)),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot combine WithHTTPClient with TLS CA options") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewProviderHTTPClientAndTransportConfigConflict(t *testing.T) {
	_, err := NewProvider(
		"",
		fmt.Sprintf("endpoint=%s;accesskey=%s", "https://example.communication.azure.us", base64.StdEncoding.EncodeToString([]byte("test-access-key"))),
		"no-reply@example.com",
		testLogger(),
		WithHTTPClient(&http.Client{Timeout: time.Second}),
		WithHTTPTransportConfig(HTTPTransportConfig{
			Timeout:             2 * time.Second,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     30 * time.Second,
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot combine WithHTTPClient with WithHTTPTransportConfig") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWithHTTPTransportConfigApplied(t *testing.T) {
	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithHTTPTransportConfig(HTTPTransportConfig{
		Timeout:             12 * time.Second,
		MaxIdleConns:        300,
		MaxIdleConnsPerHost: 40,
		IdleConnTimeout:     45 * time.Second,
	}))

	if p.httpClient.Timeout != 12*time.Second {
		t.Fatalf("unexpected client timeout: %s", p.httpClient.Timeout)
	}

	transport, ok := p.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected *http.Transport, got %T", p.httpClient.Transport)
	}
	if transport.MaxIdleConns != 300 {
		t.Fatalf("unexpected MaxIdleConns: %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 40 {
		t.Fatalf("unexpected MaxIdleConnsPerHost: %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 45*time.Second {
		t.Fatalf("unexpected IdleConnTimeout: %s", transport.IdleConnTimeout)
	}
}

func TestWithHTTPTransportConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  HTTPTransportConfig
	}{
		{
			name: "timeout must be positive",
			cfg: HTTPTransportConfig{
				Timeout:             0,
				MaxIdleConns:        1,
				MaxIdleConnsPerHost: 1,
				IdleConnTimeout:     time.Second,
			},
		},
		{
			name: "max idle conns must be positive",
			cfg: HTTPTransportConfig{
				Timeout:             time.Second,
				MaxIdleConns:        0,
				MaxIdleConnsPerHost: 1,
				IdleConnTimeout:     time.Second,
			},
		},
		{
			name: "max idle conns per host must be positive",
			cfg: HTTPTransportConfig{
				Timeout:             time.Second,
				MaxIdleConns:        1,
				MaxIdleConnsPerHost: 0,
				IdleConnTimeout:     time.Second,
			},
		},
		{
			name: "idle timeout must be positive",
			cfg: HTTPTransportConfig{
				Timeout:             time.Second,
				MaxIdleConns:        1,
				MaxIdleConnsPerHost: 1,
				IdleConnTimeout:     0,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProvider(
				"",
				fmt.Sprintf("endpoint=%s;accesskey=%s", "https://example.communication.azure.us", base64.StdEncoding.EncodeToString([]byte("test-access-key"))),
				"no-reply@example.com",
				testLogger(),
				WithHTTPTransportConfig(tc.cfg),
			)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestTLSTransportUsesProxyFromEnvironment(t *testing.T) {
	p := newProviderForEndpoint(t, "https://example.communication.azure.us", WithTLSCAPEM(validTestCertPEM(t)))
	transport, ok := p.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected *http.Transport, got %T", p.httpClient.Transport)
	}

	t.Setenv("HTTPS_PROXY", "http://proxy.example.local:8443")
	t.Setenv("NO_PROXY", "")

	req, err := http.NewRequest(http.MethodGet, "https://example.communication.azure.us", nil)
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("proxy resolution error: %v", err)
	}
	if proxyURL == nil {
		t.Fatalf("expected proxy URL from environment")
	}
	if proxyURL.String() != "http://proxy.example.local:8443" {
		t.Fatalf("unexpected proxy URL: %q", proxyURL.String())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newProviderForEndpoint(t *testing.T, endpoint string, opts ...Option) *Provider {
	t.Helper()
	connectionString := fmt.Sprintf(
		"endpoint=%s;accesskey=%s",
		endpoint,
		base64.StdEncoding.EncodeToString([]byte("test-access-key")),
	)

	p, err := NewProvider("", connectionString, "no-reply@example.com", testLogger(), opts...)
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	return p
}

func newResponse(req *http.Request, statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validTestCertPEM(t *testing.T) string {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate() error: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if len(pemBytes) == 0 {
		t.Fatal("failed to encode PEM")
	}
	return string(pemBytes)
}
