package email

import "errors"

// DeliveryError is a provider-agnostic outbound error contract.
// Implementations can expose retryability and provider status metadata.
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
