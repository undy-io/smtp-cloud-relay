package ses

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

const (
	providerName          = "ses"
	defaultRetryAttempts  = 3
	maxRetryAttempts      = 10
	defaultRetryBaseDelay = 1 * time.Second
	maxDuration           = time.Duration(1<<63 - 1)
)

type sendEmailAPI interface {
	SendEmail(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Provider implements the async submit and poll contract for SES v2.
type Provider struct {
	client           sendEmailAPI
	sender           string
	configurationSet string
	logger           *slog.Logger
}

var _ email.Provider = (*Provider)(nil)

type providerOptions struct {
	httpClient      *http.Client
	retryAttempts   int
	retryBaseDelay  time.Duration
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	client          sendEmailAPI
}

// Option mutates SES provider initialization behavior.
type Option func(*providerOptions) error

// SendError contains detailed SES delivery failure information.
type SendError struct {
	RequestID  string
	Attempt    int
	Attempts   int
	StatusCode int
	Retryable  bool
	Err        error
}

var _ email.DeliveryError = (*SendError)(nil)

// ProviderName identifies SES as the delivery provider.
func (e *SendError) ProviderName() string { return providerName }
// Temporary reports whether the SES delivery failure is retryable.
func (e *SendError) Temporary() bool      { return e.Retryable }
// HTTPStatusCode returns the SES HTTP status code when available.
func (e *SendError) HTTPStatusCode() int  { return e.StatusCode }

// Error formats the SES delivery failure with retry metadata.
func (e *SendError) Error() string {
	prefix := fmt.Sprintf("ses send failed request_id=%s attempt=%d/%d retryable=%t", e.RequestID, e.Attempt, e.Attempts, e.Retryable)
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s status=%d", prefix, e.StatusCode)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s err=%v", prefix, e.Err)
	}
	return prefix
}

// Unwrap returns the underlying SES failure.
func (e *SendError) Unwrap() error { return e.Err }

func permanentSendError(err error) *SendError {
	return &SendError{
		Attempt:   1,
		Attempts:  1,
		Retryable: false,
		Err:       err,
	}
}

func temporarySendError(statusCode int, err error) *SendError {
	return &SendError{
		Attempt:    1,
		Attempts:   1,
		StatusCode: statusCode,
		Retryable:  true,
		Err:        err,
	}
}

// WithHTTPClient injects a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(o *providerOptions) error {
		if client == nil {
			return fmt.Errorf("http client cannot be nil")
		}
		o.httpClient = client
		return nil
	}
}

// WithRetry configures AWS SDK retry behavior.
func WithRetry(attempts int, baseDelay time.Duration) Option {
	return func(o *providerOptions) error {
		if attempts < 1 {
			return fmt.Errorf("retry attempts must be >= 1")
		}
		if attempts > maxRetryAttempts {
			return fmt.Errorf("retry attempts must be <= %d", maxRetryAttempts)
		}
		if baseDelay <= 0 {
			return fmt.Errorf("retry base delay must be > 0")
		}
		o.retryAttempts = attempts
		o.retryBaseDelay = baseDelay
		return nil
	}
}

// WithStaticCredentials configures explicit static AWS credentials.
func WithStaticCredentials(accessKeyID, secretAccessKey, sessionToken string) Option {
	return func(o *providerOptions) error {
		accessKeyID = strings.TrimSpace(accessKeyID)
		secretAccessKey = strings.TrimSpace(secretAccessKey)
		sessionToken = strings.TrimSpace(sessionToken)

		hasAccess := accessKeyID != ""
		hasSecret := secretAccessKey != ""
		if hasAccess != hasSecret {
			return fmt.Errorf("ses static credentials require both access key and secret")
		}
		if sessionToken != "" && (!hasAccess || !hasSecret) {
			return fmt.Errorf("session token requires access key and secret")
		}

		o.accessKeyID = accessKeyID
		o.secretAccessKey = secretAccessKey
		o.sessionToken = sessionToken
		return nil
	}
}

// WithClient injects a custom SES client (tests).
func WithClient(client sendEmailAPI) Option {
	return func(o *providerOptions) error {
		if client == nil {
			return fmt.Errorf("ses client cannot be nil")
		}
		o.client = client
		return nil
	}
}

// NewProvider validates SES configuration and constructs the provider client.
func NewProvider(region, sender, endpoint, configurationSet string, logger *slog.Logger, opts ...Option) (*Provider, error) {
	region = strings.TrimSpace(region)
	sender = strings.TrimSpace(sender)
	endpoint = strings.TrimSpace(endpoint)
	configurationSet = strings.TrimSpace(configurationSet)

	if region == "" {
		return nil, permanentSendError(fmt.Errorf("ses region cannot be empty"))
	}
	if sender == "" {
		return nil, permanentSendError(fmt.Errorf("ses sender cannot be empty"))
	}
	validatedEndpoint, err := validateEndpoint(endpoint)
	if err != nil {
		return nil, permanentSendError(err)
	}
	endpoint = validatedEndpoint
	if logger == nil {
		logger = slog.Default()
	}

	o := providerOptions{
		retryAttempts:  defaultRetryAttempts,
		retryBaseDelay: defaultRetryBaseDelay,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&o); err != nil {
			return nil, permanentSendError(fmt.Errorf("apply ses option: %w", err))
		}
	}

	client := o.client
	if client == nil {
		loadOptions := []func(*awsconfig.LoadOptions) error{
			awsconfig.WithRegion(region),
			awsconfig.WithRetryer(func() aws.Retryer {
				return retry.NewStandard(func(so *retry.StandardOptions) {
					so.MaxAttempts = o.retryAttempts
					so.Backoff = exponentialBackoff{base: o.retryBaseDelay}
				})
			}),
		}
		if o.httpClient != nil {
			loadOptions = append(loadOptions, awsconfig.WithHTTPClient(o.httpClient))
		}
		if o.accessKeyID != "" {
			loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(o.accessKeyID, o.secretAccessKey, o.sessionToken),
			))
		}

		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
		if err != nil {
			return nil, permanentSendError(fmt.Errorf("load aws config: %w", err))
		}

		clientOptions := make([]func(*sesv2.Options), 0, 1)
		if endpoint != "" {
			clientOptions = append(clientOptions, func(so *sesv2.Options) {
				so.EndpointResolver = sesv2.EndpointResolverFromURL(endpoint)
			})
		}

		client = sesv2.NewFromConfig(awsCfg, clientOptions...)
	}

	return &Provider{
		client:           client,
		sender:           sender,
		configurationSet: configurationSet,
		logger:           logger,
	}, nil
}

func validateEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse ses endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid ses endpoint %q", raw)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("ses endpoint must use https scheme")
	}

	return u.String(), nil
}

// Submit treats SES SendEmail acceptance as terminal submission success for the
// v1 async provider contract.
func (p *Provider) Submit(ctx context.Context, msg email.Message, _ string) (email.SubmissionResult, error) {
	raw, recipients, replyTo, err := buildRawMessage(p.sender, msg)
	if err != nil {
		return email.SubmissionResult{}, permanentSendError(err)
	}

	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(p.sender),
		Destination:      &types.Destination{ToAddresses: recipients},
		Content: &types.EmailContent{
			Raw: &types.RawMessage{
				Data: raw,
			},
		},
	}
	if len(replyTo) > 0 {
		input.ReplyToAddresses = replyTo
	}
	if p.configurationSet != "" {
		input.ConfigurationSetName = aws.String(p.configurationSet)
	}

	out, err := p.client.SendEmail(ctx, input)
	if err != nil {
		return email.SubmissionResult{}, classifySendError(err)
	}

	messageID := aws.ToString(out.MessageId)
	p.logger.Info("email accepted by ses", "message_id", messageID, "to_count", len(recipients))
	return email.SubmissionResult{
		State:             email.SubmissionStateSucceeded,
		ProviderMessageID: messageID,
	}, nil
}

// Poll exists only to satisfy the shared async provider interface. SES is not
// modeled as a long-running provider in this phase, so Poll returns immediate
// terminal success for the requested operation identifier.
func (p *Provider) Poll(_ context.Context, operationID string) (email.SubmissionStatus, error) {
	return email.SubmissionStatus{
		OperationID: strings.TrimSpace(operationID),
		State:       email.SubmissionStateSucceeded,
	}, nil
}

func classifySendError(err error) *SendError {
	var respErr *smithyhttp.ResponseError
	if errorsAs(err, &respErr) {
		if isRetryableStatus(respErr.HTTPStatusCode()) {
			return temporarySendError(respErr.HTTPStatusCode(), err)
		}
		sendErr := permanentSendError(err)
		sendErr.StatusCode = respErr.HTTPStatusCode()
		return sendErr
	}

	var apiErr smithy.APIError
	if errorsAs(err, &apiErr) {
		if isRetryableAPIError(apiErr.ErrorCode()) {
			return temporarySendError(0, err)
		}
		return permanentSendError(err)
	}

	if errorsIsContextCanceled(err) {
		return permanentSendError(err)
	}

	return temporarySendError(0, err)
}

func buildRawMessage(sender string, msg email.Message) ([]byte, []string, []string, error) {
	recipients := normalizeRecipients(msg.To)
	if len(recipients) == 0 {
		return nil, nil, nil, fmt.Errorf("message has no valid recipients")
	}

	replyTo := normalizeReplyTo(msg.ReplyTo)

	plain := msg.TextBody
	html := msg.HTMLBody
	if strings.TrimSpace(plain) == "" && strings.TrimSpace(html) == "" {
		plain = "(empty message)"
	}

	contentType, body, err := buildMIMEBody(plain, html, msg.Attachments)
	if err != nil {
		return nil, nil, nil, err
	}

	var out bytes.Buffer
	writeHeader(&out, "From", sender)
	writeHeader(&out, "To", strings.Join(recipients, ", "))
	if len(replyTo) > 0 {
		writeHeader(&out, "Reply-To", strings.Join(replyTo, ", "))
	}
	for _, header := range email.SenderTraceHeaders(msg) {
		writeHeader(&out, header.Name, header.Value)
	}
	if strings.TrimSpace(msg.Subject) != "" {
		writeHeader(&out, "Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	} else {
		writeHeader(&out, "Subject", "")
	}
	writeHeader(&out, "MIME-Version", "1.0")
	writeHeader(&out, "Content-Type", contentType)
	out.WriteString("\r\n")
	out.Write(body)

	return out.Bytes(), recipients, replyTo, nil
}

func normalizeRecipients(recipients []string) []string {
	out := make([]string, 0, len(recipients))
	for _, r := range recipients {
		trimmed := strings.TrimSpace(r)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func normalizeReplyTo(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		addr, err := mail.ParseAddress(value)
		if err != nil {
			continue
		}
		if strings.TrimSpace(addr.Address) == "" {
			continue
		}

		out = append(out, addr.Address)
	}
	return out
}

func buildMIMEBody(plain, html string, attachments []email.Attachment) (string, []byte, error) {
	plain = normalizeLineEndings(plain)
	html = normalizeLineEndings(html)

	switch {
	case len(attachments) == 0 && plain != "" && html != "":
		contentType, body, err := buildAlternativeBody(plain, html)
		return contentType, body, err
	case len(attachments) == 0 && html != "":
		return "text/html; charset=UTF-8", []byte(html), nil
	case len(attachments) == 0:
		return "text/plain; charset=UTF-8", []byte(plain), nil
	default:
		return buildMixedBody(plain, html, attachments)
	}
}

func buildAlternativeBody(plain, html string) (string, []byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	if plain != "" {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", "text/plain; charset=UTF-8")
		h.Set("Content-Transfer-Encoding", "8bit")
		part, err := w.CreatePart(h)
		if err != nil {
			return "", nil, err
		}
		if _, err := io.WriteString(part, plain); err != nil {
			return "", nil, err
		}
	}
	if html != "" {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", "text/html; charset=UTF-8")
		h.Set("Content-Transfer-Encoding", "8bit")
		part, err := w.CreatePart(h)
		if err != nil {
			return "", nil, err
		}
		if _, err := io.WriteString(part, html); err != nil {
			return "", nil, err
		}
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("multipart/alternative; boundary=%q", w.Boundary()), body.Bytes(), nil
}

func buildMixedBody(plain, html string, attachments []email.Attachment) (string, []byte, error) {
	var body bytes.Buffer
	mixed := multipart.NewWriter(&body)

	if plain != "" && html != "" {
		altContentType, altBody, err := buildAlternativeBody(plain, html)
		if err != nil {
			return "", nil, err
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", altContentType)
		part, err := mixed.CreatePart(h)
		if err != nil {
			return "", nil, err
		}
		if _, err := part.Write(altBody); err != nil {
			return "", nil, err
		}
	} else {
		contentType := "text/plain; charset=UTF-8"
		bodyText := plain
		if html != "" {
			contentType = "text/html; charset=UTF-8"
			bodyText = html
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", contentType)
		h.Set("Content-Transfer-Encoding", "8bit")
		part, err := mixed.CreatePart(h)
		if err != nil {
			return "", nil, err
		}
		if _, err := io.WriteString(part, bodyText); err != nil {
			return "", nil, err
		}
	}

	for i, a := range attachments {
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}
		contentType := strings.TrimSpace(a.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		h := textproto.MIMEHeader{}
		h.Set("Content-Type", fmt.Sprintf("%s; name=%q", contentType, name))
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		h.Set("Content-Transfer-Encoding", "base64")
		part, err := mixed.CreatePart(h)
		if err != nil {
			return "", nil, err
		}
		if err := writeBase64MIME(part, a.Data); err != nil {
			return "", nil, err
		}
	}

	if err := mixed.Close(); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("multipart/mixed; boundary=%q", mixed.Boundary()), body.Bytes(), nil
}

func writeBase64MIME(w io.Writer, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	const lineLen = 76
	for len(encoded) > lineLen {
		if _, err := io.WriteString(w, encoded[:lineLen]+"\r\n"); err != nil {
			return err
		}
		encoded = encoded[lineLen:]
	}
	if _, err := io.WriteString(w, encoded+"\r\n"); err != nil {
		return err
	}
	return nil
}

func writeHeader(w io.Writer, key, value string) {
	_, _ = io.WriteString(w, key+": "+value+"\r\n")
}

func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func isRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func isRetryableAPIError(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return false
	}
	return strings.Contains(code, "throttl") ||
		strings.Contains(code, "too_many_requests") ||
		strings.Contains(code, "toomanyrequests") ||
		strings.Contains(code, "timeout") ||
		strings.Contains(code, "serviceunavailable") ||
		strings.Contains(code, "internal")
}

func errorsIsContextCanceled(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "context canceled") ||
		strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded")
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

type exponentialBackoff struct {
	base time.Duration
}

// BackoffDelay returns the exponential retry delay for the given attempt.
func (b exponentialBackoff) BackoffDelay(attempt int, _ error) (time.Duration, error) {
	if b.base <= 0 {
		return defaultRetryBaseDelay, nil
	}
	if attempt <= 1 {
		return b.base, nil
	}

	backoff := b.base
	for i := 1; i < attempt; i++ {
		if backoff > maxDuration/2 {
			return maxDuration, nil
		}
		backoff *= 2
	}
	return backoff, nil
}
