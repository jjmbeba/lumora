package checkout

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/payment"
)

func TestStartCanonicalizesRequestAndCreatesInitialization(t *testing.T) {
	bookingStore := &bookingCreatorFake{result: booking.Booking{Reference: "bk_123"}}
	initializationStore := &initializationCreatorFake{result: payment.Initialization{
		ID:                81,
		Status:            payment.InitializationStatusQueued,
		PaymentMethod:     payment.PaymentMethodCard,
		PaystackReference: "cinema_provider_ref",
	}}
	service, err := NewService(bookingStore, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	got, err := service.Start(context.Background(), StartRequest{
		Booking: booking.CreateBookingRequest{
			ScreeningID:      20,
			RequestedSeatIDs: []int{8, 3},
			Customer: booking.CustomerDetails{
				Email:       " customer@example.test ",
				PhoneNumber: " +254700000000 ",
			},
			IdempotencyKey: " checkout-key ",
		},
		PaymentMethod: payment.PaymentMethodCard,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got.Booking.Reference != "bk_123" || got.Initialization.ID != 81 {
		t.Errorf("Start() result = %+v, want booking bk_123 and initialization 81", got)
	}
	if bookingStore.calls != 1 {
		t.Fatalf("booking create calls = %d, want 1", bookingStore.calls)
	}
	if bookingStore.request.IdempotencyKey != "checkout-key" || bookingStore.request.Customer.Email != "customer@example.test" || bookingStore.request.Customer.PhoneNumber != "+254700000000" {
		t.Errorf("booking request was not trimmed: %+v", bookingStore.request)
	}
	if !reflect.DeepEqual(bookingStore.request.RequestedSeatIDs, []int{3, 8}) {
		t.Errorf("booking seat IDs = %v, want sorted [3 8]", bookingStore.request.RequestedSeatIDs)
	}
	if bookingStore.fingerprint == "" || initializationStore.calls != 1 {
		t.Fatalf("fingerprint=%q, initialization calls=%d; want non-empty fingerprint and one call", bookingStore.fingerprint, initializationStore.calls)
	}
	if initializationStore.request.BookingReference != "bk_123" || initializationStore.request.IdempotencyKey != "checkout-key" ||
		initializationStore.request.RequestFingerprint != bookingStore.fingerprint || initializationStore.request.PaymentMethod != payment.PaymentMethodCard {
		t.Errorf("initialization request = %+v, does not match canonical booking request", initializationStore.request)
	}
}

func TestStartIncludesPaymentMethodInRequestFingerprint(t *testing.T) {
	bookingStore := &bookingCreatorFake{result: booking.Booking{Reference: "bk_123"}}
	initializationStore := &initializationCreatorFake{}
	service, err := NewService(bookingStore, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	request := StartRequest{
		Booking: booking.CreateBookingRequest{
			ScreeningID:      20,
			RequestedSeatIDs: []int{3, 8},
			Customer:         booking.CustomerDetails{Email: "customer@example.test", PhoneNumber: "+254700000000"},
			IdempotencyKey:   "checkout-key",
		},
		PaymentMethod: payment.PaymentMethodCard,
	}
	if _, err := service.Start(context.Background(), request); err != nil {
		t.Fatalf("Start(CARD) error = %v", err)
	}
	cardFingerprint := bookingStore.fingerprint

	request.PaymentMethod = payment.PaymentMethodMpesa
	if _, err := service.Start(context.Background(), request); err != nil {
		t.Fatalf("Start(MPESA) error = %v", err)
	}
	if bookingStore.fingerprint == cardFingerprint {
		t.Error("changing payment method did not change the request fingerprint")
	}
	if initializationStore.requests[1].PaymentMethod != payment.PaymentMethodMpesa {
		t.Errorf("second initialization method = %v, want MPESA", initializationStore.requests[1].PaymentMethod)
	}
}

func TestStartStopsWhenBookingCreationFails(t *testing.T) {
	bookingFailure := booking.ErrSeatsUnavailable
	bookingStore := &bookingCreatorFake{err: bookingFailure}
	initializationStore := &initializationCreatorFake{}
	service, err := NewService(bookingStore, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.Start(context.Background(), validStartRequest(payment.PaymentMethodCard))
	if !errors.Is(err, bookingFailure) {
		t.Fatalf("Start() error = %v, want booking error %v", err, bookingFailure)
	}
	if initializationStore.calls != 0 {
		t.Errorf("initialization calls = %d, want 0 after booking failure", initializationStore.calls)
	}
}

func TestStartReturnsInitializationFailureAfterBookingCreation(t *testing.T) {
	initializationFailure := payment.ErrBookingNotEligibleForPayment
	bookingStore := &bookingCreatorFake{result: booking.Booking{Reference: "bk_123"}}
	initializationStore := &initializationCreatorFake{err: initializationFailure}
	service, err := NewService(bookingStore, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.Start(context.Background(), validStartRequest(payment.PaymentMethodCard))
	if !errors.Is(err, initializationFailure) {
		t.Fatalf("Start() error = %v, want initialization error %v", err, initializationFailure)
	}
	if bookingStore.calls != 1 || initializationStore.calls != 1 {
		t.Errorf("booking/initialization calls = %d/%d, want 1/1", bookingStore.calls, initializationStore.calls)
	}
}

func TestStartRejectsInvalidPaymentMethodBeforePersistence(t *testing.T) {
	bookingStore := &bookingCreatorFake{}
	initializationStore := &initializationCreatorFake{}
	service, err := NewService(bookingStore, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.Start(context.Background(), validStartRequest(payment.PaymentMethodInvalid))
	if !errors.Is(err, ErrInvalidPaymentMethod) {
		t.Fatalf("Start() error = %v, want ErrInvalidPaymentMethod", err)
	}
	if bookingStore.calls != 0 || initializationStore.calls != 0 {
		t.Errorf("booking/initialization calls = %d/%d, want 0/0", bookingStore.calls, initializationStore.calls)
	}
}

func TestGetLatestInitializationTrimsBookingReference(t *testing.T) {
	want := payment.Initialization{
		ID:                92,
		Status:            payment.InitializationStatusReady,
		PaymentMethod:     payment.PaymentMethodMpesa,
		PaystackReference: "provider-ref",
		AuthorizationURL:  "https://checkout.example.test/pay",
	}
	initializationStore := &initializationCreatorFake{readResult: want}
	service, err := NewService(&bookingCreatorFake{}, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	got, err := service.GetLatestInitialization(context.Background(), " bk-123 ")
	if err != nil {
		t.Fatalf("GetLatestInitialization() error = %v", err)
	}
	if got != want {
		t.Errorf("GetLatestInitialization() = %+v, want %+v", got, want)
	}
	if initializationStore.readCalls != 1 || initializationStore.readReference != "bk-123" {
		t.Errorf("reader calls/reference = %d/%q, want 1/bk-123", initializationStore.readCalls, initializationStore.readReference)
	}
}

func TestGetLatestInitializationRejectsEmptyBookingReference(t *testing.T) {
	initializationStore := &initializationCreatorFake{}
	service, err := NewService(&bookingCreatorFake{}, initializationStore)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if _, err := service.GetLatestInitialization(context.Background(), "  "); !errors.Is(err, payment.ErrInvalidInitializationRequest) {
		t.Fatalf("GetLatestInitialization() error = %v, want invalid initialization request", err)
	}
	if initializationStore.readCalls != 0 {
		t.Errorf("reader calls = %d, want 0 for empty booking reference", initializationStore.readCalls)
	}
}

type bookingCreatorFake struct {
	result      booking.Booking
	err         error
	calls       int
	request     booking.CreateBookingRequest
	fingerprint string
}

func (f *bookingCreatorFake) CreateWithFingerprint(_ context.Context, request booking.CreateBookingRequest, fingerprint string) (booking.Booking, error) {
	f.calls++
	f.request = request
	f.fingerprint = fingerprint
	return f.result, f.err
}

type initializationCreatorFake struct {
	result        payment.Initialization
	err           error
	calls         int
	request       payment.CreateInitializationRequest
	requests      []payment.CreateInitializationRequest
	readResult    payment.Initialization
	readErr       error
	readCalls     int
	readReference string
}

func (f *initializationCreatorFake) CreateOrReplayInitialization(_ context.Context, request payment.CreateInitializationRequest) (payment.Initialization, error) {
	f.calls++
	f.request = request
	f.requests = append(f.requests, request)
	return f.result, f.err
}

func (f *initializationCreatorFake) GetLatestForBooking(_ context.Context, bookingReference string) (payment.Initialization, error) {
	f.readCalls++
	f.readReference = bookingReference
	return f.readResult, f.readErr
}

func validStartRequest(method payment.PaymentMethod) StartRequest {
	return StartRequest{
		Booking: booking.CreateBookingRequest{
			ScreeningID:      20,
			RequestedSeatIDs: []int{3, 8},
			Customer:         booking.CustomerDetails{Email: "customer@example.test", PhoneNumber: "+254700000000"},
			IdempotencyKey:   "checkout-key",
		},
		PaymentMethod: method,
	}
}
