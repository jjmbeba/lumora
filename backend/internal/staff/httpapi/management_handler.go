package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/staff"
)

type ManagementHandler struct {
	service *staff.ManagementService
}

type createStaffInvitationRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type createStaffInvitationResponse struct {
	Account identityResponse `json:"account"`
	Status  string           `json:"status"`
}

type staffAccountListResponse struct {
	Accounts []staffAccountResponse `json:"accounts"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
	HasMore  bool                   `json:"has_more"`
}

type staffAccountResponse struct {
	ID          int       `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func NewManagementHandler(service *staff.ManagementService) (*ManagementHandler, error) {
	if service == nil {
		return nil, errors.New("staff management service is required")
	}

	return &ManagementHandler{service: service}, nil
}

func (h *ManagementHandler) DeactivateAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accountID, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil || accountID <= 0 {
		http.Error(w, "invalid staff account ID", http.StatusBadRequest)
		return
	}

	identity, ok := IdentityFromContext(r)
	if !ok {
		http.Error(w, "staff authentication required", http.StatusUnauthorized)
		return
	}

	err = h.service.DeactivateAccount(r.Context(), identity.AccountID, int(accountID))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, staff.ErrInvalidAccountID):
		http.Error(w, "invalid staff account ID", http.StatusBadRequest)
	case errors.Is(err, staff.ErrStaffAccountNotFound):
		http.Error(w, "staff account not found", http.StatusNotFound)
	case errors.Is(err, staff.ErrLastActiveManager):
		http.Error(w, "cannot deactivate the last active manager", http.StatusConflict)
	case errors.Is(err, staff.ErrCannotDeactivateSelf):
		http.Error(w, "cannot deactivate your own staff account", http.StatusConflict)
	case errors.Is(err, staff.ErrManagerUnavailable):
		http.Error(w, "staff manager is unavailable", http.StatusForbidden)
	default:
		http.Error(w, "unable to deactivate staff account", http.StatusInternalServerError)
	}
}

func (h *ManagementHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	page, err := parseOptionalQueryInt(query.Get("page"), 1)
	if err != nil {
		http.Error(w, "invalid page", http.StatusBadRequest)
		return
	}

	pageSize, err := parseOptionalQueryInt(query.Get("page_size"), 0)
	if err != nil {
		http.Error(w, "invalid page_size", http.StatusBadRequest)
		return
	}

	result, err := h.service.ListAccounts(r.Context(), staff.ListAccountsRequest{
		Search: query.Get("search"), Page: page, PageSize: pageSize,
	})

	if errors.Is(err, staff.ErrInvalidAccountList) {
		http.Error(w, "invalid staff account list options", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "unable to list staff accounts", http.StatusInternalServerError)
		return
	}

	accounts := make([]staffAccountResponse, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		accounts = append(accounts, staffAccountResponse{
			ID: account.ID, Email: account.Email, DisplayName: account.DisplayName,
			Role: string(account.Role), Status: string(account.Status), CreatedAt: account.CreatedAt,
		})
	}

	writeAuthJSON(w, http.StatusOK, staffAccountListResponse{
		Accounts: accounts, Page: result.Page, PageSize: result.PageSize, HasMore: result.HasMore,
	})
}

func parseOptionalQueryInt(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}

	return value, nil
}

func (h *ManagementHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request createStaffInvitationRequest
	if !decodeAuthRequest(w, r, &request) {
		return
	}

	identity, ok := IdentityFromContext(r)
	if !ok {
		http.Error(w, "staff authentication required", http.StatusUnauthorized)
		return
	}

	account, err := h.service.CreateInvitation(r.Context(), identity.AccountID, staff.CreateInvitationRequest{
		Email: request.Email, DisplayName: request.DisplayName, Role: staff.Role(request.Role),
	})

	switch {
	case err == nil:
		writeAuthJSON(w, http.StatusAccepted, createStaffInvitationResponse{
			Account: identityResponse{
				ID: account.ID, Email: account.Email, DisplayName: account.DisplayName, Role: string(account.Role),
			},
			Status: string(account.Status),
		})
	case errors.Is(err, staff.ErrInvalidStaffInvitation):
		http.Error(w, "invalid staff invitation details", http.StatusBadRequest)
	case errors.Is(err, staff.ErrStaffEmailAlreadyExists):
		http.Error(w, "a staff account already uses that email", http.StatusConflict)
	case errors.Is(err, staff.ErrManagerUnavailable):
		http.Error(w, "staff manager is unavailable", http.StatusForbidden)
	default:
		http.Error(w, "unable to create staff invitation", http.StatusInternalServerError)
	}
}

func (h *ManagementHandler) ResendInvitation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accountID, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil || accountID <= 0 {
		http.Error(w, "invalid staff account ID", http.StatusBadRequest)
		return
	}

	identity, ok := IdentityFromContext(r)
	if !ok {
		http.Error(w, "staff authentication required", http.StatusUnauthorized)
		return
	}

	account, err := h.service.ResendInvitation(r.Context(), identity.AccountID, int(accountID))
	switch {
	case err == nil:
		writeAuthJSON(w, http.StatusAccepted, createStaffInvitationResponse{
			Account: identityResponse{ID: account.ID, Email: account.Email, DisplayName: account.DisplayName, Role: string(account.Role)},
			Status:  string(account.Status),
		})
	case errors.Is(err, staff.ErrInvalidAccountID):
		http.Error(w, "invalid staff account ID", http.StatusBadRequest)
	case errors.Is(err, staff.ErrStaffAccountNotFound), errors.Is(err, staff.ErrAccountUnavailable):
		http.Error(w, "invited staff account not found", http.StatusNotFound)
	case errors.Is(err, staff.ErrManagerUnavailable):
		http.Error(w, "staff manager is unavailable", http.StatusForbidden)
	default:
		http.Error(w, "unable to resend staff invitation", http.StatusInternalServerError)
	}
}

func RegisterManagementRoutes(mux *http.ServeMux, authHandler *AuthHandler, handler *ManagementHandler) error {
	if mux == nil || authHandler == nil || handler == nil {
		return errors.New("staff management routes require a mux, auth handler, and management handler")
	}

	mux.Handle("POST /api/v1/staff/accounts/{id}/deactivation", authHandler.RequireCSRF(authHandler.RequireRole(staff.RoleManager, http.HandlerFunc(handler.DeactivateAccount))))
	mux.Handle("POST /api/v1/staff/accounts/{id}/invitation", authHandler.RequireCSRF(authHandler.RequireRole(staff.RoleManager, http.HandlerFunc(handler.ResendInvitation))))
	mux.Handle("POST /api/v1/staff/accounts", authHandler.RequireCSRF(authHandler.RequireRole(staff.RoleManager, http.HandlerFunc(handler.CreateInvitation))))
	mux.Handle("GET /api/v1/staff/accounts", authHandler.RequireRole(staff.RoleManager, http.HandlerFunc(handler.ListAccounts)))
	return nil
}
