-- name: CreateBootstrapOwner :one
INSERT INTO staff_accounts (
    email, display_name, role, status, password_hash, mfa_secret_ciphertext,
    mfa_key_version, mfa_enabled_at, is_bootstrap_owner
)
VALUES (
    sqlc.arg(email), sqlc.arg(display_name), 'MANAGER', 'ACTIVE',
    sqlc.arg(password_hash), sqlc.arg(mfa_secret_ciphertext),
    sqlc.arg(mfa_key_version), clock_timestamp(), true
)
RETURNING id, email, display_name, role::text AS role, status::text AS status;

-- name: BootstrapOwnerExists :one
SELECT EXISTS (
    SELECT 1 FROM staff_accounts WHERE is_bootstrap_owner
);

-- name: FindLoginCredentials :one
SELECT id, email, display_name, role::text AS role, status::text AS status,
       password_hash, mfa_secret_ciphertext, mfa_key_version,
       (mfa_enabled_at IS NOT NULL)::boolean AS mfa_enabled
FROM staff_accounts
WHERE email = sqlc.arg(email);

-- name: InsertRecoveryCode :exec
INSERT INTO staff_recovery_codes (staff_account_id, code_digest)
VALUES (sqlc.arg(staff_account_id), sqlc.arg(code_digest));

-- name: CreateAuthChallenge :exec
INSERT INTO staff_auth_challenges (
    staff_account_id, token_digest, purpose, expires_at
)
VALUES (
    sqlc.arg(staff_account_id), sqlc.arg(token_digest),
    sqlc.arg(purpose), sqlc.arg(expires_at)
);

-- name: LockAuthChallenge :one
SELECT c.id AS challenge_id, c.staff_account_id, c.purpose,
       c.failed_attempts, c.expires_at, c.consumed_at,
       a.status::text AS account_status, a.role::text AS account_role,
       a.email, a.display_name, a.mfa_secret_ciphertext, a.mfa_key_version
FROM staff_auth_challenges AS c
JOIN staff_accounts AS a ON a.id = c.staff_account_id
WHERE c.token_digest = sqlc.arg(token_digest)
  AND c.consumed_at IS NULL AND c.expires_at > clock_timestamp()
  AND c.failed_attempts < 5
FOR UPDATE OF c, a;

-- name: LoadAuthChallenge :one
SELECT c.id AS challenge_id, c.staff_account_id, c.purpose,
       c.failed_attempts, c.expires_at, c.consumed_at,
       a.status::text AS account_status, a.role::text AS account_role,
       a.email, a.display_name, a.mfa_secret_ciphertext, a.mfa_key_version
FROM staff_auth_challenges AS c
JOIN staff_accounts AS a ON a.id = c.staff_account_id
WHERE c.token_digest = sqlc.arg(token_digest)
  AND c.consumed_at IS NULL AND c.expires_at > clock_timestamp()
  AND c.failed_attempts < 5;

-- name: RecordAuthChallengeFailure :execrows
UPDATE staff_auth_challenges
SET failed_attempts = failed_attempts + 1,
    consumed_at = CASE WHEN failed_attempts + 1 >= 5 THEN clock_timestamp() ELSE consumed_at END
WHERE id = sqlc.arg(id) AND consumed_at IS NULL AND expires_at > clock_timestamp()
  AND failed_attempts < 5;

-- name: ConsumeAuthChallenge :execrows
UPDATE staff_auth_challenges
SET consumed_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND consumed_at IS NULL AND expires_at > clock_timestamp()
  AND failed_attempts < 5;

-- name: ConsumeRecoveryCode :one
UPDATE staff_recovery_codes
SET consumed_at = clock_timestamp()
WHERE staff_account_id = sqlc.arg(staff_account_id)
  AND code_digest = sqlc.arg(code_digest)
  AND consumed_at IS NULL
RETURNING id;

-- name: CreateStaffSession :one
INSERT INTO staff_sessions (staff_account_id, token_digest, expires_at)
VALUES (sqlc.arg(staff_account_id), sqlc.arg(token_digest), sqlc.arg(expires_at))
RETURNING id, expires_at;

-- name: AuthenticateStaffSession :one
UPDATE staff_sessions AS s
SET last_activity_at = clock_timestamp()
FROM staff_accounts AS a
WHERE s.token_digest = sqlc.arg(token_digest)
  AND a.id = s.staff_account_id
  AND a.status = 'ACTIVE'
  AND s.revoked_at IS NULL
  AND s.expires_at > clock_timestamp()
  AND s.last_activity_at > clock_timestamp() - interval '15 minutes'
RETURNING a.id AS account_id, a.email, a.display_name, a.role::text AS role,
          s.expires_at;

-- name: RevokeStaffSession :execrows
UPDATE staff_sessions
SET revoked_at = clock_timestamp()
WHERE token_digest = sqlc.arg(token_digest) AND revoked_at IS NULL;

-- name: RevokeStaffAccountSessions :exec
UPDATE staff_sessions
SET revoked_at = clock_timestamp()
WHERE staff_account_id = sqlc.arg(staff_account_id) AND revoked_at IS NULL;
