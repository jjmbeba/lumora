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

## Current implementation boundary

The sign-in/session path is wired into `cmd/booking-api`. Account bootstrap, authenticator enrollment, manager invitations/deactivation, role-protected staff operations, and a distributed login rate limit remain separate implementation work. Until bootstrap/enrollment is implemented, the sign-in routes have no supported path to create the first usable staff account.

