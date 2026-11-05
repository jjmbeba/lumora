# Staff authentication implementation

The accepted staff-authentication decisions are in [decisions.md](decisions.md). This page records the initial Go implementation boundary and the configuration needed to run it.

## Sign-in flow

1. `POST /staff/auth/login` accepts an email and password. A valid password creates a five-minute `MFA_SIGN_IN` challenge; it does not create a staff session.
2. `POST /staff/auth/mfa` accepts that challenge and either a six-digit TOTP code or one recovery code. The challenge is consumed and a session row is created in one PostgreSQL transaction.
3. The response sets the opaque session token in an `HttpOnly`, `Secure`, `SameSite=Strict` cookie. PostgreSQL stores only its SHA-256 digest. Protected handlers use `AuthHandler.RequireSession`, which checks account status, absolute expiry, and the 15-minute idle limit on each request.
4. `POST /staff/auth/logout` revokes the session and clears the cookie.

Sessions expire after eight hours even when active. An MFA challenge allows at most five failed attempts. Recovery-code consumption and challenge consumption are atomic with session creation.

## MFA encryption configuration

The booking API requires the following environment variables to configure its versioned AES-256 key ring:

```text
STAFF_MFA_CURRENT_KEY_VERSION=1
STAFF_MFA_KEY_VERSIONS=1
STAFF_MFA_KEY_1=<standard-base64-encoded 32-byte key>
```

Keep the key material outside source control and PostgreSQL. Retain old key versions while stored TOTP secrets still use them. Generate keys with a cryptographically secure random source; do not reuse the ticket credential or booking verification keys.

## One-time first-owner bootstrap

Apply Goose migrations first, then run the local command:

```sh
go run ./cmd/migrate up
go run ./cmd/staff-bootstrap
```

The command requires `DATABASE_URL`, `STAFF_BOOTSTRAP_SECRET` (at least 32 bytes), `STAFF_BOOTSTRAP_EMAIL`, `STAFF_BOOTSTRAP_DISPLAY_NAME`, `STAFF_BOOTSTRAP_PASSWORD`, and the MFA key-ring variables above. It prompts for the bootstrap secret without echoing it and compares it with the configured value before revealing the TOTP setup key. The secret is not stored in PostgreSQL or passed in process arguments. Run the command only from a trusted administrative shell. The database's unique email index and one-bootstrap-owner partial unique index are the final guards against duplicates and concurrent/repeated creation.

The command prints an `otpauth://` setup URI and its Base32 secret, then waits for a current six-digit code. It creates the manager only after the code verifies. On success it prints ten recovery codes once; save them securely. The database stores their SHA-256 digests, not the codes.

## Current implementation boundary

The initial-owner bootstrap and password/MFA sign-in/session path are implemented. Manager invitations/deactivation, role-protected staff operations, and a distributed login rate limit remain separate implementation work.

