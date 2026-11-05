package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"example.com/cinema-tickets-backend/internal/staff"
)

type ManagementHandler struct {
	service *staff.ManagementService
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
	case errors.Is(err, staff.ErrManagerUnavailable):
		http.Error(w, "staff manager is unavailable", http.StatusForbidden)
	default:
		http.Error(w, "unable to deactivate staff account", http.StatusInternalServerError)
	}
}

func RegisterManagementRoutes(mux *http.ServeMux, authHandler *AuthHandler, handler *ManagementHandler) error {
	if mux == nil || authHandler == nil || handler == nil {
		return errors.New("staff management routes require a mux, auth handler, and management handler")
	}

	mux.Handle("POST /staff/accounts/{id}/deactivation", authHandler.RequireRole(staff.RoleManager, http.HandlerFunc(handler.DeactivateAccount)))
	return nil
}
