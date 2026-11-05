package staff

import (
	"context"
	"errors"
)

type ManagementStore interface {
	DeactivateAccount(ctx context.Context, managerID, accountID int) error
}

type ManagementService struct {
	store ManagementStore
}

func NewManagementService(store ManagementStore) (*ManagementService, error) {
	if store == nil {
		return nil, errors.New("staff management store is required")
	}
	return &ManagementService{store: store}, nil
}

func (s *ManagementService) DeactivateAccount(ctx context.Context, managerID, accountID int) error {
	if managerID <= 0 || accountID <= 0 {
		return ErrInvalidAccountID
	}

	return s.store.DeactivateAccount(ctx, managerID, accountID)
}
