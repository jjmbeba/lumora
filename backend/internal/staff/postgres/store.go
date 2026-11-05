package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jjmbeba/lumora/backend/internal/staff"
	"github.com/jjmbeba/lumora/backend/internal/staff/postgres/dbgen"
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

func (s *Store) CreateInvitation(ctx context.Context, managerID int, invitation staff.StaffInvitation) (staff.Account, error) {
	if managerID <= 0 || int64(managerID) > 1<<31-1 || invitation.Email == "" || invitation.DisplayName == "" ||
		(invitation.Role != staff.RoleManager && invitation.Role != staff.RoleScanner) ||
		len(invitation.TokenDigest) != 32 || len(invitation.TokenCiphertext) == 0 || invitation.TokenKeyVersion <= 0 ||
		!invitation.ExpiresAt.After(time.Now()) {
		return staff.Account{}, staff.ErrInvalidStaffInvitation
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return staff.Account{}, fmt.Errorf("begin staff invitation transaction: %w", err)
	}

	defer tx.Rollback()

	queries := dbgen.New(tx)
	managerIDs, err := queries.LockActiveStaffManagers(ctx)
	if err != nil {
		return staff.Account{}, fmt.Errorf("lock active staff managers: %w", err)
	}

	if !slices.Contains(managerIDs, int32(managerID)) {
		return staff.Account{}, staff.ErrManagerUnavailable
	}

	account, err := queries.CreateInvitedStaffAccount(ctx, dbgen.CreateInvitedStaffAccountParams{
		Email: invitation.Email, DisplayName: invitation.DisplayName,
		Role: dbgen.StaffRole(invitation.Role), CreatedByStaffAccountID: sql.NullInt32{Int32: int32(managerID), Valid: true},
	})

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "staff_accounts_email_unique" {
			return staff.Account{}, staff.ErrStaffEmailAlreadyExists
		}
		return staff.Account{}, fmt.Errorf("create invited staff account: %w", err)
	}

	if err := createInvitationAndEmailJob(ctx, queries, account.ID, int32(managerID), invitation); err != nil {
		return staff.Account{}, err
	}

	if err := tx.Commit(); err != nil {
		return staff.Account{}, fmt.Errorf("commit staff invitation: %w", err)
	}

	return staff.Account{
		ID: int(account.ID), Email: account.Email, DisplayName: account.DisplayName,
		Role: staff.Role(account.Role), Status: staff.AccountStatus(account.Status),
	}, nil
}

func (s *Store) ListAccounts(ctx context.Context, search string, limit, offset int64) ([]staff.Account, error) {
	rows, err := dbgen.New(s.db).ListStaffAccounts(ctx, dbgen.ListStaffAccountsParams{
		Search: search, LimitCount: limit, OffsetCount: offset,
	})

	if err != nil {
		return nil, fmt.Errorf("list staff accounts: %w", err)
	}

	accounts := make([]staff.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, staff.Account{
			ID: int(row.ID), Email: row.Email, DisplayName: row.DisplayName,
			Role: staff.Role(row.Role), Status: staff.AccountStatus(row.Status), CreatedAt: row.CreatedAt,
		})
	}

	return accounts, nil
}

func (s *Store) LoadInvitedAccount(ctx context.Context, accountID int) (staff.Account, error) {
	if accountID <= 0 || int64(accountID) > 1<<31-1 {
		return staff.Account{}, staff.ErrInvalidAccountID
	}

	row, err := dbgen.New(s.db).LoadInvitedStaffAccount(ctx, int32(accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrAccountUnavailable
	}

	if err != nil {
		return staff.Account{}, fmt.Errorf("load invited staff account: %w", err)
	}

	return staff.Account{ID: int(row.ID), Email: row.Email, DisplayName: row.DisplayName, Role: staff.Role(row.Role), Status: staff.AccountStatus(row.Status)}, nil
}

func (s *Store) ResendInvitation(ctx context.Context, managerID, accountID int, invitation staff.StaffInvitation) (staff.Account, error) {
	if managerID <= 0 || accountID <= 0 || int64(managerID) > 1<<31-1 || int64(accountID) > 1<<31-1 ||
		len(invitation.TokenDigest) != 32 || len(invitation.TokenCiphertext) == 0 || invitation.TokenKeyVersion <= 0 ||
		!invitation.ExpiresAt.After(time.Now()) {
		return staff.Account{}, staff.ErrInvalidStaffInvitation
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return staff.Account{}, fmt.Errorf("begin staff invitation resend transaction: %w", err)
	}

	defer tx.Rollback()

	queries := dbgen.New(tx)
	managerIDs, err := queries.LockActiveStaffManagers(ctx)
	if err != nil {
		return staff.Account{}, fmt.Errorf("lock active staff managers: %w", err)
	}

	if !slices.Contains(managerIDs, int32(managerID)) {
		return staff.Account{}, staff.ErrManagerUnavailable
	}

	account, err := queries.LockInvitedStaffAccount(ctx, int32(accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrStaffAccountNotFound
	}

	if err != nil {
		return staff.Account{}, fmt.Errorf("lock invited staff account %d: %w", accountID, err)
	}

	if err := queries.CancelStaffInvitationEmailJobs(ctx, int32(accountID)); err != nil {
		return staff.Account{}, fmt.Errorf("cancel previous staff invitation email: %w", err)
	}

	if err := queries.RevokeOpenStaffInvitations(ctx, int32(accountID)); err != nil {
		return staff.Account{}, fmt.Errorf("revoke previous staff invitation: %w", err)
	}

	if err := createInvitationAndEmailJob(ctx, queries, int32(accountID), int32(managerID), invitation); err != nil {
		return staff.Account{}, err
	}

	if err := tx.Commit(); err != nil {
		return staff.Account{}, fmt.Errorf("commit staff invitation resend: %w", err)
	}

	return staff.Account{ID: int(account.ID), Email: account.Email, DisplayName: account.DisplayName, Role: staff.Role(account.Role), Status: staff.AccountStatus(account.Status)}, nil
}

func createInvitationAndEmailJob(ctx context.Context, queries *dbgen.Queries, accountID, managerID int32, invitation staff.StaffInvitation) error {
	invitationID, err := queries.CreateStaffInvitation(ctx, dbgen.CreateStaffInvitationParams{
		StaffAccountID: accountID, TokenDigest: invitation.TokenDigest, TokenCiphertext: invitation.TokenCiphertext,
		TokenKeyVersion: sql.NullInt16{Int16: invitation.TokenKeyVersion, Valid: true},
		ExpiresAt:       invitation.ExpiresAt, CreatedByStaffAccountID: managerID,
	})

	if err != nil {
		return fmt.Errorf("create staff invitation: %w", err)
	}

	if err := queries.CreateStaffInvitationEmailJob(ctx, dbgen.CreateStaffInvitationEmailJobParams{
		EventKey: fmt.Sprintf("staff-invitation:%d", invitationID), RecipientEmail: invitation.Email,
		StaffInvitationID: sql.NullInt32{Int32: invitationID, Valid: true},
	}); err != nil {
		return fmt.Errorf("queue staff invitation email: %w", err)
	}

	return nil
}

func (s *Store) LoadInvitation(ctx context.Context, digest []byte) (staff.Account, error) {
	if len(digest) != 32 {
		return staff.Account{}, staff.ErrInvalidStaffInvitation
	}

	row, err := dbgen.New(s.db).LoadStaffInvitation(ctx, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrAccountUnavailable
	}

	if err != nil {
		return staff.Account{}, fmt.Errorf("load staff invitation: %w", err)
	}

	return staff.Account{
		ID: int(row.ID), Email: row.Email, DisplayName: row.DisplayName,
		Role: staff.Role(row.Role), Status: staff.AccountStatus(row.Status),
	}, nil
}

func (s *Store) StartInvitationEnrollment(ctx context.Context, invitationDigest, secretCiphertext []byte, keyVersion int16, challengeDigest []byte, expiresAt time.Time) error {
	if len(invitationDigest) != 32 || len(secretCiphertext) == 0 || keyVersion <= 0 || len(challengeDigest) != 32 || !expiresAt.After(time.Now()) {
		return staff.ErrInvalidStaffInvitation
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin staff invitation enrollment transaction: %w", err)
	}

	defer tx.Rollback()

	queries := dbgen.New(tx)
	loaded, err := queries.LoadStaffInvitation(ctx, invitationDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrInvalidStaffInvitation
	}

	if err != nil {
		return fmt.Errorf("load staff invitation for setup: %w", err)
	}

	account, err := queries.LockStaffAccount(ctx, loaded.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrInvalidStaffInvitation
	}

	if err != nil {
		return fmt.Errorf("lock invited staff account: %w", err)
	}

	if account.Status != string(staff.AccountStatusInvited) {
		return staff.ErrInvalidStaffInvitation
	}

	invitation, err := queries.LockStaffInvitationForSetup(ctx, invitationDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrInvalidStaffInvitation
	}

	if err != nil {
		return fmt.Errorf("lock staff invitation for setup: %w", err)
	}

	if invitation.AccountStatus != string(staff.AccountStatusInvited) {
		return staff.ErrInvalidStaffInvitation
	}

	if err := queries.ConsumePendingStaffEnrollmentChallenges(ctx, invitation.StaffAccountID); err != nil {
		return fmt.Errorf("replace previous staff enrollment challenges: %w", err)
	}

	rows, err := queries.SetStaffInvitationMFASecret(ctx, dbgen.SetStaffInvitationMFASecretParams{
		MfaSecretCiphertext: secretCiphertext,
		MfaKeyVersion:       sql.NullInt16{Int16: keyVersion, Valid: true},
		ID:                  invitation.StaffAccountID,
	})

	if err != nil {
		return fmt.Errorf("save pending staff TOTP secret: %w", err)
	}
	if rows != 1 {
		return staff.ErrInvalidStaffInvitation
	}

	if err := queries.CreateAuthChallenge(ctx, dbgen.CreateAuthChallengeParams{
		StaffAccountID: invitation.StaffAccountID, TokenDigest: challengeDigest,
		Purpose: string(staff.ChallengePurposeEnrollment), ExpiresAt: expiresAt,
	}); err != nil {
		return fmt.Errorf("create staff enrollment challenge: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit staff invitation enrollment start: %w", err)
	}

	return nil
}

func (s *Store) CompleteInvitationEnrollment(ctx context.Context, invitationDigest, challengeDigest []byte, passwordHash string, recoveryDigests [][]byte) (staff.Account, error) {
	if len(invitationDigest) != 32 || len(challengeDigest) != 32 || passwordHash == "" || len(recoveryDigests) != 10 {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	for _, digest := range recoveryDigests {
		if len(digest) != 32 {
			return staff.Account{}, staff.ErrInvalidStaffEnrollment
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return staff.Account{}, fmt.Errorf("begin complete staff enrollment transaction: %w", err)
	}

	defer tx.Rollback()

	queries := dbgen.New(tx)
	loaded, err := queries.LoadStaffInvitation(ctx, invitationDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	if err != nil {
		return staff.Account{}, fmt.Errorf("load staff invitation for enrollment: %w", err)
	}

	account, err := queries.LockStaffAccount(ctx, loaded.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}
	if err != nil {
		return staff.Account{}, fmt.Errorf("lock invited staff account for enrollment: %w", err)
	}

	if account.Status != string(staff.AccountStatusInvited) {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	if err := queries.CancelStaffInvitationEmailJobs(ctx, loaded.ID); err != nil {
		return staff.Account{}, fmt.Errorf("cancel pending staff invitation emails: %w", err)
	}

	invitation, err := queries.LockStaffInvitationForSetup(ctx, invitationDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	if err != nil {
		return staff.Account{}, fmt.Errorf("lock staff invitation for enrollment completion: %w", err)
	}

	challenge, err := queries.LockAuthChallenge(ctx, challengeDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	if err != nil {
		return staff.Account{}, fmt.Errorf("lock staff enrollment challenge: %w", err)
	}

	if challenge.StaffAccountID != invitation.StaffAccountID || challenge.Purpose != string(staff.ChallengePurposeEnrollment) || challenge.AccountStatus != string(staff.AccountStatusInvited) {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	rows, err := queries.ConsumeAuthChallenge(ctx, challenge.ChallengeID)
	if err != nil {
		return staff.Account{}, fmt.Errorf("consume staff enrollment challenge: %w", err)
	}

	if rows != 1 {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	rows, err = queries.ActivateStaffInvitationAccount(ctx, dbgen.ActivateStaffInvitationAccountParams{
		PasswordHash: sql.NullString{String: passwordHash, Valid: true}, ID: invitation.StaffAccountID,
	})
	if err != nil {
		return staff.Account{}, fmt.Errorf("activate staff account after enrollment: %w", err)
	}

	if rows != 1 {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	for _, digest := range recoveryDigests {
		if err := queries.InsertRecoveryCode(ctx, dbgen.InsertRecoveryCodeParams{StaffAccountID: invitation.StaffAccountID, CodeDigest: digest}); err != nil {
			return staff.Account{}, fmt.Errorf("save staff recovery code: %w", err)
		}
	}

	rows, err = queries.ConsumeStaffInvitation(ctx, invitation.InvitationID)
	if err != nil {
		return staff.Account{}, fmt.Errorf("consume staff invitation: %w", err)
	}

	if rows != 1 {
		return staff.Account{}, staff.ErrInvalidStaffEnrollment
	}

	if err := tx.Commit(); err != nil {
		return staff.Account{}, fmt.Errorf("commit staff invitation enrollment: %w", err)
	}

	return staff.Account{
		ID: int(invitation.StaffAccountID), Email: challenge.Email,
		DisplayName: challenge.DisplayName, Role: staff.Role(challenge.AccountRole),
		Status: staff.AccountStatusActive, MFAEnabled: true,
	}, nil
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

	validSignIn := row.AccountStatus == string(staff.AccountStatusActive) && row.Purpose == string(staff.ChallengePurposeSignIn)
	validEnrollment := row.AccountStatus == string(staff.AccountStatusInvited) && row.Purpose == string(staff.ChallengePurposeEnrollment)
	if (!validSignIn && !validEnrollment) || !row.MfaKeyVersion.Valid || row.MfaKeyVersion.Int16 <= 0 || len(row.MfaSecretCiphertext) == 0 {
		return staff.ChallengeCredentials{}, staff.ErrInvalidChallenge
	}

	return staff.ChallengeCredentials{
		Account: staff.Account{ID: int(row.StaffAccountID), Email: row.Email, DisplayName: row.DisplayName, Role: staff.Role(row.AccountRole), Status: staff.AccountStatus(row.AccountStatus), MFAEnabled: validSignIn},
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
