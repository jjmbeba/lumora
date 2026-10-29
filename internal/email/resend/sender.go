package resend

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
	"github.com/resend/resend-go/v4"
)

type ResendSender struct {
	client      *resend.Client
	fromAddress string
}

func NewResendSender(apiKey string, fromAddress string) (*ResendSender, error) {
	apiKey = strings.TrimSpace(apiKey)
	fromAddress = strings.TrimSpace(fromAddress)
	if apiKey == "" {
		return nil, errors.New("Resend API key is required")
	}

	if fromAddress == "" {
		return nil, errors.New("sender address is required")
	}

	return &ResendSender{
		client:      resend.NewClient(apiKey),
		fromAddress: fromAddress,
	}, nil
}

func (r *ResendSender) SendVerificationCode(ctx context.Context, email booking.VerificationCodeEmail) error {
	if strings.TrimSpace(email.To) == "" || email.Code == "" || email.ExpiresIn <= 0 {
		return errors.New("verification email recipient, code, and expiry are required")
	}

	expiry := formatExpiry(email.ExpiresIn)
	textBody := fmt.Sprintf(
		"Your booking verification code is %s.\n\nEnter this code to verify access to your booking. It expires in %s. If you did not request this code, you can ignore this email.",
		email.Code,
		expiry,
	)
	htmlBody := fmt.Sprintf(
		"<p>Your booking verification code is:</p><p><strong>%s</strong></p><p>Enter this code to verify access to your booking. It expires in %s. If you did not request this code, you can ignore this email.</p>",
		html.EscapeString(email.Code),
		html.EscapeString(expiry),
	)
	params := &resend.SendEmailRequest{
		From:    r.fromAddress,
		To:      []string{strings.TrimSpace(email.To)},
		Subject: "Your booking verification code",
		Text:    textBody,
		Html:    htmlBody,
	}

	if _, err := r.client.Emails.SendWithContext(ctx, params); err != nil {
		return fmt.Errorf("send booking verification email with Resend: %w", err)
	}

	return nil
}

func formatExpiry(expiry time.Duration) string {
	if expiry%time.Hour == 0 {
		hours := int(expiry / time.Hour)
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	if expiry%time.Minute == 0 {
		minutes := int(expiry / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	if expiry%time.Second == 0 {
		seconds := int(expiry / time.Second)
		if seconds == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", seconds)
	}
	return expiry.String()
}
