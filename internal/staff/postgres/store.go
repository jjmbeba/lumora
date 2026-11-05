package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"example.com/cinema-tickets-backend/internal/staff"
	"example.com/cinema-tickets-backend/internal/staff/postgres/dbgen"
	"github.com/jackc/pgx/v5/pgconn"
)

type Store struct{ db *sql.DB }

var _ staff.Store = (*Store)(nil)
var _ staff.ManagementStore = (*Store)(nil)

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("staff database pool is required")
	}

	return &Store{db: db}, nil
}

func (s *Store) BootstrapOwnerExists(ctx context.Context) (bool, error) {
	exists, err := dbgen.New(s.db).BootstrapOwnerExists(ctx)
	if err != nil {
		return false, fmt.Errorf("check bootstrap manager: %w", err)
	}

	return exists, nil
}

func (s *Store) CreateBootstrapOwner(ctx context.Context, owner staff.BootstrapOwner) (staff.Account, error) {
	owner.Email = strings.ToLower(strings.TrimSpace(owner.Email))
	owner.DisplayName = strings.TrimSpace(owner.DisplayName)

	if owner.Email == "" || owner.DisplayName == "" || owner.PasswordHash == "" ||
		len(owner.MFASecretCiphertext) == 0 || owner.MFAKeyVersion <= 0 || len(owner.RecoveryCodeDigests) != 10 {
		return staff.Account{}, errors.New("bootstrap owner credentials and ten recovery codes are required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return staff.Account{}, fmt.Errorf("begin staff bootstrap transaction: %w", err)
	}
	defer tx.Rollback()

	row, err := dbgen.New(tx).CreateBootstrapOwner(ctx, dbgen.CreateBootstrapOwnerParams{
		Email: owner.Email, DisplayName: owner.DisplayName,
		PasswordHash:        sql.NullString{String: owner.PasswordHash, Valid: true},
		MfaSecretCiphertext: owner.MFASecretCiphertext,
		MfaKeyVersion:       sql.NullInt16{Int16: owner.MFAKeyVersion, Valid: true},
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "staff_accounts_one_bootstrap_owner" {
			return staff.Account{}, staff.ErrBootstrapUnavailable
		}
		return staff.Account{}, fmt.Errorf("create bootstrap manager: %w", err)
	}

	for _, digest := range owner.RecoveryCodeDigests {
		if len(digest) != 32 {
			return staff.Account{}, errors.New("recovery code digest must be 32 bytes")
		}

		if err := dbgen.New(tx).InsertRecoveryCode(ctx, dbgen.InsertRecoveryCodeParams{StaffAccountID: row.ID, CodeDigest: digest}); err != nil {
			return staff.Account{}, fmt.Errorf("save bootstrap recovery code: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return staff.Account{}, fmt.Errorf("commit staff bootstrap transaction: %w", err)
	}

	return staff.Account{
		ID: int(row.ID), Email: row.Email, DisplayName: row.DisplayName,
		Role: staff.Role(row.Role), Status: staff.AccountStatus(row.Status), MFAEnabled: true,
	}, nil
}

func (s *Store) FindLoginCredentials(ctx context.Context, email string) (staff.LoginCredentials, error) {
	row, err := dbgen.New(s.db).FindLoginCredentials(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return staff.LoginCredentials{}, staff.ErrAccountUnavailable
	}

	if err != nil {
		return staff.LoginCredentials{}, fmt.Errorf("find staff login credentials: %w", err)
	}

	if !row.PasswordHash.Valid || !row.MfaKeyVersion.Valid {
		return staff.LoginCredentials{}, staff.ErrAccountUnavailable
	}

	return staff.LoginCredentials{
		Account: staff.Account{
			ID: int(row.ID), Email: row.Email, DisplayName: row.DisplayName,
			Role: staff.Role(row.Role), Status: staff.AccountStatus(row.Status), MFAEnabled: row.MfaEnabled,
		},
		PasswordHash:        row.PasswordHash.String,
		MFASecretCiphertext: append([]byte(nil), row.MfaSecretCiphertext...),
		MFAKeyVersion:       row.MfaKeyVersion.Int16,
	}, nil
}

func (s *Store) CreateLoginChallenge(ctx context.Context, accountID int, purpose staff.ChallengePurpose, digest []byte, expiresAt time.Time) error {
	if accountID <= 0 || len(digest) != 32 || (purpose != staff.ChallengePurposeSignIn && purpose != staff.ChallengePurposeEnrollment) {
		return errors.New("invalid staff MFA challenge")
	}

	err := dbgen.New(s.db).CreateAuthChallenge(ctx, dbgen.CreateAuthChallengeParams{
		StaffAccountID: int32(accountID), TokenDigest: digest, Purpose: string(purpose), ExpiresAt: expiresAt,
	})

	if err != nil {
		return fmt.Errorf("create staff MFA challenge: %w", err)
	}

	return nil
}

func (s *Store) LoadLoginChallenge(ctx context.Context, digest []byte) (staff.ChallengeCredentials, error) {
	row, err := dbgen.New(s.db).LoadAuthChallenge(ctx, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ChallengeCredentials{}, staff.ErrInvalidChallenge
	}

	if err != nil {
		return staff.ChallengeCredentials{}, fmt.Errorf("load MFA challenge: %w", err)
	}

	if row.AccountStatus != string(staff.AccountStatusActive) || !row.MfaKeyVersion.Valid || row.MfaKeyVersion.Int16 <= 0 || len(row.MfaSecretCiphertext) == 0 {
		return staff.ChallengeCredentials{}, staff.ErrInvalidChallenge
	}

	return staff.ChallengeCredentials{
		Account: staff.Account{ID: int(row.StaffAccountID), Email: row.Email, DisplayName: row.DisplayName, Role: staff.Role(row.AccountRole), Status: staff.AccountStatus(row.AccountStatus), MFAEnabled: true},
		Purpose: staff.ChallengePurpose(row.Purpose), MFASecretCiphertext: append([]byte(nil), row.MfaSecretCiphertext...), MFAKeyVersion: row.MfaKeyVersion.Int16,
	}, nil
}

func (s *Store) RecordLoginChallengeFailure(ctx context.Context, digest []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin MFA challenge failure transaction: %w", err)
	}

	defer tx.Rollback()

	challenge, err := dbgen.New(tx).LockAuthChallenge(ctx, digest)

	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrInvalidChallenge
	}
	if err != nil {
		return fmt.Errorf("lock MFA challenge: %w", err)
	}

	rows, err := dbgen.New(tx).RecordAuthChallengeFailure(ctx, challenge.ChallengeID)

	if err != nil {
		return fmt.Errorf("record MFA failure: %w", err)
	}
	if rows != 1 {
		return staff.ErrInvalidChallenge
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit MFA failure: %w", err)
	}

	return nil
}

func (s *Store) CompleteLoginChallenge(ctx context.Context, challengeDigest, recoveryDigest, sessionDigest []byte, expiresAt time.Time) (staff.Identity, error) {
	if len(challengeDigest) != 32 || len(sessionDigest) != 32 || (len(recoveryDigest) != 0 && len(recoveryDigest) != 32) {
		return staff.Identity{}, staff.ErrInvalidChallenge
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return staff.Identity{}, fmt.Errorf("begin complete MFA login transaction: %w", err)
	}

	defer tx.Rollback()

	queries := dbgen.New(tx)
	challenge, err := queries.LockAuthChallenge(ctx, challengeDigest)

	if errors.Is(err, sql.ErrNoRows) {
		return staff.Identity{}, staff.ErrInvalidChallenge
	}
	if err != nil {
		return staff.Identity{}, fmt.Errorf("lock MFA challenge: %w", err)
	}

	if challenge.AccountStatus != string(staff.AccountStatusActive) || challenge.Purpose != string(staff.ChallengePurposeSignIn) {
		return staff.Identity{}, staff.ErrInvalidChallenge
	}

	if len(recoveryDigest) != 0 {
		if _, err := queries.ConsumeRecoveryCode(ctx, dbgen.ConsumeRecoveryCodeParams{StaffAccountID: challenge.StaffAccountID, CodeDigest: recoveryDigest}); errors.Is(err, sql.ErrNoRows) {
			return staff.Identity{}, staff.ErrInvalidMFA
		} else if err != nil {
			return staff.Identity{}, fmt.Errorf("consume staff recovery code: %w", err)
		}
	}

	rows, err := queries.ConsumeAuthChallenge(ctx, challenge.ChallengeID)

	if err != nil {
		return staff.Identity{}, fmt.Errorf("consume MFA challenge: %w", err)
	}
	if rows != 1 {
		return staff.Identity{}, staff.ErrInvalidChallenge
	}

	if _, err := queries.CreateStaffSession(ctx, dbgen.CreateStaffSessionParams{
		StaffAccountID: challenge.StaffAccountID, TokenDigest: sessionDigest, ExpiresAt: expiresAt,
	}); err != nil {
		return staff.Identity{}, fmt.Errorf("create staff session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return staff.Identity{}, fmt.Errorf("commit staff MFA login: %w", err)
	}

	return staff.Identity{AccountID: int(challenge.StaffAccountID), Email: challenge.Email, DisplayName: challenge.DisplayName, Role: staff.Role(challenge.AccountRole)}, nil
}

func (s *Store) AuthenticateSession(ctx context.Context, digest []byte) (staff.Identity, time.Time, error) {
	if len(digest) != 32 {
		return staff.Identity{}, time.Time{}, staff.ErrInvalidSession
	}

	row, err := dbgen.New(s.db).AuthenticateStaffSession(ctx, digest)

	if errors.Is(err, sql.ErrNoRows) {
		return staff.Identity{}, time.Time{}, staff.ErrInvalidSession
	}
	if err != nil {
		return staff.Identity{}, time.Time{}, fmt.Errorf("authenticate staff session: %w", err)
	}

	return staff.Identity{AccountID: int(row.AccountID), Email: row.Email, DisplayName: row.DisplayName, Role: staff.Role(row.Role)}, row.ExpiresAt, nil
}

func (s *Store) RevokeSession(ctx context.Context, digest []byte) error {
	if len(digest) != 32 {
		return staff.ErrInvalidSession
	}

	if _, err := dbgen.New(s.db).RevokeStaffSession(ctx, digest); err != nil {
		return fmt.Errorf("revoke staff session: %w", err)
	}

	return nil
}

func (s *Store) DeactivateAccount(ctx context.Context, managerID, accountID int) error {
	if managerID <= 0 || accountID <= 0 || int64(managerID) > 1<<31-1 || int64(accountID) > 1<<31-1 {
		return staff.ErrInvalidAccountID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin staff account deactivation transaction: %w", err)
	}
	defer tx.Rollback()

	queries := dbgen.New(tx)
	managerIDs, err := queries.LockActiveStaffManagers(ctx)
	if err != nil {
		return fmt.Errorf("lock active staff managers: %w", err)
	}

	if !slices.Contains(managerIDs, int32(managerID)) {
		return staff.ErrManagerUnavailable
	}

	target, err := queries.LockStaffAccount(ctx, int32(accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrStaffAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("lock staff account %d: %w", accountID, err)
	}

	if target.Role == string(staff.RoleManager) && target.Status == string(staff.AccountStatusActive) && len(managerIDs) == 1 {
		return staff.ErrLastActiveManager
	}

	if _, err := queries.DisableStaffAccount(ctx, int32(accountID)); err != nil {
		return fmt.Errorf("disable staff account %d: %w", accountID, err)
	}
	if err := queries.RevokeStaffAccountSessions(ctx, int32(accountID)); err != nil {
		return fmt.Errorf("revoke sessions for staff account %d: %w", accountID, err)
	}

	if err := queries.CancelStaffInvitationEmailJobs(ctx, int32(accountID)); err != nil {
		return fmt.Errorf("cancel invitation emails for staff account %d: %w", accountID, err)
	}
	if err := queries.RevokeOpenStaffInvitations(ctx, int32(accountID)); err != nil {
		return fmt.Errorf("revoke invitations for staff account %d: %w", accountID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit staff account deactivation: %w", err)
	}
	return nil
}
