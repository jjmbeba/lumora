# Staff authentication implementation

The accepted staff-authentication decisions are in [decisions.md](decisions.md). This page records the initial Go implementation boundary and the configuration needed to run it.

## Sign-in flow

1. `POST /staff/auth/login` accepts an email and password. A valid password creates a five-minute `MFA_SIGN_IN` challenge; it does not create a staff session.
2. `POST /staff/auth/mfa` accepts that challenge and either a six-digit TOTP code or one recovery code. The challenge is consumed and a session row is created in one PostgreSQL transaction.
3. The response sets the opaque session token in an `HttpOnly`, `Secure`, `SameSite=Strict` cookie. PostgreSQL stores only its SHA-256 digest. Protected handlers use `AuthHandler.RequireSession`, which checks account status, absolute expiry, and the 15-minute idle limit on each request.
4. `POST /staff/auth/logout` revokes the session and clears the cookie.

Cookie-authenticated mutations require the exact configured `Origin` and `X-Staff-Request: 1`. Set `STAFF_FRONTEND_ORIGIN` to the frontend origin (scheme and host only). This check applies to logout and manager mutations. Manager-only handlers must use `AuthHandler.RequireRole(staff.RoleManager, handler)`. It authenticates the session first, then returns `403 Forbidden` when the authenticated account is not a manager. Do not authorize from a client-supplied role or trust a role copied into a session cookie.

`POST /staff/accounts/{id}/deactivation` is manager-only. It disables the account and revokes its sessions and any open invitation in one transaction. It returns `409 Conflict` when the target is the last active manager.

Sessions expire after eight hours even when active. An MFA challenge allows at most five failed attempts. Recovery-code consumption and challenge consumption are atomic with session creation.

## MFA encryption configuration

The booking API requires the following environment variables to configure its versioned AES-256 key ring:

```text
STAFF_MFA_CURRENT_KEY_VERSION=1
STAFF_MFA_KEY_VERSIONS=1
STAFF_MFA_KEY_1=<standard-base64-encoded 32-byte key>
```

Keep the key material outside source control and PostgreSQL. Retain old key versions while stored TOTP secrets still use them. Generate keys with a cryptographically secure random source; do not reuse the ticket credential or booking verification keys.

The booking API and invitation worker also require a separate versioned key ring for encrypted invitation tokens:

```text
STAFF_INVITATION_CURRENT_KEY_VERSION=1
STAFF_INVITATION_KEY_VERSIONS=1
STAFF_INVITATION_KEY_1=<standard-base64-encoded 32-byte key>
```

The booking API uses this ring when creating invitations; run `cd backend && go run ./cmd/staff-invitation-worker` with the same retained versions and `STAFF_INVITATION_SETUP_URL` set to the frontend's account-setup page. The API also requires `STAFF_FRONTEND_ORIGIN`, such as `https://staff.example.com`. The invitation worker requires `DATABASE_URL`, `RESEND_API_KEY`, `RESEND_EMAIL_FROM` (or legacy `RESEND_FROM_EMAIL`), `STAFF_INVITATION_SETUP_URL`, and the invitation key-ring variables above. The setup URL must be HTTPS, except for loopback development URLs. The worker clears invitation ciphertext after successful delivery, consumption, revocation, or expiry. Keep old keys while any retryable invitation email still has encrypted token data.

## Manager invitations

`GET /staff/accounts` is manager-only and lists accounts in every status. Pagination uses `page` (default `1`) and `page_size` (default `20`, maximum `100`); `has_more` indicates whether another page exists. Optional `search` performs a case-insensitive substring search over email and display name. Example: `GET /staff/accounts?page=2&page_size=20&search=alex`.

`POST /staff/accounts` accepts `{ "email", "display_name", "role" }` and is restricted to managers. It creates the `INVITED` account, stores a digest and encrypted retry copy of its random setup token, and queues a `STAFF_INVITATION` email in one transaction. The endpoint returns `202 Accepted`; it does not claim the email was delivered. The browser must send the configured `Origin` and `X-Staff-Request: 1` on this and other cookie-authenticated mutations.

`POST /staff/accounts/{id}/invitation` resends setup to an invited account. It revokes the previous open invitation and cancels queued delivery before inserting a replacement invitation and email job in one transaction. A previously delivered link stops working as soon as this transaction commits.

The link expires after 24 hours. The frontend starts setup with `POST /staff/invitations/setup/start` and the invitation token, then completes it with `POST /staff/invitations/setup/complete`, sending the token, MFA challenge, six-digit TOTP code, and chosen password. The account becomes `ACTIVE` only after the password and TOTP verify together. The completion response returns ten recovery codes once; the new staff member then signs in normally. Since the link token is a bearer credential, the setup page should avoid third-party resources, use a restrictive referrer policy, and remove the token from the address bar after reading it.

Deactivation uses `POST /staff/accounts/{id}/deactivation`. It revokes sessions and open invitations transactionally, and refuses to disable the last active manager.

## One-time first-owner bootstrap

Apply Goose migrations first, then run the local command:

```sh
(cd backend && go run ./cmd/migrate up)
(cd backend && go run ./cmd/staff-bootstrap)
```

The command requires `DATABASE_URL`, `STAFF_BOOTSTRAP_SECRET` (at least 32 bytes), `STAFF_BOOTSTRAP_EMAIL`, `STAFF_BOOTSTRAP_DISPLAY_NAME`, `STAFF_BOOTSTRAP_PASSWORD`, and the MFA key-ring variables above. It prompts for the bootstrap secret without echoing it and compares it with the configured value before revealing the TOTP setup key. The secret is not stored in PostgreSQL or passed in process arguments. Run the command only from a trusted administrative shell. The database's unique email index and one-bootstrap-owner partial unique index are the final guards against duplicates and concurrent/repeated creation.

The command prints an `otpauth://` setup URI and its Base32 secret, then waits for a current six-digit code. It creates the manager only after the code verifies. On success it prints ten recovery codes once; save them securely. The database stores their SHA-256 digests, not the codes.

## Verify the sign-in and session flow

The HTTP integration test uses a dedicated disposable PostgreSQL database. Apply the migrations to that database, then run:

```sh
STAFF_HTTP_TEST_DATABASE_URL="$DATABASE_URL"; (cd backend && go test ./internal/staff/httpapi -run '^TestAuthHTTPFlowPostgres$' -v)
```

The test creates and removes its own fixture account. It covers password login, MFA challenge completion, secure session-cookie authentication, logout, and rejection of the revoked session.

The PostgreSQL invitation tests use a separate database because they create a temporary trigger to force an outbox failure and verify transaction rollback. With the repository's local PostgreSQL container running, create the disposable database and apply all Goose migrations once:

```sh
docker compose up -d db
docker compose exec -T db psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c 'CREATE DATABASE staff_invitation_test'
DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5433/staff_invitation_test?sslmode=disable' \
  (cd backend && go run ./cmd/migrate up)

STAFF_INVITATION_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:5433/staff_invitation_test?sslmode=disable" \
  (cd backend && go test ./internal/staff/postgres -run '^(TestCreateAndResendInvitationInvalidatePreviousLink|TestInvitationEnrollmentActivatesAccountOnce|TestExpiredInvitationCannotStartEnrollment|TestCreateInvitationRollsBackWhenEmailJobInsertFails)$' -v)
```

The database name must include `test`; the tests clean their account fixtures and remove the temporary trigger/function after the rollback case. CI creates and migrates this database independently from the auth HTTP integration database.

## Current implementation boundary

The initial-owner bootstrap, password/MFA sign-in/session path, manager invitations and resend, invitation email delivery, invitation acceptance/MFA enrollment, CSRF checks for cookie-authenticated mutations, and manager account deactivation are implemented. A distributed login rate limit remains separate implementation work.

