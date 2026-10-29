package checkout

import "errors"

var (
	ErrBookingCreatorRequired        = errors.New("booking creator is required")
	ErrInitializationCreatorRequired = errors.New("payment initialization creator is required")
	ErrInvalidPaymentMethod          = errors.New("unsupported payment method")
)
