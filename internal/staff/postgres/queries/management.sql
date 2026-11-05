-- name: CreateInvitedStaffAccount :one
INSERT INTO staff_accounts (
    email, display_name, role, status, created_by_staff_account_id
)
VALUES (
    sqlc.arg(email), sqlc.arg(display_name), sqlc.arg(role)::"StaffRole",
    'INVITED', sqlc.arg(created_by_staff_account_id)
)
RETURNING id, email, display_name, role::text AS role, status::text AS status;

-- name: CreateStaffInvitation :one
INSERT INTO staff_invitations (
    staff_account_id, token_digest, token_ciphertext, token_key_version,
    expires_at, created_by_staff_account_id
)
VALUES (
    sqlc.arg(staff_account_id), sqlc.arg(token_digest), sqlc.arg(token_ciphertext),
    sqlc.arg(token_key_version), sqlc.arg(expires_at), sqlc.arg(created_by_staff_account_id)
)
RETURNING id;

-- name: CreateStaffInvitationEmailJob :exec
INSERT INTO email_outbox (
    event_key, kind, recipient_email, staff_invitation_id
)
VALUES (
    sqlc.arg(event_key), 'STAFF_INVITATION', sqlc.arg(recipient_email), sqlc.arg(staff_invitation_id)
);

-- name: LoadInvitedStaffAccount :one
SELECT id, email, display_name, role::text AS role, status::text AS status
FROM staff_accounts
WHERE id = sqlc.arg(id) AND status = 'INVITED';

-- name: LockInvitedStaffAccount :one
SELECT id, email, display_name, role::text AS role, status::text AS status
FROM staff_accounts
WHERE id = sqlc.arg(id) AND status = 'INVITED'
FOR UPDATE;

-- name: LoadStaffInvitation :one
SELECT sa.id, sa.email, sa.display_name, sa.role::text AS role, sa.status::text AS status
FROM staff_invitations AS si
JOIN staff_accounts AS sa ON sa.id = si.staff_account_id
WHERE si.token_digest = sqlc.arg(token_digest)
  AND si.consumed_at IS NULL AND si.revoked_at IS NULL
  AND si.expires_at > clock_timestamp()
  AND sa.status = 'INVITED';

-- name: LockStaffInvitationForSetup :one
SELECT si.id AS invitation_id, si.staff_account_id,
       sa.email, sa.status::text AS account_status,
       si.expires_at
FROM staff_invitations AS si
JOIN staff_accounts AS sa ON sa.id = si.staff_account_id
WHERE si.token_digest = sqlc.arg(token_digest)
  AND si.consumed_at IS NULL AND si.revoked_at IS NULL
  AND si.expires_at > clock_timestamp()
  AND sa.status = 'INVITED'
FOR UPDATE OF si, sa;

-- name: ConsumePendingStaffEnrollmentChallenges :exec
UPDATE staff_auth_challenges
SET consumed_at = clock_timestamp()
WHERE staff_account_id = sqlc.arg(staff_account_id)
  AND purpose = 'MFA_ENROLLMENT' AND consumed_at IS NULL;

-- name: SetStaffInvitationMFASecret :execrows
UPDATE staff_accounts
SET mfa_secret_ciphertext = sqlc.arg(mfa_secret_ciphertext),
    mfa_key_version = sqlc.arg(mfa_key_version), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'INVITED';

-- name: ActivateStaffInvitationAccount :execrows
UPDATE staff_accounts
SET status = 'ACTIVE', password_hash = sqlc.arg(password_hash),
    mfa_enabled_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'INVITED'
  AND mfa_secret_ciphertext IS NOT NULL AND mfa_key_version IS NOT NULL;

-- name: ConsumeStaffInvitation :execrows
UPDATE staff_invitations
SET consumed_at = clock_timestamp(), token_ciphertext = NULL, token_key_version = NULL
WHERE id = sqlc.arg(id) AND consumed_at IS NULL AND revoked_at IS NULL
  AND expires_at > clock_timestamp();
