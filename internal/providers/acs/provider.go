package acs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/httpclient"
)

const (
	emailSendPath               = "/emails:send"
	emailSendAPIVersion         = "2023-03-31"
	defaultRetryAttempts        = 3
	maxRetryAttempts            = 10
	defaultRetryBaseDelay       = 1 * time.Second
	maxResponseBodyBytes  int64 = 8 << 10
	maxDuration                 = time.Duration(1<<63 - 1)
)

type Provider struct {
	endpoint           *url.URL
	accessKey          []byte
	sender             string
	httpClient         *http.Client
	hasHTTPClient      bool
	logger             *slog.Logger
	retryAttempts      int
	retryBaseDelay     time.Duration
	extraCAPEMs        [][]byte
	transportConfig    HTTPTransportConfig
	hasTransportConfig bool
}

// Option mutates Provider behavior during initialization.
type Option func(*Provider) error

// HTTPTransportConfig controls outbound ACS HTTP transport behavior.
type HTTPTransportConfig struct {
	Timeout             time.Duration
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

// WithRetry configures send retry behavior.
func WithRetry(attempts int, baseDelay time.Duration) Option {
	return func(p *Provider) error {
		if attempts < 1 {
			return fmt.Errorf("retry attempts must be >= 1")
		}
		if attempts > maxRetryAttempts {
			return fmt.Errorf("retry attempts must be <= %d", maxRetryAttempts)
		}
		if baseDelay <= 0 {
			return fmt.Errorf("retry base delay must be > 0")
		}
		p.retryAttempts = attempts
		p.retryBaseDelay = baseDelay
		return nil
	}
}

// WithHTTPClient injects a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) error {
		if client == nil {
			return fmt.Errorf("http client cannot be nil")
		}
		p.httpClient = client
		p.hasHTTPClient = true
		return nil
	}
}

// WithHTTPTransportConfig configures the provider-managed HTTP transport.
func WithHTTPTransportConfig(cfg HTTPTransportConfig) Option {
	return func(p *Provider) error {
		if cfg.Timeout <= 0 {
			return fmt.Errorf("http timeout must be > 0")
		}
		if cfg.MaxIdleConns <= 0 {
			return fmt.Errorf("http max idle conns must be > 0")
		}
		if cfg.MaxIdleConnsPerHost <= 0 {
			return fmt.Errorf("http max idle conns per host must be > 0")
		}
		if cfg.IdleConnTimeout <= 0 {
			return fmt.Errorf("http idle conn timeout must be > 0")
		}
		p.transportConfig = cfg
		p.hasTransportConfig = true
		return nil
	}
}

// WithTLSCAFile loads additional trusted CA PEM certificates from a file path.
func WithTLSCAFile(path string) Option {
	return func(p *Provider) error {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("tls ca file path cannot be empty")
		}

		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read tls ca file %q: %w", path, err)
		}
		return appendExtraCAPEM(p, b)
	}
}

// WithTLSCAPEM adds additional trusted CA PEM certificates from inline content.
func WithTLSCAPEM(pem string) Option {
	return func(p *Provider) error {
		pem = strings.TrimSpace(pem)
		if pem == "" {
			return fmt.Errorf("tls ca pem cannot be empty")
		}
		return appendExtraCAPEM(p, []byte(pem))
	}
}

type sendRequest struct {
	SenderAddress string          `json:"senderAddress"`
	Recipients    recipients      `json:"recipients"`
	Content       content         `json:"content"`
	Attachments   []attachmentDTO `json:"attachments,omitempty"`
}

type recipients struct {
	To []recipient `json:"to"`
}

type recipient struct {
	Address string `json:"address"`
}

type content struct {
	Subject   string `json:"subject"`
	PlainText string `json:"plainText,omitempty"`
	HTML      string `json:"html,omitempty"`
}

type attachmentDTO struct {
	Name            string `json:"name"`
	ContentType     string `json:"contentType"`
	ContentInBase64 string `json:"contentInBase64"`
}

// SendError contains detailed provider failure information.
type SendError struct {
	RequestID  string
	Attempt    int
	Attempts   int
	StatusCode int
	Retryable  bool
	Err        error
}

var _ email.DeliveryError = (*SendError)(nil)

func (e *SendError) ProviderName() string { return "acs" }

func (e *SendError) Temporary() bool { return e.Retryable }

func (e *SendError) HTTPStatusCode() int { return e.StatusCode }

func (e *SendError) Error() string {
	prefix := fmt.Sprintf("acs send failed request_id=%s attempt=%d/%d retryable=%t", e.RequestID, e.Attempt, e.Attempts, e.Retryable)
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s status=%d", prefix, e.StatusCode)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s err=%v", prefix, e.Err)
	}
	return prefix
}

func (e *SendError) Unwrap() error { return e.Err }

func NewProvider(endpoint, connectionString, sender string, logger *slog.Logger, opts ...Option) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	parsedEndpoint, accessKey, err := parseConnection(endpoint, connectionString)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(sender) == "" {
		return nil, fmt.Errorf("acs sender cannot be empty")
	}

	provider := &Provider{
		endpoint:       parsedEndpoint,
		accessKey:      accessKey,
		sender:         strings.TrimSpace(sender),
		logger:         logger,
		retryAttempts:  defaultRetryAttempts,
		retryBaseDelay: defaultRetryBaseDelay,
		transportConfig: HTTPTransportConfig{
			Timeout:             30 * time.Second,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(provider); err != nil {
			return nil, fmt.Errorf("apply provider option: %w", err)
		}
	}

	if provider.hasHTTPClient && len(provider.extraCAPEMs) > 0 {
		return nil, fmt.Errorf("cannot combine WithHTTPClient with TLS CA options")
	}
	if provider.hasHTTPClient && provider.hasTransportConfig {
		return nil, fmt.Errorf("cannot combine WithHTTPClient with WithHTTPTransportConfig")
	}

	if !provider.hasHTTPClient {
		client, err := provider.buildHTTPClient()
		if err != nil {
			return nil, err
		}
		provider.httpClient = client
	}

	return provider, nil
}

func (p *Provider) Send(ctx context.Context, msg email.Message) error {
	body, err := buildSendRequest(p.sender, msg)
	if err != nil {
		return err
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal acs request: %w", err)
	}

	requestID, err := newRequestID()
	if err != nil {
		return fmt.Errorf("generate request id: %w", err)
	}

	var lastErr *SendError
	for attempt := 1; attempt <= p.retryAttempts; attempt++ {
		attemptErr := p.sendOnce(ctx, requestID, attempt, bodyBytes)
		if attemptErr == nil {
			return nil
		}

		lastErr = attemptErr
		if !attemptErr.Retryable || attempt == p.retryAttempts {
			return attemptErr
		}

		backoff := retryBackoff(p.retryBaseDelay, attempt)
		p.logger.Warn(
			"acs send failed, retrying",
			"attempt",
			attempt,
			"max_attempts",
			p.retryAttempts,
			"request_id",
			attemptErr.RequestID,
			"status_code",
			attemptErr.StatusCode,
			"retryable",
			attemptErr.Retryable,
			"backoff",
			backoff.String(),
			"error",
			attemptErr,
		)

		select {
		case <-ctx.Done():
			return &SendError{
				RequestID: requestID,
				Attempt:   attempt,
				Attempts:  p.retryAttempts,
				Retryable: false,
				Err:       ctx.Err(),
			}
		case <-time.After(backoff):
		}
	}

	return lastErr
}

func retryBackoff(base time.Duration, attempt int) time.Duration {
	if attempt <= 1 || base <= 0 {
		return base
	}

	backoff := base
	for i := 1; i < attempt; i++ {
		if backoff > maxDuration/2 {
			return maxDuration
		}
		backoff *= 2
	}
	return backoff
}

func buildSendRequest(sender string, msg email.Message) (sendRequest, error) {
	if len(msg.To) == 0 {
		return sendRequest{}, fmt.Errorf("message has no recipients")
	}

	toRecipients := make([]recipient, 0, len(msg.To))
	for _, to := range msg.To {
		trimmed := strings.TrimSpace(to)
		if trimmed == "" {
			continue
		}
		toRecipients = append(toRecipients, recipient{Address: trimmed})
	}
	if len(toRecipients) == 0 {
		return sendRequest{}, fmt.Errorf("message has no valid recipients")
	}

	body := sendRequest{
		SenderAddress: sender,
		Recipients: recipients{
			To: toRecipients,
		},
		Content: content{
			Subject:   msg.Subject,
			PlainText: msg.TextBody,
			HTML:      msg.HTMLBody,
		},
	}

	if body.Content.PlainText == "" && body.Content.HTML == "" {
		body.Content.PlainText = "(empty message)"
	}

	if len(msg.Attachments) > 0 {
		body.Attachments = make([]attachmentDTO, 0, len(msg.Attachments))
		for i, a := range msg.Attachments {
			name := strings.TrimSpace(a.Filename)
			if name == "" {
				name = fmt.Sprintf("attachment-%d", i+1)
			}
			contentType := strings.TrimSpace(a.ContentType)
			if contentType == "" {
				contentType = "application/octet-stream"
			}

			body.Attachments = append(body.Attachments, attachmentDTO{
				Name:            name,
				ContentType:     contentType,
				ContentInBase64: base64.StdEncoding.EncodeToString(a.Data),
			})
		}
	}

	return body, nil
}

func (p *Provider) sendOnce(ctx context.Context, requestID string, attempt int, body []byte) *SendError {
	target := *p.endpoint
	target.Path = strings.TrimRight(target.Path, "/") + emailSendPath

	q := target.Query()
	q.Set("api-version", emailSendAPIVersion)
	target.RawQuery = q.Encode()

	hash := sha256.Sum256(body)
	contentHash := base64.StdEncoding.EncodeToString(hash[:])
	xmsDate := time.Now().UTC().Format(http.TimeFormat)

	signature, err := p.buildSignature(http.MethodPost, target.RequestURI(), xmsDate, target.Host, contentHash)
	if err != nil {
		return &SendError{
			RequestID: requestID,
			Attempt:   attempt,
			Attempts:  p.retryAttempts,
			Retryable: false,
			Err:       fmt.Errorf("build authorization header: %w", err),
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return &SendError{
			RequestID: requestID,
			Attempt:   attempt,
			Attempts:  p.retryAttempts,
			Retryable: false,
			Err:       fmt.Errorf("build request: %w", err),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ms-date", xmsDate)
	req.Header.Set("x-ms-content-sha256", contentHash)
	req.Header.Set("Authorization", signature)
	req.Header.Set("x-ms-client-request-id", requestID)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return &SendError{
			RequestID: requestID,
			Attempt:   attempt,
			Attempts:  p.retryAttempts,
			Retryable: true,
			Err:       err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))
		return &SendError{
			RequestID:  requestID,
			Attempt:    attempt,
			Attempts:   p.retryAttempts,
			StatusCode: resp.StatusCode,
			Retryable:  isRetryableStatus(resp.StatusCode),
		}
	}

	operationLocation := resp.Header.Get("operation-location")
	p.logger.Info("email accepted by acs", "request_id", requestID, "operation_location", operationLocation)
	return nil
}

func (p *Provider) buildSignature(method, pathAndQuery, xmsDate, host, contentHash string) (string, error) {
	stringToSign := strings.Join([]string{
		method,
		pathAndQuery,
		xmsDate + ";" + host + ";" + contentHash,
	}, "\n")

	h := hmac.New(sha256.New, p.accessKey)
	if _, err := h.Write([]byte(stringToSign)); err != nil {
		return "", fmt.Errorf("compute hmac: %w", err)
	}

	sig := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return "HMAC-SHA256 SignedHeaders=x-ms-date;host;x-ms-content-sha256&Signature=" + sig, nil
}

func parseConnection(endpoint, connectionString string) (*url.URL, []byte, error) {
	parts, err := parseConnectionString(connectionString)
	if err != nil {
		return nil, nil, err
	}

	resolvedEndpoint := strings.TrimSpace(endpoint)
	if resolvedEndpoint == "" {
		resolvedEndpoint = parts["endpoint"]
	}
	if resolvedEndpoint == "" {
		return nil, nil, fmt.Errorf("acs endpoint is required (ACS_ENDPOINT or endpoint= in ACS_CONNECTION_STRING)")
	}

	u, err := url.Parse(strings.TrimRight(resolvedEndpoint, "/"))
	if err != nil {
		return nil, nil, fmt.Errorf("parse acs endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, nil, fmt.Errorf("invalid acs endpoint %q", resolvedEndpoint)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, nil, fmt.Errorf("acs endpoint must use https scheme")
	}

	accessKeyStr := parts["accesskey"]
	if accessKeyStr == "" {
		return nil, nil, fmt.Errorf("ACS_CONNECTION_STRING missing accesskey")
	}

	accessKey, err := base64.StdEncoding.DecodeString(accessKeyStr)
	if err != nil {
		return nil, nil, fmt.Errorf("decode accesskey from ACS_CONNECTION_STRING: %w", err)
	}

	return u, accessKey, nil
}

func parseConnectionString(raw string) (map[string]string, error) {
	result := make(map[string]string)
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid ACS_CONNECTION_STRING part %q", part)
		}

		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		if key == "" || val == "" {
			return nil, fmt.Errorf("invalid ACS_CONNECTION_STRING part %q", part)
		}
		result[key] = val
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("ACS_CONNECTION_STRING is empty")
	}
	return result, nil
}

func isRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func newRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func appendExtraCAPEM(p *Provider, pemBytes []byte) error {
	pemBytes = bytes.TrimSpace(pemBytes)
	if len(pemBytes) == 0 {
		return fmt.Errorf("tls ca pem cannot be empty")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return fmt.Errorf("tls ca pem does not contain valid certificates")
	}

	cp := make([]byte, len(pemBytes))
	copy(cp, pemBytes)
	p.extraCAPEMs = append(p.extraCAPEMs, cp)
	return nil
}

func (p *Provider) buildHTTPClient() (*http.Client, error) {
	return httpclient.Build(httpclient.Config{
		Timeout:             p.transportConfig.Timeout,
		MaxIdleConns:        p.transportConfig.MaxIdleConns,
		MaxIdleConnsPerHost: p.transportConfig.MaxIdleConnsPerHost,
		IdleConnTimeout:     p.transportConfig.IdleConnTimeout,
		TLSCAPEM:            string(bytes.Join(p.extraCAPEMs, []byte("\n"))),
	})
}
