package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
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
