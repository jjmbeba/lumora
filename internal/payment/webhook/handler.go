package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"example.com/cinema-tickets-backend/internal/payment"
)

type EventStore interface {
	RecordAndSchedule(ctx context.Context, event payment.ProviderWebhookEvent) error
}

type paystackWebhookEnvelope struct {
	Event string         `json:"event"`
	Data  jsontext.Value `json:"data"`
}

type paystackChargeSuccessData struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	Reference string    `json:"reference"`
	Amount    int64     `json:"amount"`
	PaidAt    time.Time `json:"paid_at"`
	Currency  string    `json:"currency"`
}

type Handler struct {
	secretKey []byte
	store     EventStore
}

const maxWebhookBodyBytes = 1 << 20

func NewHandler(secretKey string, store EventStore) (*Handler, error) {
	if secretKey == "" {
		return nil, fmt.Errorf("Paystack secret key cannot be empty")
	}

	if store == nil {
		return nil, fmt.Errorf("Event store must not be nil")
	}

	return &Handler{
		secretKey: []byte(secretKey),
		store:     store,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawBody, ok := h.readBody(w, r)
	if !ok {
		return
	}

	signature := r.Header.Get("x-paystack-signature")
	if !h.verifySignature(rawBody, signature) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)

		return
	}

	parsedEvent, supported, err := parseWebhookEvent(rawBody)
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)

		return
	}

	if !supported {
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := h.store.RecordAndSchedule(r.Context(), parsedEvent); err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "unable to read payload", http.StatusBadRequest)
		}

		return nil, false
	}

	if len(rawBody) == 0 {
		http.Error(w, "empty payload", http.StatusBadRequest)
		return nil, false
	}

	return rawBody, true
}

func (h *Handler) verifySignature(rawBody []byte, signature string) bool {
	if signature == "" {
		return false
	}

	mac := hmac.New(sha512.New, h.secretKey)
	mac.Write(rawBody)
	expected := mac.Sum(nil)

	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}

	return hmac.Equal(expected, provided)
}

func parseWebhookEvent(rawBody []byte) (payment.ProviderWebhookEvent, bool, error) {
	var envelope paystackWebhookEnvelope

	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return payment.ProviderWebhookEvent{}, false, fmt.Errorf("decode webhook envelope: %w", err)
	}

	if strings.TrimSpace(envelope.Event) == "" {
		return payment.ProviderWebhookEvent{}, false, errors.New("webhook event type is required")
	}

	if envelope.Event != "charge.success" {
		return payment.ProviderWebhookEvent{}, false, nil
	}

	var data paystackChargeSuccessData
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return payment.ProviderWebhookEvent{}, true, errors.New("charge.success data is required")
	}

	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return payment.ProviderWebhookEvent{}, true, fmt.Errorf("decode charge.success data: %w", err)
	}

	if data.ID <= 0 {
		return payment.ProviderWebhookEvent{}, true, fmt.Errorf("transaction ID must be positive")
	}

	if data.Status != "success" {
		return payment.ProviderWebhookEvent{}, true, errors.New("charge.success status must be success")
	}

	if strings.TrimSpace(data.Reference) == "" {
		return payment.ProviderWebhookEvent{}, true, errors.New("transaction reference is required")
	}

	if data.Amount <= 0 {
		return payment.ProviderWebhookEvent{}, true, errors.New("transaction amount must be positive")
	}

	if strings.TrimSpace(data.Currency) == "" {
		return payment.ProviderWebhookEvent{}, true, errors.New("transaction currency is required")
	}
	if data.PaidAt.IsZero() {
		return payment.ProviderWebhookEvent{}, true, errors.New("transaction paid_at is required")
	}

	return payment.ProviderWebhookEvent{
		EventType:      envelope.Event,
		TransactionID:  data.ID,
		Reference:      data.Reference,
		ProviderStatus: data.Status,
		AmountMinor:    data.Amount,
		Currency:       data.Currency,
		ProviderPaidAt: data.PaidAt,
	}, true, nil
}
