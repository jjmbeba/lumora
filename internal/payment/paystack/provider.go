package paystack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.com/cinema-tickets-backend/internal/payment"
)

const initializeURL = "https://api.paystack.co/transaction/initialize"
const verifyBaseURL = "https://api.paystack.co/transaction/verify/"

type PaystackProvider struct {
	secretKey string
	client    *http.Client
}

type initializeRequest struct {
	Amount    string `json:"amount"`
	Email     string `json:"email"`
	Currency  string `json:"currency,omitempty"`
	Reference string `json:"reference"`
}

type initializeResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		AuthorizationURL string `json:"authorization_url"`
		AccessCode       string `json:"access_code"`
		Reference        string `json:"reference"`
	} `json:"data"`
}

type initializeErrorResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type verifyErrorResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
}

type verificationResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		ID        int64     `json:"id"`
		Status    string    `json:"status"`
		Reference string    `json:"reference"`
		Amount    int64     `json:"amount"`
		PaidAt    time.Time `json:"paid_at"`
		CreatedAt time.Time `json:"created_at"`
		Channel   string    `json:"channel"`
		Currency  string    `json:"currency"`
		Customer  struct {
			ID           int64  `json:"id"`
			FirstName    string `json:"first_name"`
			LastName     string `json:"last_name"`
			Email        string `json:"email"`
			CustomerCode string `json:"customer_code"`
			Phone        string `json:"phone"`
		} `json:"customer"`
	} `json:"data"`
}

func NewPaystackProvider(secretKey string) (*PaystackProvider, error) {
	if strings.TrimSpace(secretKey) == "" {
		return nil, fmt.Errorf("Paystack secret key must not be empty")
	}

	return &PaystackProvider{
		secretKey: secretKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

func (p *PaystackProvider) Initialize(ctx context.Context, request payment.InitRequest) (payment.InitResult, error) {
	paystackRequest := initializeRequest{
		Amount:    strconv.FormatInt(request.AmountMinor, 10),
		Email:     request.Email,
		Currency:  request.Currency,
		Reference: request.Reference,
	}

	payload, err := json.Marshal(paystackRequest)
	if err != nil {
		return payment.InitResult{}, fmt.Errorf("marshal Paystack initialize request: %w", err)
	}

	resp, err := p.do(ctx, http.MethodPost, initializeURL, payload)
	if err != nil {
		return payment.InitResult{}, fmt.Errorf("initialize Paystack transaction: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiError initializeErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiError); err != nil {
			return payment.InitResult{}, fmt.Errorf("decode Paystack initialize error response (HTTP %d): %w", resp.StatusCode, err)
		}

		if resp.StatusCode == http.StatusBadRequest && !apiError.Status && apiError.Type == "validation_error" {
			return payment.InitResult{
				Status:  payment.ResultStatusRejected,
				Message: apiError.Message,
			}, nil
		}

		return payment.InitResult{}, fmt.Errorf(
			"Paystack initialize returned HTTP %d (type=%q code=%q): %s",
			resp.StatusCode,
			apiError.Type,
			apiError.Code,
			apiError.Message,
		)
	}

	var response initializeResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return payment.InitResult{}, fmt.Errorf("decode Paystack initialize response: %w", err)
	}
	if !response.Status {
		return payment.InitResult{}, fmt.Errorf("Paystack initialize was not successful: %s", response.Message)
	}
	if response.Data.AuthorizationURL == "" || response.Data.Reference == "" {
		return payment.InitResult{}, fmt.Errorf("Paystack initialize response is missing checkout URL or reference")
	}

	return payment.InitResult{
		Status:           payment.ResultStatusCheckoutCreated,
		Message:          response.Message,
		AuthorizationURL: response.Data.AuthorizationURL,
		Reference:        response.Data.Reference,
		AccessCode:       response.Data.AccessCode,
	}, nil
}

func (p *PaystackProvider) do(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return nil, fmt.Errorf("create Paystack API request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.secretKey)
	req.Header.Set("Accept", "application/json")

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Paystack API request: %w", err)
	}

	return resp, nil
}

func (p *PaystackProvider) Verify(ctx context.Context, reference string) (payment.Verification, error) {
	if reference == "" {
		return payment.Verification{}, fmt.Errorf("Reference must not be empty")
	}

	resp, err := p.do(ctx, http.MethodGet, verifyBaseURL+reference, nil)
	if err != nil {
		return payment.Verification{}, fmt.Errorf("verify Paystack transaction: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiError verifyErrorResponse

		if err := json.NewDecoder(resp.Body).Decode(&apiError); err != nil {
			return payment.Verification{}, fmt.Errorf("decode Paystack verify error response: %w", err)
		}

		return payment.Verification{}, fmt.Errorf(
			"Paystack verify returned HTTP %d: %s",
			resp.StatusCode,
			apiError.Message,
		)
	}

	var response verificationResponse

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return payment.Verification{}, fmt.Errorf("decode Paystack verify response: %w", err)
	}

	if !response.Status {
		return payment.Verification{}, fmt.Errorf("Paystack verify was not successful: %s", response.Message)
	}
	if response.Data.Status == "" || response.Data.Reference == "" {
		return payment.Verification{}, fmt.Errorf("Paystack verify response is missing status or reference")
	}

	if response.Data.Reference != reference {
		return payment.Verification{}, fmt.Errorf("Paystack verification reference and reference in request do not match")
	}

	switch response.Data.Status {
	case "success":
		return payment.Verification{
			VerificationStatus: payment.VerificationStatusPaid,
			Message:            response.Message,
			TransactionID:      response.Data.ID,
			Reference:          response.Data.Reference,
			AmountMinor:        response.Data.Amount,
			PaidAt:             response.Data.PaidAt,
			Currency:           response.Data.Currency,
		}, nil

	case "failed":
		return payment.Verification{
			VerificationStatus: payment.VerificationStatusFailed,
			Message:            response.Message,
		}, nil

	case "abandoned":
		return payment.Verification{
			// Paystack's abandoned status means the customer has not completed
			// checkout yet. It can still become successful later, so keep
			// reconciling while the local booking hold remains valid.
			VerificationStatus: payment.VerificationStatusPending,
			Message:            response.Message,
		}, nil

	case "ongoing", "pending", "processing", "queued":
		return payment.Verification{
			VerificationStatus: payment.VerificationStatusPending,
			Message:            response.Message,
		}, nil

	case "reversed":
		return payment.Verification{
			VerificationStatus: payment.VerificationStatusReversed,
			Message:            response.Message,
		}, nil

	default:
		return payment.Verification{
			VerificationStatus: payment.VerificationStatusInvalid,
			Message:            response.Message,
		}, nil
	}
}
