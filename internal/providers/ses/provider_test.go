package ses

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/mail"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/smithy-go"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

type stubSendEmailClient struct {
	input *sesv2.SendEmailInput
	out   *sesv2.SendEmailOutput
	err   error
}

func (c *stubSendEmailClient) SendEmail(_ context.Context, params *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	c.input = params
	if c.err != nil {
		return nil, c.err
	}
	if c.out != nil {
		return c.out, nil
	}
	return &sesv2.SendEmailOutput{}, nil
}

type fakeAPIError struct {
	code string
}

func (e fakeAPIError) Error() string                 { return e.code }
func (e fakeAPIError) ErrorCode() string             { return e.code }
func (e fakeAPIError) ErrorMessage() string          { return "api error" }
func (e fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func TestNewProviderValidWithInjectedClient(t *testing.T) {
	client := &stubSendEmailClient{}

	p, err := NewProvider("us-gov-west-1", "no-reply@example.com", "", "relay-config", testLogger(), WithClient(client))
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}

	if p.client != client {
		t.Fatal("expected injected client to be used")
	}
	if p.sender != "no-reply@example.com" {
		t.Fatalf("unexpected sender: %q", p.sender)
	}
	if p.configurationSet != "relay-config" {
		t.Fatalf("unexpected configuration set: %q", p.configurationSet)
	}
}

func TestNewProviderInvalid(t *testing.T) {
	tests := []struct {
		name   string
		region string
		sender string
		opts   []Option
		substr string
	}{
		{
			name:   "missing region",
			region: "",
			sender: "no-reply@example.com",
			substr: "region cannot be empty",
		},
		{
			name:   "missing sender",
			region: "us-gov-west-1",
			sender: "",
			substr: "sender cannot be empty",
		},
		{
			name:   "nil injected client",
			region: "us-gov-west-1",
			sender: "no-reply@example.com",
			opts:   []Option{WithClient(nil)},
			substr: "ses client cannot be nil",
		},
		{
			name:   "invalid retry option",
			region: "us-gov-west-1",
			sender: "no-reply@example.com",
			opts:   []Option{WithRetry(0, 0)},
			substr: "retry attempts must be >=",
		},
		{
			name:   "invalid static creds",
			region: "us-gov-west-1",
			sender: "no-reply@example.com",
			opts:   []Option{WithStaticCredentials("AKIA", "", "")},
			substr: "require both access key and secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProvider(tc.region, tc.sender, "", "", testLogger(), tc.opts...)
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
	client := &stubSendEmailClient{
		out: &sesv2.SendEmailOutput{MessageId: aws.String("msg-123")},
	}

	p, err := NewProvider("us-gov-west-1", "no-reply@example.com", "", "relay-config", testLogger(), WithClient(client))
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}

	msg := email.Message{
		From:     "reply@example.com",
		To:       []string{"one@example.com", " ", "two@example.com"},
		Subject:  "Test Subject",
		TextBody: "Text body",
		HTMLBody: "<p>HTML body</p>",
		Attachments: []email.Attachment{
			{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello note")},
		},
	}

	if err := p.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if client.input == nil {
		t.Fatal("expected SendEmailInput to be captured")
	}
	if got := aws.ToString(client.input.FromEmailAddress); got != "no-reply@example.com" {
		t.Fatalf("unexpected FromEmailAddress: %q", got)
	}
	if client.input.Destination == nil {
		t.Fatal("expected destination")
	}
	if got := client.input.Destination.ToAddresses; len(got) != 2 || got[0] != "one@example.com" || got[1] != "two@example.com" {
		t.Fatalf("unexpected recipients: %#v", got)
	}
	if got := client.input.ReplyToAddresses; len(got) != 1 || got[0] != "reply@example.com" {
		t.Fatalf("unexpected reply-to: %#v", got)
	}
	if got := aws.ToString(client.input.ConfigurationSetName); got != "relay-config" {
		t.Fatalf("unexpected configuration set: %q", got)
	}
	if client.input.Content == nil || client.input.Content.Raw == nil {
		t.Fatal("expected raw MIME content")
	}

	raw := client.input.Content.Raw.Data
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}

	if got := parsed.Header.Get("From"); got != "no-reply@example.com" {
		t.Fatalf("unexpected MIME From header: %q", got)
	}
	if got := parsed.Header.Get("Reply-To"); got != "reply@example.com" {
		t.Fatalf("unexpected MIME Reply-To header: %q", got)
	}

	decoder := &mime.WordDecoder{}
	decodedSubject, err := decoder.DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("DecodeHeader() error = %v", err)
	}
	if decodedSubject != "Test Subject" {
		t.Fatalf("unexpected subject: %q", decodedSubject)
	}

	rawText := string(raw)
	if !strings.Contains(rawText, "multipart/mixed") {
		t.Fatalf("expected multipart/mixed MIME body, got %q", rawText)
	}
	if !strings.Contains(rawText, "filename=\"note.txt\"") {
		t.Fatalf("expected attachment filename in MIME body, got %q", rawText)
	}
	if !strings.Contains(rawText, "aGVsbG8gbm90ZQ==") {
		t.Fatalf("expected attachment payload in MIME body, got %q", rawText)
	}
}

func TestClassifySendError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "throttling api error",
			err:       fakeAPIError{code: "ThrottlingException"},
			retryable: true,
		},
		{
			name:      "message rejected api error",
			err:       fakeAPIError{code: "MessageRejected"},
			retryable: false,
		},
		{
			name:      "context canceled",
			err:       context.Canceled,
			retryable: false,
		},
		{
			name:      "generic error",
			err:       errors.New("boom"),
			retryable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySendError(tc.err)
			if got.Retryable != tc.retryable {
				t.Fatalf("unexpected retryable: got %t want %t", got.Retryable, tc.retryable)
			}
		})
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
