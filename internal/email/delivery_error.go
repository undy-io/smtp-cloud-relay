package email

import "errors"

// DeliveryError is a provider-agnostic outbound error contract.
// Providers should use it for provider-owned failures in both setup and send paths
// so caller retry and SMTP mapping logic can stay provider-neutral.
type DeliveryError interface {
	error
	ProviderName() string
	Temporary() bool
	HTTPStatusCode() int
}

// AsDeliveryError unwraps err into a DeliveryError when available.
func AsDeliveryError(err error) (DeliveryError, bool) {
	var de DeliveryError
	if !errors.As(err, &de) {
		return nil, false
	}
	return de, true
}
