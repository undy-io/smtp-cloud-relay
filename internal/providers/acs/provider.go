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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/httpclient"
)

const (
	emailSendPath                  = "/emails:send"
	emailOperationPathPrefix       = "/emails/operations/"
	emailSendAPIVersion            = "2023-03-31"
	defaultRetryAttempts           = 3
	maxRetryAttempts               = 10
	defaultRetryBaseDelay          = 1 * time.Second
	maxResponseBodyBytes     int64 = 8 << 10
	maxDuration                    = time.Duration(1<<63 - 1)
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

var _ email.Provider = (*Provider)(nil)

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
	SenderAddress string            `json:"senderAddress"`
	Recipients    recipients        `json:"recipients"`
	Content       content           `json:"content"`
	ReplyTo       []recipient       `json:"replyTo,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Attachments   []attachmentDTO   `json:"attachments,omitempty"`
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

type operationResponseDTO struct {
	ID     string             `json:"id"`
	Status string             `json:"status"`
	Error  *operationErrorDTO `json:"error,omitempty"`
}

type operationErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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

func permanentSendError(requestID string, attempt, attempts int, err error) *SendError {
	return &SendError{
		RequestID: requestID,
		Attempt:   clampAttempt(attempt),
		Attempts:  clampAttempt(attempts),
		Retryable: false,
		Err:       err,
	}
}

func temporarySendError(requestID string, attempt, attempts, statusCode int, err error) *SendError {
	return &SendError{
		RequestID:  requestID,
		Attempt:    clampAttempt(attempt),
		Attempts:   clampAttempt(attempts),
		StatusCode: statusCode,
		Retryable:  true,
		Err:        err,
	}
}

func statusSendError(requestID string, attempt, attempts, statusCode int) *SendError {
	return &SendError{
		RequestID:  requestID,
		Attempt:    clampAttempt(attempt),
		Attempts:   clampAttempt(attempts),
		StatusCode: statusCode,
		Retryable:  isRetryableStatus(statusCode),
	}
}

func clampAttempt(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func NewProvider(endpoint, connectionString, sender string, logger *slog.Logger, opts ...Option) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	parsedEndpoint, accessKey, err := parseConnection(endpoint, connectionString)
	if err != nil {
		return nil, permanentSendError("", 1, 1, err)
	}

	if strings.TrimSpace(sender) == "" {
		return nil, permanentSendError("", 1, 1, fmt.Errorf("acs sender cannot be empty"))
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
			return nil, permanentSendError("", 1, 1, fmt.Errorf("apply provider option: %w", err))
		}
	}

	if provider.hasHTTPClient && len(provider.extraCAPEMs) > 0 {
		return nil, permanentSendError("", 1, 1, fmt.Errorf("cannot combine WithHTTPClient with TLS CA options"))
	}
	if provider.hasHTTPClient && provider.hasTransportConfig {
		return nil, permanentSendError("", 1, 1, fmt.Errorf("cannot combine WithHTTPClient with WithHTTPTransportConfig"))
	}

	if !provider.hasHTTPClient {
		client, err := provider.buildHTTPClient()
		if err != nil {
			return nil, permanentSendError("", 1, 1, err)
		}
		provider.httpClient = client
	}

	return provider, nil
}

func (p *Provider) Send(ctx context.Context, msg email.Message) error {
	result, err := p.Submit(ctx, msg, "")
	if err != nil {
		return err
	}
	return compatibilitySendResult(result)
}

func (p *Provider) Submit(ctx context.Context, msg email.Message, operationID string) (email.SubmissionResult, error) {
	body, err := buildSendRequest(p.sender, msg)
	if err != nil {
		return email.SubmissionResult{}, permanentSendError("", 1, 1, err)
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return email.SubmissionResult{}, permanentSendError("", 1, 1, fmt.Errorf("marshal acs request: %w", err))
	}

	requestID, err := newRequestID()
	if err != nil {
		return email.SubmissionResult{}, permanentSendError("", 1, 1, fmt.Errorf("generate request id: %w", err))
	}

	operationID = strings.TrimSpace(operationID)
	var lastResult email.SubmissionResult
	var lastErr *SendError
	for attempt := 1; attempt <= p.retryAttempts; attempt++ {
		attemptResult, attemptErr := p.submitOnce(ctx, requestID, operationID, attempt, bodyBytes)
		if attemptErr == nil {
			lastResult = attemptResult
			lastResult.OperationID = operationID
			lastResult.State = email.SubmissionStateRunning
			return lastResult, nil
		}

		lastErr = attemptErr
		if !attemptErr.Retryable || attempt == p.retryAttempts {
			return email.SubmissionResult{}, attemptErr
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
			return email.SubmissionResult{}, permanentSendError(requestID, attempt, p.retryAttempts, ctx.Err())
		case <-time.After(backoff):
		}
	}

	return email.SubmissionResult{}, lastErr
}

func (p *Provider) Poll(ctx context.Context, operationID string) (email.SubmissionStatus, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return email.SubmissionStatus{}, permanentSendError("", 1, 1, fmt.Errorf("acs poll operation id cannot be empty"))
	}

	requestID, err := newRequestID()
	if err != nil {
		return email.SubmissionStatus{}, permanentSendError(operationID, 1, 1, fmt.Errorf("generate request id: %w", err))
	}

	resp, sendErr := p.sendAuthorizedRequest(ctx, requestID, "", http.MethodGet, p.operationTarget(operationID), nil, 1)
	if sendErr != nil {
		return email.SubmissionStatus{}, sendErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))
		return email.SubmissionStatus{}, statusSendError(requestID, 1, 1, resp.StatusCode)
	}

	body, err := decodeOperationResponse(resp.Body)
	if err != nil {
		return email.SubmissionStatus{}, permanentSendError(requestID, 1, 1, fmt.Errorf("decode acs poll response: %w", err))
	}

	state, err := mapACSOperationState(body.Status)
	if err != nil {
		return email.SubmissionStatus{}, permanentSendError(requestID, 1, 1, err)
	}

	status := email.SubmissionStatus{
		OperationID:       operationID,
		RetryAfter:        parseRetryAfter(resp.Header.Get("retry-after")),
		State:             state,
		ProviderMessageID: strings.TrimSpace(body.ID),
	}
	if failure := submissionFailureForState(state, body.Error); failure != nil {
		status.Failure = failure
	}
	return status, nil
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

	replyToRecipients := normalizeReplyToRecipients(msg.ReplyTo)
	if len(replyToRecipients) > 0 {
		body.ReplyTo = replyToRecipients
	}
	if traceHeaders := email.SenderTraceHeaderMap(msg); len(traceHeaders) > 0 {
		body.Headers = traceHeaders
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

func normalizeReplyToRecipients(addresses []string) []recipient {
	out := make([]recipient, 0, len(addresses))
	for _, raw := range addresses {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		addr, err := mail.ParseAddress(raw)
		if err != nil {
			continue
		}
		if strings.TrimSpace(addr.Address) == "" {
			continue
		}

		out = append(out, recipient{Address: addr.Address})
	}
	return out
}

func (p *Provider) submitOnce(ctx context.Context, requestID, operationID string, attempt int, body []byte) (email.SubmissionResult, *SendError) {
	resp, sendErr := p.sendAuthorizedRequest(ctx, requestID, operationID, http.MethodPost, p.sendTarget(), body, attempt)
	if sendErr != nil {
		return email.SubmissionResult{}, sendErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))
		return email.SubmissionResult{}, statusSendError(requestID, attempt, p.retryAttempts, resp.StatusCode)
	}

	bodyResult, err := decodeOperationResponse(resp.Body)
	if err != nil {
		return email.SubmissionResult{}, permanentSendError(requestID, attempt, p.retryAttempts, fmt.Errorf("decode acs submit response: %w", err))
	}

	operationLocation := resp.Header.Get("operation-location")
	retryAfter := parseRetryAfter(resp.Header.Get("retry-after"))
	// ACS operation success means provider-side submission success, not final recipient delivery.
	p.logger.Info(
		"email accepted by acs",
		"request_id",
		requestID,
		"operation_location",
		operationLocation,
		"provider_message_id",
		strings.TrimSpace(bodyResult.ID),
	)
	return email.SubmissionResult{
		OperationLocation: strings.TrimSpace(operationLocation),
		RetryAfter:        retryAfter,
		State:             email.SubmissionStateRunning,
		ProviderMessageID: strings.TrimSpace(bodyResult.ID),
	}, nil
}

func (p *Provider) sendTarget() *url.URL {
	target := *p.endpoint
	target.Path = strings.TrimRight(target.Path, "/") + emailSendPath

	q := target.Query()
	q.Set("api-version", emailSendAPIVersion)
	target.RawQuery = q.Encode()
	return &target
}

func (p *Provider) operationTarget(operationID string) *url.URL {
	target := *p.endpoint
	target.Path = strings.TrimRight(target.Path, "/") + emailOperationPathPrefix + url.PathEscape(operationID)

	q := target.Query()
	q.Set("api-version", emailSendAPIVersion)
	target.RawQuery = q.Encode()
	return &target
}

func (p *Provider) sendAuthorizedRequest(ctx context.Context, requestID, operationID, method string, target *url.URL, body []byte, attempt int) (*http.Response, *SendError) {
	contentHash := hashRequestBody(body)
	xmsDate := time.Now().UTC().Format(http.TimeFormat)

	signature, err := p.buildSignature(method, target.RequestURI(), xmsDate, target.Host, contentHash)
	if err != nil {
		return nil, permanentSendError(requestID, attempt, p.retryAttempts, fmt.Errorf("build authorization header: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, permanentSendError(requestID, attempt, p.retryAttempts, fmt.Errorf("build request: %w", err))
	}

	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("x-ms-date", xmsDate)
	req.Header.Set("x-ms-content-sha256", contentHash)
	req.Header.Set("Authorization", signature)
	req.Header.Set("x-ms-client-request-id", requestID)
	if strings.TrimSpace(operationID) != "" {
		req.Header.Set("Operation-Id", strings.TrimSpace(operationID))
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, temporarySendError(requestID, attempt, p.retryAttempts, 0, err)
	}
	return resp, nil
}

func hashRequestBody(body []byte) string {
	hash := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(hash[:])
}

func decodeOperationResponse(r io.Reader) (operationResponseDTO, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBodyBytes))
	if err != nil {
		return operationResponseDTO{}, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return operationResponseDTO{}, nil
	}

	var result operationResponseDTO
	if err := json.Unmarshal(body, &result); err != nil {
		return operationResponseDTO{}, err
	}
	return result, nil
}

func mapACSOperationState(raw string) (email.SubmissionState, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "running", "notstarted":
		return email.SubmissionStateRunning, nil
	case "succeeded":
		return email.SubmissionStateSucceeded, nil
	case "failed":
		return email.SubmissionStateFailed, nil
	case "canceled", "cancelled":
		return email.SubmissionStateCanceled, nil
	default:
		return "", fmt.Errorf("unsupported acs operation status %q", raw)
	}
}

func submissionFailureForState(state email.SubmissionState, detail *operationErrorDTO) *email.SubmissionFailure {
	if state != email.SubmissionStateFailed && state != email.SubmissionStateCanceled {
		return nil
	}

	message := ""
	if detail != nil {
		message = strings.TrimSpace(detail.Message)
		if message == "" {
			message = strings.TrimSpace(detail.Code)
		}
	}
	if message == "" {
		message = fmt.Sprintf("acs operation ended in %q state", state)
	}

	return &email.SubmissionFailure{
		Message:   message,
		Temporary: false,
	}
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

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}

	when, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}

	delay := time.Until(when)
	if delay < 0 {
		return 0
	}
	return delay
}

func compatibilitySendResult(result email.SubmissionResult) error {
	switch result.State {
	case email.SubmissionStateRunning, email.SubmissionStateSucceeded:
		return nil
	case email.SubmissionStateFailed, email.SubmissionStateCanceled:
		if result.Failure != nil {
			err := errors.New(result.Failure.Message)
			if result.Failure.Temporary {
				return temporarySendError("", 1, 1, result.Failure.StatusCode, err)
			}
			sendErr := permanentSendError("", 1, 1, err)
			sendErr.StatusCode = result.Failure.StatusCode
			return sendErr
		}
		return permanentSendError("", 1, 1, fmt.Errorf("acs submission ended in %q state", result.State))
	default:
		return permanentSendError("", 1, 1, fmt.Errorf("unknown acs submission state %q", result.State))
	}
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
