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

type Provider struct {
	client           sendEmailAPI
	sender           string
	configurationSet string
	logger           *slog.Logger
}

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

func (e *SendError) ProviderName() string { return providerName }
func (e *SendError) Temporary() bool      { return e.Retryable }
func (e *SendError) HTTPStatusCode() int  { return e.StatusCode }

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

func (e *SendError) Unwrap() error { return e.Err }

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

func NewProvider(region, sender, endpoint, configurationSet string, logger *slog.Logger, opts ...Option) (*Provider, error) {
	region = strings.TrimSpace(region)
	sender = strings.TrimSpace(sender)
	endpoint = strings.TrimSpace(endpoint)
	configurationSet = strings.TrimSpace(configurationSet)

	if region == "" {
		return nil, fmt.Errorf("ses region cannot be empty")
	}
	if sender == "" {
		return nil, fmt.Errorf("ses sender cannot be empty")
	}
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
			return nil, fmt.Errorf("apply ses option: %w", err)
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
			return nil, fmt.Errorf("load aws config: %w", err)
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

func (p *Provider) Send(ctx context.Context, msg email.Message) error {
	raw, recipients, replyTo, err := buildRawMessage(p.sender, msg)
	if err != nil {
		return &SendError{
			Attempt:   1,
			Attempts:  1,
			Retryable: false,
			Err:       err,
		}
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
		return classifySendError(err)
	}

	p.logger.Info("email accepted by ses", "message_id", aws.ToString(out.MessageId), "to_count", len(recipients))
	return nil
}

func classifySendError(err error) *SendError {
	sendErr := &SendError{
		Attempt:   1,
		Attempts:  1,
		Retryable: false,
		Err:       err,
	}

	var respErr *smithyhttp.ResponseError
	if errorsAs(err, &respErr) {
		sendErr.StatusCode = respErr.HTTPStatusCode()
		sendErr.Retryable = isRetryableStatus(sendErr.StatusCode)
		return sendErr
	}

	var apiErr smithy.APIError
	if errorsAs(err, &apiErr) {
		sendErr.Retryable = isRetryableAPIError(apiErr.ErrorCode())
		return sendErr
	}

	if errorsIsContextCanceled(err) {
		sendErr.Retryable = false
		return sendErr
	}

	sendErr.Retryable = true
	return sendErr
}

func buildRawMessage(sender string, msg email.Message) ([]byte, []string, []string, error) {
	recipients := normalizeRecipients(msg.To)
	if len(recipients) == 0 {
		return nil, nil, nil, fmt.Errorf("message has no valid recipients")
	}

	replyTo := buildReplyTo(msg.From)

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

func buildReplyTo(from string) []string {
	from = strings.TrimSpace(from)
	if from == "" {
		return nil
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return nil
	}
	if strings.TrimSpace(addr.Address) == "" {
		return nil
	}
	return []string{addr.Address}
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
