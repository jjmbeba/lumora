package payment

import "time"

type ProviderWebhookEvent struct {
	EventType      string
	TransactionID  int64
	Reference      string
	ProviderStatus string
	AmountMinor    int64
	Currency       string
	ProviderPaidAt time.Time
}
