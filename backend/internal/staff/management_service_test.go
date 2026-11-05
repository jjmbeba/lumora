package staff

import (
	"context"
	"errors"
	"testing"
)

func TestManagementServiceListAccountsDefaultsAndHasMore(t *testing.T) {
	store := &managementListStore{accounts: make([]Account, defaultStaffAccountPageSize+1)}
	for i := range store.accounts {
		store.accounts[i] = Account{ID: i + 1}
	}
	service := newTestManagementService(t, store)

	got, err := service.ListAccounts(context.Background(), ListAccountsRequest{})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if store.search != "" || store.limit != defaultStaffAccountPageSize+1 || store.offset != 0 {
		t.Fatalf("store query = search %q, limit %d, offset %d; want empty, %d, 0", store.search, store.limit, store.offset, defaultStaffAccountPageSize+1)
	}
	if got.Page != 1 || got.PageSize != defaultStaffAccountPageSize || !got.HasMore {
		t.Fatalf("page metadata = %+v, want page 1, size %d, has_more true", got, defaultStaffAccountPageSize)
	}
	if len(got.Accounts) != defaultStaffAccountPageSize {
		t.Fatalf("returned %d accounts, want %d", len(got.Accounts), defaultStaffAccountPageSize)
	}
}

func TestManagementServiceListAccountsCalculatesPageOffsetAndTrimsSearch(t *testing.T) {
	store := &managementListStore{accounts: []Account{{ID: 3}, {ID: 4}}}
	service := newTestManagementService(t, store)

	got, err := service.ListAccounts(context.Background(), ListAccountsRequest{
		Search: "  manager  ", Page: 3, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if store.search != "manager" || store.limit != 3 || store.offset != 4 {
		t.Fatalf("store query = search %q, limit %d, offset %d; want manager, 3, 4", store.search, store.limit, store.offset)
	}
	if got.Page != 3 || got.PageSize != 2 || got.HasMore || len(got.Accounts) != 2 {
		t.Fatalf("page result = %+v, want page 3, size 2, two accounts, has_more false", got)
	}
}

func TestManagementServiceListAccountsRejectsInvalidOptions(t *testing.T) {
	service := newTestManagementService(t, &managementListStore{})
	tests := []struct {
		name    string
		request ListAccountsRequest
	}{
		{name: "negative page", request: ListAccountsRequest{Page: -1}},
		{name: "negative page size", request: ListAccountsRequest{PageSize: -1}},
		{name: "page size too large", request: ListAccountsRequest{PageSize: maxStaffAccountPageSize + 1}},
		{name: "search too long", request: ListAccountsRequest{Search: string(make([]byte, maxStaffAccountSearchRunes+1))}},
		{name: "offset overflow", request: ListAccountsRequest{Page: int(^uint(0) >> 1), PageSize: maxStaffAccountPageSize}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.ListAccounts(context.Background(), test.request)
			if !errors.Is(err, ErrInvalidAccountList) {
				t.Fatalf("ListAccounts() error = %v, want ErrInvalidAccountList", err)
			}
		})
	}
}

type managementListStore struct {
	accounts []Account
	search   string
	limit    int64
	offset   int64
}

func (s *managementListStore) ListAccounts(_ context.Context, search string, limit, offset int64) ([]Account, error) {
	s.search, s.limit, s.offset = search, limit, offset
	return s.accounts, nil
}

func (*managementListStore) CreateInvitation(context.Context, int, StaffInvitation) (Account, error) {
	return Account{}, nil
}

func (*managementListStore) LoadInvitedAccount(context.Context, int) (Account, error) {
	return Account{}, nil
}

func (*managementListStore) ResendInvitation(context.Context, int, int, StaffInvitation) (Account, error) {
	return Account{}, nil
}

func (*managementListStore) DeactivateAccount(context.Context, int, int) error { return nil }

func newTestManagementService(t *testing.T, store ManagementStore) *ManagementService {
	t.Helper()
	cipher, err := NewInvitationTokenCipher(map[int16][]byte{1: make([]byte, 32)}, 1)
	if err != nil {
		t.Fatalf("create invitation cipher: %v", err)
	}
	service, err := NewManagementService(store, cipher)
	if err != nil {
		t.Fatalf("create management service: %v", err)
	}
	return service
}
