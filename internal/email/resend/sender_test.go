package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/ticket"
	resendSDK "github.com/resend/resend-go/v4"
)

func TestSendVerificationCodeBuildsResendRequest(t *testing.T) {
	var got struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
		HTML    string   `json:"html"`
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/emails" {
			t.Errorf("request = %s %s, want POST /emails", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"id":"email-test-id"}`), nil
	})

	sender := newTestResendSender(t, transport)
	err := sender.SendVerificationCode(context.Background(), booking.VerificationCodeEmail{
		To:        "customer@example.com",
		Code:      "004281",
		ExpiresIn: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("SendVerificationCode() error = %v", err)
	}

	if got.From != "Cinema <tickets@example.com>" {
		t.Errorf("from = %q, want configured sender", got.From)
	}
	if len(got.To) != 1 || got.To[0] != "customer@example.com" {
		t.Errorf("to = %v, want [customer@example.com]", got.To)
	}
	if got.Subject != "Your booking verification code" {
		t.Errorf("subject = %q, want booking verification subject", got.Subject)
	}
	for name, body := range map[string]string{"text": got.Text, "html": got.HTML} {
		if !strings.Contains(body, "004281") {
			t.Errorf("%s body %q does not include the code", name, body)
		}
		if !strings.Contains(body, "10 minutes") {
			t.Errorf("%s body %q does not explain the expiry", name, body)
		}
		if !strings.Contains(body, "verify access to your booking") {
			t.Errorf("%s body %q does not explain what the code is for", name, body)
		}
	}
}

func TestSendVerificationCodeUsesRequestContext(t *testing.T) {
	requestStarted := make(chan struct{})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	sender := newTestResendSender(t, transport)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- sender.SendVerificationCode(ctx, booking.VerificationCodeEmail{
			To:        "customer@example.com",
			Code:      "004281",
			ExpiresIn: 10 * time.Minute,
		})
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Resend request did not reach test server")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SendVerificationCode() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendVerificationCode() did not return after context cancellation")
	}
}

func TestSendTicketEmailEmbedsQRsAndUsesIdempotencyKey(t *testing.T) {
	type attachment struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		ContentID   string `json:"content_id"`
		Content     []int  `json:"content"`
	}
	var got struct {
		To          []string     `json:"to"`
		Subject     string       `json:"subject"`
		Text        string       `json:"text"`
		HTML        string       `json:"html"`
		Attachments []attachment `json:"attachments"`
	}
	var idempotencyKey string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		idempotencyKey = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"id":"email-test-id"}`), nil
	})

	sender := newTestResendSender(t, transport)
	err := sender.SendTicketEmail(context.Background(), ticket.TicketEmail{
		To:               "customer@example.com",
		BookingReference: "BOOK-123",
		IdempotencyKey:   "booking-ticket-email:42",
		Tickets: []ticket.TicketEmailItem{
			{SeatLabel: "A&1", Credential: "opaque-ticket-value-1"},
			{SeatLabel: "A2", Credential: "opaque-ticket-value-2"},
		},
	})
	if err != nil {
		t.Fatalf("SendTicketEmail() error = %v", err)
	}
	if idempotencyKey != "booking-ticket-email:42" {
		t.Errorf("Idempotency-Key = %q, want booking-ticket-email:42", idempotencyKey)
	}
	if len(got.To) != 1 || got.To[0] != "customer@example.com" {
		t.Errorf("to = %v, want [customer@example.com]", got.To)
	}
	if got.Subject != "Your cinema tickets" {
		t.Errorf("subject = %q, want Your cinema tickets", got.Subject)
	}
	if !strings.Contains(got.HTML, "BOOK-123") || !strings.Contains(got.HTML, "A&amp;1") {
		t.Errorf("HTML body did not render escaped booking and seat details: %q", got.HTML)
	}
	if strings.Contains(got.Text, "opaque-ticket-value") {
		t.Errorf("plain-text body exposed a ticket credential: %q", got.Text)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("attachment count = %d, want 2", len(got.Attachments))
	}
	for i, gotAttachment := range got.Attachments {
		if gotAttachment.ContentType != "image/png" || gotAttachment.ContentID != fmt.Sprintf("ticket-qr-%d", i+1) {
			t.Errorf("attachment %d metadata = %q/%q", i+1, gotAttachment.ContentType, gotAttachment.ContentID)
		}
		if len(gotAttachment.Content) < 8 || gotAttachment.Content[0] != 137 || gotAttachment.Content[1] != 80 || gotAttachment.Content[2] != 78 || gotAttachment.Content[3] != 71 {
			t.Errorf("attachment %d does not contain a PNG image", i+1)
		}
		if !strings.Contains(got.HTML, "cid:"+gotAttachment.ContentID) {
			t.Errorf("HTML body does not reference inline attachment %q", gotAttachment.ContentID)
		}
	}
}

func TestNewResendSenderRejectsBlankConfiguration(t *testing.T) {
	if _, err := NewResendSender("  ", "sender@example.com"); err == nil {
		t.Error("NewResendSender() accepted a blank API key")
	}
	if _, err := NewResendSender("test-key", "  "); err == nil {
		t.Error("NewResendSender() accepted a blank sender address")
	}
}

func newTestResendSender(t *testing.T, transport http.RoundTripper) *ResendSender {
	t.Helper()
	sender, err := NewResendSender("test-api-key", " Cinema <tickets@example.com> ")
	if err != nil {
		t.Fatalf("NewResendSender() error = %v", err)
	}
	baseURL, err := url.Parse("https://resend.test/")
	if err != nil {
		t.Fatalf("parse test Resend URL: %v", err)
	}
	sender.client = resendSDK.NewCustomClient(&http.Client{Transport: transport}, "test-api-key")
	sender.client.BaseURL = baseURL
	return sender
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
