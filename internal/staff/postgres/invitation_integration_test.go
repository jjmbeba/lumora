package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/staff"
	staffpostgres "example.com/cinema-tickets-backend/internal/staff/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var staffInvitationIntegrationID atomic.Uint64

func TestListAccountsSearchesAndPaginatesWithStableOrdering(t *testing.T) {
	fixture := newStaffInvitationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, name := range []string{"Zebra SearchTarget", "Alpha SearchTarget", "Beta SearchTarget"} {
		account, err := fixture.management.CreateInvitation(ctx, fixture.managerID, staff.CreateInvitationRequest{
			Email:       fixture.email(strings.ReplaceAll(strings.ToLower(name), " ", "-")),
			DisplayName: name, Role: staff.RoleScanner,
		})
		if err != nil {
			t.Fatalf("create searchable account %q: %v", name, err)
		}
		fixture.addAccountID(account.ID)
	}

	firstPage, err := fixture.management.ListAccounts(ctx, staff.ListAccountsRequest{
		Search: "SEARCHTARGET", Page: 1, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("list first matching page: %v", err)
	}
	if firstPage.Page != 1 || firstPage.PageSize != 2 || !firstPage.HasMore || len(firstPage.Accounts) != 2 {
		t.Fatalf("first page metadata/accounts = %+v, want page 1 size 2, two accounts, has_more true", firstPage)
	}
	if firstPage.Accounts[0].DisplayName != "Alpha SearchTarget" || firstPage.Accounts[1].DisplayName != "Beta SearchTarget" {
		t.Fatalf("first page order = %q, %q; want Alpha then Beta", firstPage.Accounts[0].DisplayName, firstPage.Accounts[1].DisplayName)
	}

	secondPage, err := fixture.management.ListAccounts(ctx, staff.ListAccountsRequest{
		Search: "searchtarget", Page: 2, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("list second matching page: %v", err)
	}
	if secondPage.Page != 2 || secondPage.PageSize != 2 || secondPage.HasMore || len(secondPage.Accounts) != 1 {
		t.Fatalf("second page metadata/accounts = %+v, want page 2 size 2, one account, has_more false", secondPage)
	}
	if secondPage.Accounts[0].DisplayName != "Zebra SearchTarget" {
		t.Fatalf("second page account = %q, want Zebra SearchTarget", secondPage.Accounts[0].DisplayName)
	}
}

func TestCreateAndResendInvitationInvalidatePreviousLink(t *testing.T) {
	fixture := newStaffInvitationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := fixture.management.CreateInvitation(ctx, fixture.managerID, staff.CreateInvitationRequest{
		Email: fixture.email("invitee"), DisplayName: "Invitation Test User", Role: staff.RoleScanner,
	})
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	fixture.addAccountID(created.ID)
	if created.Status != staff.AccountStatusInvited || created.Role != staff.RoleScanner {
		t.Fatalf("created account status/role = %s/%s, want INVITED/SCANNER", created.Status, created.Role)
	}

	oldInvitation := fixture.loadInvitation(t, ctx, created.ID)
	oldToken, err := fixture.invitationCipher.Decrypt(created.Email, oldInvitation.keyVersion, oldInvitation.ciphertext)
	if err != nil {
		t.Fatalf("decrypt initial invitation token: %v", err)
	}
	oldTokenDigest := sha256.Sum256([]byte(oldToken))
	if !bytes.Equal(oldInvitation.digest, oldTokenDigest[:]) {
		t.Fatal("stored invitation digest does not match its decrypted token")
	}
	var oldJobStatus, oldRecipient string
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT eo.status::text, eo.recipient_email
		FROM email_outbox AS eo
		WHERE eo.staff_invitation_id = $1`, oldInvitation.id,
	).Scan(&oldJobStatus, &oldRecipient); err != nil {
		t.Fatalf("load initial invitation email job: %v", err)
	}
	if oldJobStatus != "QUEUED" || oldRecipient != created.Email {
		t.Fatalf("initial invitation email job = %s to %q, want QUEUED to %q", oldJobStatus, oldRecipient, created.Email)
	}

	removeOutboxFailure := installInvitationOutboxFailure(t, fixture.db)
	if _, err := fixture.management.ResendInvitation(ctx, fixture.managerID, created.ID); err == nil {
		t.Fatal("ResendInvitation succeeded despite the forced email outbox failure")
	}
	removeOutboxFailure()
	var revokedBeforeRetry sql.NullTime
	var tokenCiphertextBeforeRetry []byte
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT revoked_at, token_ciphertext FROM staff_invitations WHERE id = $1`, oldInvitation.id,
	).Scan(&revokedBeforeRetry, &tokenCiphertextBeforeRetry); err != nil {
		t.Fatalf("load invitation after failed resend: %v", err)
	}
	if revokedBeforeRetry.Valid || len(tokenCiphertextBeforeRetry) == 0 {
		t.Fatal("failed resend did not roll back revocation and token clearing")
	}
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT status::text FROM email_outbox WHERE staff_invitation_id = $1`, oldInvitation.id,
	).Scan(&oldJobStatus); err != nil {
		t.Fatalf("load old email job after failed resend: %v", err)
	}
	if oldJobStatus != "QUEUED" {
		t.Fatalf("old invitation email job after failed resend = %s, want QUEUED", oldJobStatus)
	}

	resent, err := fixture.management.ResendInvitation(ctx, fixture.managerID, created.ID)
	if err != nil {
		t.Fatalf("resend invitation: %v", err)
	}
	if resent.ID != created.ID || resent.Email != created.Email || resent.Status != staff.AccountStatusInvited {
		t.Fatalf("resend returned unexpected account: %+v", resent)
	}
	var revokedAt sql.NullTime
	var oldCiphertext []byte
	var oldKeyVersion sql.NullInt16
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT revoked_at, token_ciphertext, token_key_version
		FROM staff_invitations WHERE id = $1`, oldInvitation.id,
	).Scan(&revokedAt, &oldCiphertext, &oldKeyVersion); err != nil {
		t.Fatalf("load revoked invitation: %v", err)
	}
	if !revokedAt.Valid || oldCiphertext != nil || oldKeyVersion.Valid {
		t.Fatalf("old invitation was not revoked and cleared: revoked=%v ciphertext=%t key_version=%v", revokedAt.Valid, oldCiphertext != nil, oldKeyVersion.Valid)
	}
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT status::text FROM email_outbox WHERE staff_invitation_id = $1`, oldInvitation.id,
	).Scan(&oldJobStatus); err != nil {
		t.Fatalf("load cancelled invitation email job: %v", err)
	}
	if oldJobStatus != "FAILED" {
		t.Fatalf("old invitation email job status = %s, want FAILED", oldJobStatus)
	}

	newInvitation := fixture.loadInvitation(t, ctx, created.ID)
	newToken, err := fixture.invitationCipher.Decrypt(created.Email, newInvitation.keyVersion, newInvitation.ciphertext)
	if err != nil {
		t.Fatalf("decrypt replacement invitation token: %v", err)
	}
	if newToken == oldToken {
		t.Fatal("resend reused the previous invitation token")
	}
	newTokenDigest := sha256.Sum256([]byte(newToken))
	if !bytes.Equal(newInvitation.digest, newTokenDigest[:]) {
		t.Fatal("stored replacement invitation digest does not match its decrypted token")
	}
	var newJobStatus string
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT status::text FROM email_outbox WHERE staff_invitation_id = $1`, newInvitation.id,
	).Scan(&newJobStatus); err != nil {
		t.Fatalf("load replacement invitation email job: %v", err)
	}
	if newJobStatus != "QUEUED" {
		t.Fatalf("replacement invitation email job status = %s, want QUEUED", newJobStatus)
	}
	if _, err := fixture.auth.StartInvitationEnrollment(ctx, oldToken); !errors.Is(err, staff.ErrInvalidStaffInvitation) {
		t.Fatalf("start setup with revoked link error = %v, want ErrInvalidStaffInvitation", err)
	}
	if _, err := fixture.auth.StartInvitationEnrollment(ctx, newToken); err != nil {
		t.Fatalf("start setup with replacement link: %v", err)
	}
}

func TestInvitationEnrollmentActivatesAccountOnce(t *testing.T) {
	fixture := newStaffInvitationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	account, err := fixture.management.CreateInvitation(ctx, fixture.managerID, staff.CreateInvitationRequest{
		Email: fixture.email("enrollment"), DisplayName: "Enrollment Test User", Role: staff.RoleScanner,
	})
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	fixture.addAccountID(account.ID)
	invit := fixture.loadInvitation(t, ctx, account.ID)
	token, err := fixture.invitationCipher.Decrypt(account.Email, invit.keyVersion, invit.ciphertext)
	if err != nil {
		t.Fatalf("decrypt invitation token: %v", err)
	}

	challenge, err := fixture.auth.StartInvitationEnrollment(ctx, token)
	if err != nil {
		t.Fatalf("start invitation enrollment: %v", err)
	}
	result, err := fixture.auth.CompleteInvitationEnrollment(ctx, token, challenge.Token, "integration-test-password", totpCode(challenge.TOTPSecret, time.Now()))
	if err != nil {
		t.Fatalf("complete invitation enrollment: %v", err)
	}
	if result.Account.ID != account.ID || result.Account.Status != staff.AccountStatusActive || !result.Account.MFAEnabled {
		t.Fatalf("completed account state = %+v, want active account with MFA", result.Account)
	}
	if len(result.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes returned = %d, want 10", len(result.RecoveryCodes))
	}

	var passwordHash string
	var status string
	var mfaEnabledAt sql.NullTime
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT status::text, password_hash, mfa_enabled_at
		FROM staff_accounts WHERE id = $1`, account.ID,
	).Scan(&status, &passwordHash, &mfaEnabledAt); err != nil {
		t.Fatalf("load activated account: %v", err)
	}
	if status != "ACTIVE" || passwordHash == "" || passwordHash == "integration-test-password" || !mfaEnabledAt.Valid {
		t.Fatalf("persisted account status/password/MFA = %s/%q/%v", status, passwordHash, mfaEnabledAt.Valid)
	}

	rows, err := fixture.db.QueryContext(ctx, `
		SELECT code_digest FROM staff_recovery_codes WHERE staff_account_id = $1`, account.ID)
	if err != nil {
		t.Fatalf("load recovery code digests: %v", err)
	}
	defer rows.Close()
	stored := make(map[[sha256.Size]byte]bool)
	for rows.Next() {
		var digest []byte
		if err := rows.Scan(&digest); err != nil {
			t.Fatalf("scan recovery digest: %v", err)
		}
		if len(digest) != sha256.Size {
			t.Fatalf("recovery digest length = %d, want %d", len(digest), sha256.Size)
		}
		var fixed [sha256.Size]byte
		copy(fixed[:], digest)
		stored[fixed] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recovery code digests: %v", err)
	}
	if len(stored) != 10 {
		t.Fatalf("stored unique recovery code digests = %d, want 10", len(stored))
	}
	for _, code := range result.RecoveryCodes {
		digest := sha256.Sum256([]byte(code))
		if !stored[digest] {
			t.Fatal("a returned recovery code does not match a stored digest")
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close recovery digest rows: %v", err)
	}

	loginChallenge, err := fixture.auth.StartLogin(ctx, account.Email, "integration-test-password")
	if err != nil {
		t.Fatalf("start login with enrolled password: %v", err)
	}
	identity, session, err := fixture.auth.CompleteLogin(ctx, loginChallenge.Token, totpCode(challenge.TOTPSecret, time.Now()))
	if err != nil {
		t.Fatalf("complete login after invitation enrollment: %v", err)
	}
	if identity.AccountID != account.ID || session.Token == "" {
		t.Fatalf("login after enrollment returned identity/session = %+v/%+v", identity, session)
	}

	var consumedAt sql.NullTime
	var tokenCiphertext []byte
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT consumed_at, token_ciphertext FROM staff_invitations WHERE id = $1`, invit.id,
	).Scan(&consumedAt, &tokenCiphertext); err != nil {
		t.Fatalf("load consumed invitation: %v", err)
	}
	if !consumedAt.Valid || tokenCiphertext != nil {
		t.Fatalf("invitation consumed/ciphertext = %v/%t, want consumed and cleared", consumedAt.Valid, tokenCiphertext != nil)
	}
	var emailJobStatus string
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT status::text FROM email_outbox WHERE staff_invitation_id = $1`, invit.id,
	).Scan(&emailJobStatus); err != nil {
		t.Fatalf("load invitation email state after setup: %v", err)
	}
	if emailJobStatus != "FAILED" {
		t.Fatalf("pending invitation email status after setup = %s, want FAILED", emailJobStatus)
	}
	if _, err := fixture.auth.StartInvitationEnrollment(ctx, token); !errors.Is(err, staff.ErrInvalidStaffInvitation) {
		t.Fatalf("reused invitation token error = %v, want ErrInvalidStaffInvitation", err)
	}
	if _, err := fixture.auth.CompleteInvitationEnrollment(ctx, token, challenge.Token, "integration-test-password", totpCode(challenge.TOTPSecret, time.Now())); !errors.Is(err, staff.ErrInvalidStaffEnrollment) {
		t.Fatalf("replayed enrollment error = %v, want ErrInvalidStaffEnrollment", err)
	}
}

func TestExpiredInvitationCannotStartEnrollment(t *testing.T) {
	fixture := newStaffInvitationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	account, err := fixture.management.CreateInvitation(ctx, fixture.managerID, staff.CreateInvitationRequest{
		Email: fixture.email("expired"), DisplayName: "Expired Invitation User", Role: staff.RoleScanner,
	})
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	fixture.addAccountID(account.ID)
	invit := fixture.loadInvitation(t, ctx, account.ID)
	token, err := fixture.invitationCipher.Decrypt(account.Email, invit.keyVersion, invit.ciphertext)
	if err != nil {
		t.Fatalf("decrypt invitation token: %v", err)
	}
	if _, err := fixture.db.ExecContext(ctx, `
		UPDATE staff_invitations
		SET created_at = clock_timestamp() - interval '2 days',
		    expires_at = clock_timestamp() - interval '1 day'
		WHERE id = $1`, invit.id); err != nil {
		t.Fatalf("expire invitation fixture: %v", err)
	}
	if _, err := fixture.auth.StartInvitationEnrollment(ctx, token); !errors.Is(err, staff.ErrInvalidStaffInvitation) {
		t.Fatalf("expired invitation error = %v, want ErrInvalidStaffInvitation", err)
	}
}

func TestCreateInvitationRollsBackWhenEmailJobInsertFails(t *testing.T) {
	fixture := newStaffInvitationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	installInvitationOutboxFailure(t, fixture.db)

	email := fixture.email("rollback")
	if _, err := fixture.management.CreateInvitation(ctx, fixture.managerID, staff.CreateInvitationRequest{
		Email: email, DisplayName: "Rollback Test User", Role: staff.RoleScanner,
	}); err == nil {
		t.Fatal("CreateInvitation succeeded despite the forced email outbox failure")
	}
	var accountCount, invitationCount int
	if err := fixture.db.QueryRowContext(ctx, `SELECT count(*) FROM staff_accounts WHERE email = $1`, email).Scan(&accountCount); err != nil {
		t.Fatalf("count rolled-back account rows: %v", err)
	}
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT count(*) FROM staff_invitations si
		JOIN staff_accounts sa ON sa.id = si.staff_account_id
		WHERE sa.email = $1`, email,
	).Scan(&invitationCount); err != nil {
		t.Fatalf("count rolled-back invitation rows: %v", err)
	}
	if accountCount != 0 || invitationCount != 0 {
		t.Fatalf("failed invitation creation left account/invitation rows = %d/%d, want 0/0", accountCount, invitationCount)
	}
}

func installInvitationOutboxFailure(t *testing.T, db *sql.DB) func() {
	t.Helper()
	suffix := fmt.Sprintf("%d", staffInvitationIntegrationID.Add(1))
	functionName := "staff_invitation_outbox_failure_" + suffix
	triggerName := "staff_invitation_outbox_failure_trigger_" + suffix
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.kind = 'STAFF_INVITATION' THEN
				RAISE EXCEPTION 'forced invitation outbox failure';
			END IF;
			RETURN NEW;
		END
		$$`, functionName)); err != nil {
		t.Fatalf("create invitation failure trigger function: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE TRIGGER %s BEFORE INSERT ON email_outbox
		FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		_, _ = db.Exec(fmt.Sprintf("DROP FUNCTION %s()", functionName))
		t.Fatalf("create invitation failure trigger: %v", err)
	}
	removed := false
	remove := func() {
		if removed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON email_outbox", triggerName)); err != nil {
			t.Errorf("drop invitation failure trigger: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)); err != nil {
			t.Errorf("drop invitation failure trigger function: %v", err)
		}
		removed = true
	}
	t.Cleanup(remove)
	return remove
}

type staffInvitationFixture struct {
	db               *sql.DB
	managerID        int
	accountIDs       []int
	emailPrefix      string
	management       *staff.ManagementService
	auth             *staff.AuthService
	invitationCipher *staff.InvitationTokenCipher
}

type storedStaffInvitation struct {
	id         int32
	digest     []byte
	ciphertext []byte
	keyVersion int16
}

func newStaffInvitationFixture(t *testing.T) *staffInvitationFixture {
	t.Helper()
	dsn := os.Getenv("STAFF_INVITATION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set STAFF_INVITATION_TEST_DATABASE_URL to a dedicated PostgreSQL database with migrations applied")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open staff invitation integration database: %v", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close staff invitation integration database: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to staff invitation integration database: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("identify staff invitation integration database: %v", err)
	}
	if !strings.Contains(strings.ToLower(databaseName), "test") {
		t.Fatalf("STAFF_INVITATION_TEST_DATABASE_URL points to %q; database name must include 'test'", databaseName)
	}
	var schemaReady bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass('staff_invitations') IS NOT NULL
		   AND to_regclass('email_outbox') IS NOT NULL
		   AND to_regclass('staff_auth_challenges') IS NOT NULL
		   AND to_regclass('staff_recovery_codes') IS NOT NULL`).Scan(&schemaReady); err != nil {
		t.Fatalf("check staff invitation schema: %v", err)
	}
	if !schemaReady {
		t.Fatal("staff_invitations is missing; run go run ./cmd/migrate up against the invitation test database first")
	}

	suffix := fmt.Sprintf("%d_%d", os.Getpid(), staffInvitationIntegrationID.Add(1))
	emailPrefix := "staff-invitation-integration-" + suffix
	managerEmail := emailPrefix + "-manager@example.test"
	var managerID int
	if err := db.QueryRowContext(ctx, `
		INSERT INTO staff_accounts (
			email, display_name, role, status, password_hash,
			mfa_secret_ciphertext, mfa_key_version, mfa_enabled_at
		)
		VALUES ($1, 'Invitation Test Manager', 'MANAGER', 'ACTIVE', 'integration-password-hash',
			decode('01', 'hex'), 1, clock_timestamp())
		RETURNING id`, managerEmail,
	).Scan(&managerID); err != nil {
		t.Fatalf("create invitation test manager: %v", err)
	}
	fixture := &staffInvitationFixture{db: db, managerID: managerID, accountIDs: []int{managerID}, emailPrefix: emailPrefix}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := fixture.cleanup(cleanupCtx); err != nil {
			t.Errorf("clean up staff invitation fixtures: %v", err)
		}
	})
	mfaKey := make([]byte, 32)
	invitationKey := make([]byte, 32)
	for i := range mfaKey {
		mfaKey[i] = byte(i + 1)
		invitationKey[i] = byte(255 - i)
	}
	mfaCipher, err := staff.NewMFACipher(map[int16][]byte{1: mfaKey}, 1)
	if err != nil {
		t.Fatalf("create staff MFA cipher: %v", err)
	}
	fixture.invitationCipher, err = staff.NewInvitationTokenCipher(map[int16][]byte{1: invitationKey}, 1)
	if err != nil {
		t.Fatalf("create invitation token cipher: %v", err)
	}
	store, err := staffpostgres.NewStore(db)
	if err != nil {
		t.Fatalf("create staff PostgreSQL store: %v", err)
	}
	fixture.management, err = staff.NewManagementService(store, fixture.invitationCipher)
	if err != nil {
		t.Fatalf("create staff management service: %v", err)
	}
	fixture.auth, err = staff.NewAuthService(store, mfaCipher)
	if err != nil {
		t.Fatalf("create staff auth service: %v", err)
	}
	return fixture
}

func (f *staffInvitationFixture) email(label string) string {
	return f.emailPrefix + "-" + label + "@example.test"
}

func (f *staffInvitationFixture) loadInvitation(t *testing.T, ctx context.Context, accountID int) storedStaffInvitation {
	t.Helper()
	var invitation storedStaffInvitation
	if err := f.db.QueryRowContext(ctx, `
		SELECT id, token_digest, token_ciphertext, token_key_version
		FROM staff_invitations
		WHERE staff_account_id = $1 AND consumed_at IS NULL AND revoked_at IS NULL`, accountID,
	).Scan(&invitation.id, &invitation.digest, &invitation.ciphertext, &invitation.keyVersion); err != nil {
		t.Fatalf("load open invitation for account %d: %v", accountID, err)
	}
	return invitation
}

func (f *staffInvitationFixture) cleanup(ctx context.Context) error {
	ids := make([]int32, len(f.accountIDs))
	for i, id := range f.accountIDs {
		ids[i] = int32(id)
	}
	queries := []string{
		`DELETE FROM email_outbox WHERE staff_invitation_id IN (
			SELECT id FROM staff_invitations
			WHERE staff_account_id = ANY($1::int[]) OR created_by_staff_account_id = ANY($1::int[]))`,
		`DELETE FROM staff_invitations WHERE staff_account_id = ANY($1::int[]) OR created_by_staff_account_id = ANY($1::int[])`,
		`DELETE FROM staff_auth_challenges WHERE staff_account_id = ANY($1::int[])`,
		`DELETE FROM staff_recovery_codes WHERE staff_account_id = ANY($1::int[])`,
		`DELETE FROM staff_sessions WHERE staff_account_id = ANY($1::int[])`,
	}
	for _, query := range queries {
		if _, err := f.db.ExecContext(ctx, query, ids); err != nil {
			return err
		}
	}
	if _, err := f.db.ExecContext(ctx, `DELETE FROM staff_accounts WHERE id = ANY($1::int[]) AND id <> $2`, ids, int32(f.managerID)); err != nil {
		return err
	}
	if _, err := f.db.ExecContext(ctx, `DELETE FROM staff_accounts WHERE id = $1`, int32(f.managerID)); err != nil {
		return err
	}
	return nil
}

func (f *staffInvitationFixture) addAccountID(id int) {
	f.accountIDs = append(f.accountIDs, id)
}
