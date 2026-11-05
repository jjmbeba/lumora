package booking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type bookingFingerprintInput struct {
	ScreeningID         int    `json:"screening_id"`
	RequestedSeatIDs    []int  `json:"requested_seat_ids"`
	CustomerEmail       string `json:"customer_email"`
	CustomerPhoneNumber string `json:"customer_phone_number"`
}

type Service struct {
	store Store
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("Booking store is required")
	}

	return &Service{
		store: store,
	}, nil
}

func (s *Service) Create(ctx context.Context, request CreateBookingRequest) (Booking, error) {
	return s.create(ctx, request, "")
}

func (s *Service) CreateWithFingerprint(ctx context.Context, request CreateBookingRequest, requestFingerprint string) (Booking, error) {
	if strings.TrimSpace(requestFingerprint) == "" {
		return Booking{}, fmt.Errorf("%w: request fingerprint is required", ErrInvalidRequest)
	}

	return s.create(ctx, request, requestFingerprint)
}

func (s *Service) GetDetails(ctx context.Context, bookingReference string) (BookingDetails, error) {
	bookingReference = strings.TrimSpace(bookingReference)
	if bookingReference == "" {
		return BookingDetails{}, fmt.Errorf("%w: booking reference is required", ErrInvalidRequest)
	}

	return s.store.GetDetails(ctx, bookingReference)
}

func (s *Service) create(ctx context.Context, request CreateBookingRequest, requestFingerprint string) (Booking, error) {
	if request.ScreeningID < 1 {
		return Booking{}, fmt.Errorf("%w: Screening ID must be a positive number", ErrInvalidRequest)
	}

	if len(request.RequestedSeatIDs) < 1 {
		return Booking{}, fmt.Errorf("%w: Booking request must contain at least one seat", ErrInvalidRequest)
	}

	requestedSeatsSet := make(map[int]struct{})

	for _, seat := range request.RequestedSeatIDs {
		if seat <= 0 {
			return Booking{}, fmt.Errorf("%w: Seat ID must be a positive integer", ErrInvalidRequest)
		}

		if _, exists := requestedSeatsSet[seat]; exists {
			return Booking{}, fmt.Errorf("%w: Duplicate seats found in request: %d", ErrInvalidRequest, seat)
		}

		requestedSeatsSet[seat] = struct{}{}
	}

	trimmedEmail, trimmedPhone := strings.TrimSpace(request.Customer.Email), strings.TrimSpace(request.Customer.PhoneNumber)
	trimmedIdempotencyKey := strings.TrimSpace(request.IdempotencyKey)

	if trimmedEmail == "" || trimmedPhone == "" {
		return Booking{}, fmt.Errorf("%w: Customer email and phone number are required", ErrInvalidRequest)
	}

	if trimmedIdempotencyKey == "" {
		return Booking{}, fmt.Errorf("%w: Idempotency key is required", ErrInvalidRequest)
	}

	validatedCustomer := CustomerDetails{
		Email:       trimmedEmail,
		PhoneNumber: trimmedPhone,
	}

	validatedRequest := CreateBookingRequest{
		ScreeningID:      request.ScreeningID,
		RequestedSeatIDs: request.RequestedSeatIDs,
		Customer:         validatedCustomer,
		IdempotencyKey:   trimmedIdempotencyKey,
	}

	var seatIDs []int
	if requestFingerprint == "" {
		var err error
		requestFingerprint, seatIDs, err = createBookingFingerprint(validatedRequest)
		if err != nil {
			return Booking{}, err
		}
	} else {
		seatIDs = append([]int(nil), validatedRequest.RequestedSeatIDs...)
		sort.Ints(seatIDs)
	}

	params := CreateBookingParams{
		ScreeningID:        validatedRequest.ScreeningID,
		RequestedSeatIDs:   seatIDs,
		IdempotencyKey:     validatedRequest.IdempotencyKey,
		RequestFingerprint: requestFingerprint,
		Customer:           validatedRequest.Customer,
	}

	return s.store.Create(ctx, params)
}

func createBookingFingerprint(request CreateBookingRequest) (string, []int, error) {
	seatIDs := make([]int, len(request.RequestedSeatIDs))
	copy(seatIDs, request.RequestedSeatIDs)
	sort.Ints(seatIDs)

	input := bookingFingerprintInput{
		ScreeningID:         request.ScreeningID,
		RequestedSeatIDs:    seatIDs,
		CustomerEmail:       request.Customer.Email,
		CustomerPhoneNumber: request.Customer.PhoneNumber,
	}

	canonical, err := json.Marshal(input)
	if err != nil {
		return "", make([]int, 0), fmt.Errorf("marshal booking fingerprint input: %w", err)
	}

	sum := sha256.Sum256(canonical)

	return hex.EncodeToString(sum[:]), seatIDs, nil
}
