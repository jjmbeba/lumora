package resend

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/booking"
	"github.com/jjmbeba/lumora/backend/internal/staff"
	"github.com/jjmbeba/lumora/backend/internal/ticket"
	"github.com/resend/resend-go/v4"
	"github.com/skip2/go-qrcode"
)

type ResendSender struct {
	client            *resend.Client
	fromAddress       string
	recipientOverride string
	invitationHTML    *htmltemplate.Template
	invitationText    *texttemplate.Template
}

type Config struct {
	Environment   string
	TestRecipient string
}

//go:embed templates/staff_invitation.html templates/staff_invitation.txt
var staffInvitationTemplates embed.FS

var _ booking.VerificationEmailSender = (*ResendSender)(nil)
var _ staff.InvitationEmailSender = (*ResendSender)(nil)
var _ ticket.TicketEmailSender = (*ResendSender)(nil)

func NewResendSender(apiKey string, fromAddress string, config Config) (*ResendSender, error) {
	apiKey = strings.TrimSpace(apiKey)
	fromAddress = strings.TrimSpace(fromAddress)
	config.Environment = strings.TrimSpace(config.Environment)
	config.TestRecipient = strings.TrimSpace(config.TestRecipient)
	if apiKey == "" {
		return nil, errors.New("Resend API key is required")
	}

	if fromAddress == "" {
		return nil, errors.New("sender address is required")
	}

	if config.TestRecipient != "" && config.Environment != "development" {
		return nil, errors.New("Resend test recipient override is only allowed in development")
	}

	invitationHTML, err := htmltemplate.ParseFS(staffInvitationTemplates, "templates/staff_invitation.html")
	if err != nil {
		return nil, fmt.Errorf("parse staff invitation HTML template: %w", err)
	}

	invitationText, err := texttemplate.ParseFS(staffInvitationTemplates, "templates/staff_invitation.txt")
	if err != nil {
		return nil, fmt.Errorf("parse staff invitation text template: %w", err)
	}

	return &ResendSender{
		client:            resend.NewClient(apiKey),
		fromAddress:       fromAddress,
		recipientOverride: config.TestRecipient,
		invitationHTML:    invitationHTML,
		invitationText:    invitationText,
	}, nil
}

func (r *ResendSender) SendVerificationCode(ctx context.Context, email booking.VerificationCodeEmail) error {
	recipient, err := r.validateRecipient(email.To)
	if err != nil {
		return fmt.Errorf("verification email: %w", err)
	}
	if email.Code == "" || email.ExpiresIn <= 0 {
		return errors.New("verification email code and expiry are required")
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
		To:      []string{recipient},
		Subject: "Your booking verification code",
		Text:    textBody,
		Html:    htmlBody,
	}

	if _, err := r.client.Emails.SendWithContext(ctx, params); err != nil {
		return fmt.Errorf("send booking verification email with Resend: %w", err)
	}

	return nil
}

func (r *ResendSender) SendTicketEmail(ctx context.Context, email ticket.TicketEmail) error {
	recipient, err := r.validateRecipient(email.To)
	if err != nil {
		return fmt.Errorf("ticket email: %w", err)
	}
	if strings.TrimSpace(email.BookingReference) == "" || strings.TrimSpace(email.IdempotencyKey) == "" || len(email.Tickets) == 0 {
		return errors.New("ticket email booking reference, idempotency key, and tickets are required")
	}

	var htmlBody strings.Builder
	var textBody strings.Builder
	fmt.Fprintf(&htmlBody, "<p>Your booking <strong>%s</strong> is confirmed. Show each guest's QR code at the cinema.</p>", html.EscapeString(email.BookingReference))
	fmt.Fprintf(&textBody, "Your booking %s is confirmed. Show each guest's QR code at the cinema.\n", email.BookingReference)

	attachments := make([]*resend.Attachment, 0, len(email.Tickets))
	for i, ticketItem := range email.Tickets {
		if strings.TrimSpace(ticketItem.SeatLabel) == "" || ticketItem.Credential == "" {
			return fmt.Errorf("ticket %d is missing its seat label or credential", i+1)
		}

		png, err := qrcode.Encode(ticketItem.Credential, qrcode.Medium, 256)
		if err != nil {
			return fmt.Errorf("encode QR code for ticket %d: %w", i+1, err)
		}

		contentID := fmt.Sprintf("ticket-qr-%d", i+1)
		attachments = append(attachments, &resend.Attachment{
			Filename:    fmt.Sprintf("ticket-%d.png", i+1),
			Content:     png,
			ContentType: "image/png",
			ContentId:   contentID,
		})

		fmt.Fprintf(&htmlBody, "<section><h2>Seat %s</h2><img src=\"cid:%s\" alt=\"Admission QR code for seat %s\" width=\"256\" height=\"256\"></section>",
			html.EscapeString(ticketItem.SeatLabel), contentID, html.EscapeString(ticketItem.SeatLabel))
		fmt.Fprintf(&textBody, "\nSeat %s: QR code attached as ticket-%d.png\n", ticketItem.SeatLabel, i+1)
	}

	params := &resend.SendEmailRequest{
		From:        r.fromAddress,
		To:          []string{recipient},
		Subject:     "Your cinema tickets",
		Text:        textBody.String(),
		Html:        htmlBody.String(),
		Attachments: attachments,
	}
	options := &resend.SendEmailOptions{IdempotencyKey: email.IdempotencyKey}
	if _, err := r.client.Emails.SendWithOptions(ctx, params, options); err != nil {
		return fmt.Errorf("send ticket confirmation email with Resend: %w", err)
	}

	return nil
}

func (r *ResendSender) SendStaffInvitationEmail(ctx context.Context, email staff.StaffInvitationEmail) error {
	recipient, err := r.validateRecipient(email.To)
	if err != nil {
		return fmt.Errorf("staff invitation email: %w", err)
	}

	if strings.TrimSpace(email.SetupURL) == "" || strings.TrimSpace(email.IdempotencyKey) == "" || !email.ExpiresAt.After(time.Now()) {
		return errors.New("staff invitation email setup URL, idempotency key, and future expiry are required")
	}

	expiry := email.ExpiresAt.UTC().Format(time.RFC1123)
	data := staffInvitationTemplateData{
		DisplayName: invitationDisplayName(email.DisplayName),
		Role:        invitationRoleLabel(email.Role),
		SetupURL:    email.SetupURL,
		ExpiresAt:   expiry,
	}
	var textBody bytes.Buffer
	if err := r.invitationText.Execute(&textBody, data); err != nil {
		return fmt.Errorf("render staff invitation text email: %w", err)
	}
	var htmlBody bytes.Buffer
	if err := r.invitationHTML.Execute(&htmlBody, data); err != nil {
		return fmt.Errorf("render staff invitation HTML email: %w", err)
	}
	params := &resend.SendEmailRequest{
		From: r.fromAddress, To: []string{recipient},
		Subject: "You’re invited to join Lumora", Text: textBody.String(), Html: htmlBody.String(),
	}

	options := &resend.SendEmailOptions{IdempotencyKey: email.IdempotencyKey}
	if _, err := r.client.Emails.SendWithOptions(ctx, params, options); err != nil {
		return fmt.Errorf("send staff invitation email with Resend: %w", err)
	}

	return nil
}

type staffInvitationTemplateData struct {
	DisplayName string
	Role        string
	SetupURL    string
	ExpiresAt   string
}

func invitationDisplayName(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return "there"
}

func invitationRoleLabel(role staff.Role) string {
	switch role {
	case staff.RoleManager:
		return "Manager"
	case staff.RoleScanner:
		return "Scanner"
	default:
		return "staff member"
	}
}

func (r *ResendSender) validateRecipient(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("recipient email is required")
	}

	if r.recipientOverride != "" {
		return r.recipientOverride, nil
	}
	return address, nil
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
