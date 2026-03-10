package smtp

import (
	"errors"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

var (
	temporaryDeliveryError = &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "temporary relay failure"}
	permanentDeliveryError = &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 0, 0}, Message: "permanent relay failure"}
)

// MapDeliveryError converts provider-owned delivery failures into SMTP reply codes.
func MapDeliveryError(err error) *gosmtp.SMTPError {
	if err == nil {
		return nil
	}

	var smtpErr *gosmtp.SMTPError
	if errors.As(err, &smtpErr) {
		return smtpErr
	}

	deliveryErr, ok := email.AsDeliveryError(err)
	if !ok {
		return temporaryDeliveryError
	}
	if deliveryErr.Temporary() {
		return temporaryDeliveryError
	}
	return permanentDeliveryError
}
