package staff

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultStaffAccountPageSize = 20
	maxStaffAccountPageSize     = 100
	maxStaffAccountSearchRunes  = 100
)

type ManagementStore interface {
	ListAccounts(ctx context.Context, search string, limit, offset int64) ([]Account, error)
	CreateInvitation(ctx context.Context, managerID int, invitation StaffInvitation) (Account, error)
	LoadInvitedAccount(ctx context.Context, accountID int) (Account, error)
	ResendInvitation(ctx context.Context, managerID, accountID int, invitation StaffInvitation) (Account, error)
	DeactivateAccount(ctx context.Context, managerID, accountID int) error
}

type ManagementService struct {
	store  ManagementStore
	cipher *InvitationTokenCipher
}

func NewManagementService(store ManagementStore, cipher *InvitationTokenCipher) (*ManagementService, error) {
	if store == nil || cipher == nil {
		return nil, errors.New("staff management requires a store and invitation token cipher")
	}
	return &ManagementService{store: store, cipher: cipher}, nil
}

func (s *ManagementService) DeactivateAccount(ctx context.Context, managerID, accountID int) error {
	if managerID <= 0 || accountID <= 0 {
		return ErrInvalidAccountID
	}

	return s.store.DeactivateAccount(ctx, managerID, accountID)
}

func (s *ManagementService) ListAccounts(ctx context.Context, request ListAccountsRequest) (AccountPage, error) {
	if request.Page == 0 {
		request.Page = 1
	}

	if request.PageSize == 0 {
		request.PageSize = defaultStaffAccountPageSize
	}

	request.Search = strings.TrimSpace(request.Search)

	if request.Page < 1 || request.PageSize < 1 || request.PageSize > maxStaffAccountPageSize ||
		utf8.RuneCountInString(request.Search) > maxStaffAccountSearchRunes {
		return AccountPage{}, ErrInvalidAccountList
	}

	maxOffset := (1 << 31) - 1 - int64(request.PageSize+1)
	if int64(request.Page-1) > maxOffset/int64(request.PageSize) {
		return AccountPage{}, ErrInvalidAccountList
	}

	offset := int64(request.Page-1) * int64(request.PageSize)
	accounts, err := s.store.ListAccounts(ctx, request.Search, int64(request.PageSize+1), offset)

	if err != nil {
		return AccountPage{}, err
	}

	hasMore := len(accounts) > request.PageSize
	if hasMore {
		accounts = accounts[:request.PageSize]
	}

	return AccountPage{Accounts: accounts, Page: request.Page, PageSize: request.PageSize, HasMore: hasMore}, nil
}

type CreateInvitationRequest struct {
	Email       string
	DisplayName string
	Role        Role
}

type StaffInvitation struct {
	Email           string
	DisplayName     string
	Role            Role
	TokenDigest     []byte
	TokenCiphertext []byte
	TokenKeyVersion int16
	ExpiresAt       time.Time
}

func (s *ManagementService) CreateInvitation(ctx context.Context, managerID int, request CreateInvitationRequest) (Account, error) {
	if managerID <= 0 || int64(managerID) > 1<<31-1 {
		return Account{}, ErrInvalidAccountID
	}

	invit, err := s.newInvitation(request)
	if err != nil {
		return Account{}, err
	}

	return s.store.CreateInvitation(ctx, managerID, invit)
}

func (s *ManagementService) ResendInvitation(ctx context.Context, managerID, accountID int) (Account, error) {
	if managerID <= 0 || accountID <= 0 || int64(managerID) > 1<<31-1 || int64(accountID) > 1<<31-1 {
		return Account{}, ErrInvalidAccountID
	}

	account, err := s.store.LoadInvitedAccount(ctx, accountID)
	if err != nil {
		return Account{}, err
	}

	invit, err := s.newInvitation(CreateInvitationRequest{
		Email: account.Email, DisplayName: account.DisplayName, Role: account.Role,
	})
	if err != nil {
		return Account{}, err
	}

	return s.store.ResendInvitation(ctx, managerID, accountID, invit)
}

func (s *ManagementService) newInvitation(request CreateInvitationRequest) (StaffInvitation, error) {
	email := strings.ToLower(strings.TrimSpace(request.Email))
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil || parsedEmail.Address != email {
		return StaffInvitation{}, ErrInvalidStaffInvitation
	}

	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" || (request.Role != RoleManager && request.Role != RoleScanner) {
		return StaffInvitation{}, ErrInvalidStaffInvitation
	}

	token, digest, err := newOpaqueToken()
	if err != nil {
		return StaffInvitation{}, fmt.Errorf("generate staff invitation token: %w", err)
	}

	ciphertext, keyVersion, err := s.cipher.Encrypt(email, token)
	if err != nil {
		return StaffInvitation{}, fmt.Errorf("encrypt staff invitation token: %w", err)
	}

	return StaffInvitation{
		Email: email, DisplayName: displayName, Role: request.Role,
		TokenDigest: digest, TokenCiphertext: ciphertext, TokenKeyVersion: keyVersion,
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}, nil
}
