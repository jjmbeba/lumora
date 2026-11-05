package checkout

import "errors"

var (
	ErrBookingCreatorRequired      = errors.New("booking creator is required")
	ErrInitializationStoreRequired = errors.New("payment initialization store is required")
	ErrInvalidPaymentMethod        = errors.New("unsupported payment method")
)
