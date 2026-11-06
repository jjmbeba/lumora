package resend

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
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
	logoPNG           []byte
	verificationHTML  *htmltemplate.Template
	verificationText  *texttemplate.Template
	ticketHTML        *htmltemplate.Template
	ticketText        *texttemplate.Template
	invitationHTML    *htmltemplate.Template
	invitationText    *texttemplate.Template
}

type Config struct {
	Environment   string
	TestRecipient string
}

//go:embed templates/*.html templates/*.txt templates/*.png
var emailTemplates embed.FS

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

	verificationHTML, err := htmltemplate.ParseFS(emailTemplates, "templates/booking_verification.html")
	if err != nil {
		return nil, fmt.Errorf("parse booking verification HTML template: %w", err)
	}

	verificationText, err := texttemplate.ParseFS(emailTemplates, "templates/booking_verification.txt")
	if err != nil {
		return nil, fmt.Errorf("parse booking verification text template: %w", err)
	}

	ticketHTML, err := htmltemplate.ParseFS(emailTemplates, "templates/booking_tickets.html")
	if err != nil {
		return nil, fmt.Errorf("parse booking tickets HTML template: %w", err)
	}

	ticketText, err := texttemplate.ParseFS(emailTemplates, "templates/booking_tickets.txt")
	if err != nil {
		return nil, fmt.Errorf("parse booking tickets text template: %w", err)
	}

	invitationHTML, err := htmltemplate.ParseFS(emailTemplates, "templates/staff_invitation.html")
	if err != nil {
		return nil, fmt.Errorf("parse staff invitation HTML template: %w", err)
	}

	invitationText, err := texttemplate.ParseFS(emailTemplates, "templates/staff_invitation.txt")
	if err != nil {
		return nil, fmt.Errorf("parse staff invitation text template: %w", err)
	}
	logoPNG, err := emailTemplates.ReadFile("templates/lumora-mark.png")
	if err != nil {
		return nil, fmt.Errorf("read Lumora email mark: %w", err)
	}

	return &ResendSender{
		client:            resend.NewClient(apiKey),
		fromAddress:       fromAddress,
		recipientOverride: config.TestRecipient,
		logoPNG:           logoPNG,
		verificationHTML:  verificationHTML,
		verificationText:  verificationText,
		ticketHTML:        ticketHTML,
		ticketText:        ticketText,
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

	data := bookingVerificationTemplateData{Code: email.Code, ExpiresIn: formatExpiry(email.ExpiresIn)}
	var textBody bytes.Buffer
	if err := r.verificationText.Execute(&textBody, data); err != nil {
		return fmt.Errorf("render booking verification text email: %w", err)
	}
	var htmlBody bytes.Buffer
	if err := r.verificationHTML.Execute(&htmlBody, data); err != nil {
		return fmt.Errorf("render booking verification HTML email: %w", err)
	}
	params := &resend.SendEmailRequest{
		From:    r.fromAddress,
		To:      []string{recipient},
		Subject: "Your booking verification code",
		Text:    textBody.String(),
		Html:    htmlBody.String(),
		Attachments: []*resend.Attachment{
			r.logoAttachment(),
		},
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

	data := bookingTicketsTemplateData{
		BookingReference: email.BookingReference,
		MovieTitle:       email.MovieTitle,
		ScreeningAt:      formatScreeningAt(email.ScreeningAt),
		AuditoriumName:   email.AuditoriumName,
		ShowInlineQRs:    len(email.Tickets) <= 2,
		Tickets:          make([]bookingTicketTemplateItem, 0, len(email.Tickets)),
	}
	attachments := make([]*resend.Attachment, 0, len(email.Tickets)+1)
	seatLabels := make([]string, 0, len(email.Tickets))
	for i, ticketItem := range email.Tickets {
		if strings.TrimSpace(ticketItem.SeatLabel) == "" || ticketItem.Credential == "" {
			return fmt.Errorf("ticket %d is missing its seat label or credential", i+1)
		}

		png, err := qrcode.Encode(ticketItem.Credential, qrcode.Medium, 256)
		if err != nil {
			return fmt.Errorf("encode QR code for ticket %d: %w", i+1, err)
		}

		attachmentName := fmt.Sprintf("ticket-%d.png", i+1)
		contentID := fmt.Sprintf("ticket-qr-%d", i+1)
		attachments = append(attachments, &resend.Attachment{
			Filename:    attachmentName,
			Content:     png,
			ContentType: "image/png",
			ContentId:   contentID,
		})
		data.Tickets = append(data.Tickets, bookingTicketTemplateItem{
			SeatLabel: ticketItem.SeatLabel, AttachmentName: attachmentName, ContentID: contentID,
		})
		seatLabels = append(seatLabels, ticketItem.SeatLabel)
	}
	data.SeatLabels = strings.Join(seatLabels, ", ")

	var textBody bytes.Buffer
	if err := r.ticketText.Execute(&textBody, data); err != nil {
		return fmt.Errorf("render booking tickets text email: %w", err)
	}
	var htmlBody bytes.Buffer
	if err := r.ticketHTML.Execute(&htmlBody, data); err != nil {
		return fmt.Errorf("render booking tickets HTML email: %w", err)
	}

	subject := "Your cinema tickets"
	if title := strings.TrimSpace(email.MovieTitle); title != "" {
		subject = "Your tickets for " + title
	}
	params := &resend.SendEmailRequest{
		From:        r.fromAddress,
		To:          []string{recipient},
		Subject:     subject,
		Text:        textBody.String(),
		Html:        htmlBody.String(),
		Attachments: append(attachments, r.logoAttachment()),
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
		Attachments: []*resend.Attachment{
			r.logoAttachment(),
		},
	}

	options := &resend.SendEmailOptions{IdempotencyKey: email.IdempotencyKey}
	if _, err := r.client.Emails.SendWithOptions(ctx, params, options); err != nil {
		return fmt.Errorf("send staff invitation email with Resend: %w", err)
	}

	return nil
}

func (r *ResendSender) logoAttachment() *resend.Attachment {
	return &resend.Attachment{
		Filename:    "lumora-mark.png",
		Content:     r.logoPNG,
		ContentType: "image/png",
		ContentId:   "lumora-mark",
	}
}

type bookingVerificationTemplateData struct {
	Code      string
	ExpiresIn string
}

type bookingTicketsTemplateData struct {
	BookingReference string
	MovieTitle       string
	ScreeningAt      string
	AuditoriumName   string
	SeatLabels       string
	ShowInlineQRs    bool
	Tickets          []bookingTicketTemplateItem
}

type bookingTicketTemplateItem struct {
	SeatLabel      string
	AttachmentName string
	ContentID      string
}

func formatScreeningAt(screeningAt time.Time) string {
	if screeningAt.IsZero() {
		return ""
	}

	return screeningAt.Format("Monday, 2 January 2006 · 3:04 PM MST")
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
