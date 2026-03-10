package smtp

import (
	"errors"
	"testing"

	gosmtp "github.com/emersion/go-smtp"
)

type stubDeliveryError struct {
	temporary  bool
	statusCode int
}

func (e stubDeliveryError) Error() string        { return "delivery failure" }
func (e stubDeliveryError) ProviderName() string { return "stub" }
func (e stubDeliveryError) Temporary() bool      { return e.temporary }
func (e stubDeliveryError) HTTPStatusCode() int  { return e.statusCode }

func TestMapDeliveryErrorTemporary(t *testing.T) {
	err := MapDeliveryError(stubDeliveryError{temporary: true, statusCode: 503})
	if err == nil {
		t.Fatal("expected smtp error")
	}
	if err.Code != 451 {
		t.Fatalf("unexpected code: %d", err.Code)
	}
	if err.EnhancedCode != (gosmtp.EnhancedCode{4, 3, 0}) {
		t.Fatalf("unexpected enhanced code: %#v", err.EnhancedCode)
	}
}

func TestMapDeliveryErrorPermanent(t *testing.T) {
	err := MapDeliveryError(stubDeliveryError{temporary: false, statusCode: 400})
	if err == nil {
		t.Fatal("expected smtp error")
	}
	if err.Code != 554 {
		t.Fatalf("unexpected code: %d", err.Code)
	}
	if err.EnhancedCode != (gosmtp.EnhancedCode{5, 0, 0}) {
		t.Fatalf("unexpected enhanced code: %#v", err.EnhancedCode)
	}
}

func TestMapDeliveryErrorUnknownDefaultsTemporary(t *testing.T) {
	err := MapDeliveryError(errors.New("boom"))
	if err == nil {
		t.Fatal("expected smtp error")
	}
	if err.Code != 451 {
		t.Fatalf("unexpected code: %d", err.Code)
	}
}

func TestMapDeliveryErrorPassesThroughSMTPError(t *testing.T) {
	want := &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 5, 0}, Message: "nope"}
	got := MapDeliveryError(want)
	if got != want {
		t.Fatal("expected existing smtp error to pass through")
	}
}
