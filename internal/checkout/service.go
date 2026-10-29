package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/payment"
)

type StartRequest struct {
	Booking       booking.CreateBookingRequest
	PaymentMethod payment.PaymentMethod
}

type StartResult struct {
	Booking        booking.Booking
	Initialization payment.Initialization
}

type BookingCreator interface {
	CreateWithFingerprint(
		ctx context.Context,
		request booking.CreateBookingRequest,
		requestFingerprint string,
	) (booking.Booking, error)
}

type Starter interface {
	Start(ctx context.Context, request StartRequest) (StartResult, error)
}

type InitializationReader interface {
	GetLatestForBooking(ctx context.Context, bookingReference string) (payment.Initialization, error)
}

type InitializationStore interface {
	payment.InitializationCreator
	InitializationReader
}

type Service struct {
	bookings        BookingCreator
	initializations InitializationStore
}

func NewService(bookings BookingCreator, initializations InitializationStore) (*Service, error) {
	if bookings == nil {
		return nil, ErrBookingCreatorRequired
	}
	if initializations == nil {
		return nil, ErrInitializationStoreRequired
	}

	return &Service{bookings: bookings, initializations: initializations}, nil
}

func (s *Service) Start(ctx context.Context, request StartRequest) (StartResult, error) {
	methodName, ok := paymentMethodName(request.PaymentMethod)
	if !ok {
		return StartResult{}, ErrInvalidPaymentMethod
	}

	bookingRequest := request.Booking
	bookingRequest.IdempotencyKey = strings.TrimSpace(bookingRequest.IdempotencyKey)
	bookingRequest.Customer.Email = strings.TrimSpace(bookingRequest.Customer.Email)
	bookingRequest.Customer.PhoneNumber = strings.TrimSpace(bookingRequest.Customer.PhoneNumber)
	bookingRequest.RequestedSeatIDs = append([]int(nil), bookingRequest.RequestedSeatIDs...)
	sort.Ints(bookingRequest.RequestedSeatIDs)

	fingerprint, err := requestFingerprint(bookingRequest, methodName)
	if err != nil {
		return StartResult{}, fmt.Errorf("fingerprint checkout request: %w", err)
	}

	createdBooking, err := s.bookings.CreateWithFingerprint(ctx, bookingRequest, fingerprint)
	if err != nil {
		return StartResult{}, err
	}

	initialization, err := s.initializations.CreateOrReplayInitialization(ctx, payment.CreateInitializationRequest{
		BookingReference:   createdBooking.Reference,
		IdempotencyKey:     bookingRequest.IdempotencyKey,
		RequestFingerprint: fingerprint,
		PaymentMethod:      request.PaymentMethod,
	})
	if err != nil {
		return StartResult{}, err
	}

	return StartResult{Booking: createdBooking, Initialization: initialization}, nil
}

func (s *Service) GetLatestInitialization(ctx context.Context, bookingReference string) (payment.Initialization, error) {
	bookingReference = strings.TrimSpace(bookingReference)
	if bookingReference == "" {
		return payment.Initialization{}, payment.ErrInvalidInitializationRequest
	}

	return s.initializations.GetLatestForBooking(ctx, bookingReference)
}

type fingerprintInput struct {
	ScreeningID         int    `json:"screening_id"`
	RequestedSeatIDs    []int  `json:"requested_seat_ids"`
	CustomerEmail       string `json:"customer_email"`
	CustomerPhoneNumber string `json:"customer_phone_number"`
	PaymentMethod       string `json:"payment_method"`
}

func requestFingerprint(request booking.CreateBookingRequest, methodName string) (string, error) {
	canonical, err := json.Marshal(fingerprintInput{
		ScreeningID:         request.ScreeningID,
		RequestedSeatIDs:    request.RequestedSeatIDs,
		CustomerEmail:       request.Customer.Email,
		CustomerPhoneNumber: request.Customer.PhoneNumber,
		PaymentMethod:       methodName,
	})
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func paymentMethodName(method payment.PaymentMethod) (string, bool) {
	switch method {
	case payment.PaymentMethodCard:
		return "CARD", true
	case payment.PaymentMethodMpesa:
		return "MPESA", true
	default:
		return "", false
	}
}
