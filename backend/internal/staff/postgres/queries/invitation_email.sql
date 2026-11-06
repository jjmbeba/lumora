-- name: FailExpiredStaffInvitationEmailJobs :exec
UPDATE email_outbox
SET status = 'FAILED', lease_expires_at = NULL, sent_at = NULL,
    last_error_code = 'invitation_expired', updated_at = clock_timestamp()
WHERE kind = 'STAFF_INVITATION'
  AND status IN ('QUEUED', 'SENDING')
  AND staff_invitation_id IN (
    SELECT id FROM staff_invitations
    WHERE expires_at <= clock_timestamp()
      AND consumed_at IS NULL AND revoked_at IS NULL
  );

-- name: RevokeExpiredStaffInvitations :exec
UPDATE staff_invitations
SET revoked_at = clock_timestamp(), token_ciphertext = NULL, token_key_version = NULL
WHERE expires_at <= clock_timestamp()
  AND consumed_at IS NULL AND revoked_at IS NULL;

-- name: ClaimNextStaffInvitationEmail :one
WITH candidate AS (
    SELECT eo.id
    FROM email_outbox AS eo
    JOIN staff_invitations AS si ON si.id = eo.staff_invitation_id
    JOIN staff_accounts AS sa ON sa.id = si.staff_account_id
    WHERE eo.kind = 'STAFF_INVITATION'
      AND sa.status = 'INVITED'
      AND si.consumed_at IS NULL AND si.revoked_at IS NULL
      AND si.expires_at > clock_timestamp()
      AND si.token_ciphertext IS NOT NULL AND si.token_key_version IS NOT NULL
      AND ((eo.status = 'QUEUED' AND eo.next_attempt_at <= clock_timestamp())
        OR (eo.status = 'SENDING' AND eo.lease_expires_at <= clock_timestamp()))
    ORDER BY eo.next_attempt_at, eo.id
    LIMIT 1
    FOR UPDATE OF eo SKIP LOCKED
)
UPDATE email_outbox AS eo
SET status = 'SENDING',
    attempt_count = eo.attempt_count + 1,
    lease_expires_at = clock_timestamp() + (sqlc.arg(lease_micros)::bigint * interval '1 microsecond'),
    updated_at = clock_timestamp()
FROM candidate
WHERE eo.id = candidate.id
RETURNING eo.id, eo.staff_invitation_id, eo.recipient_email, eo.event_key, eo.attempt_count;

-- name: LoadStaffInvitationEmail :one
SELECT sa.email, sa.display_name, sa.role::text AS role,
       si.token_ciphertext, si.token_key_version, si.expires_at
FROM email_outbox AS eo
JOIN staff_invitations AS si ON si.id = eo.staff_invitation_id
JOIN staff_accounts AS sa ON sa.id = si.staff_account_id
WHERE eo.id = sqlc.arg(id)
  AND eo.kind = 'STAFF_INVITATION'
  AND eo.status = 'SENDING'
  AND eo.attempt_count = sqlc.arg(attempt_count)
  AND sa.status = 'INVITED'
  AND si.consumed_at IS NULL AND si.revoked_at IS NULL
  AND si.expires_at > clock_timestamp()
  AND si.token_ciphertext IS NOT NULL AND si.token_key_version IS NOT NULL;

-- name: MarkStaffInvitationEmailSent :one
UPDATE email_outbox
SET status = 'SENT', lease_expires_at = NULL, last_error_code = NULL,
    sent_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND kind = 'STAFF_INVITATION'
  AND status = 'SENDING' AND attempt_count = sqlc.arg(attempt_count)
RETURNING staff_invitation_id;

-- name: ClearStaffInvitationTokenCiphertext :exec
UPDATE staff_invitations
SET token_ciphertext = NULL, token_key_version = NULL
WHERE id = sqlc.arg(id);

-- name: RetryStaffInvitationEmail :one
UPDATE email_outbox
SET status = CASE WHEN sqlc.arg(terminal)::boolean THEN 'FAILED'::"EmailOutboxStatus" ELSE 'QUEUED'::"EmailOutboxStatus" END,
    next_attempt_at = CASE WHEN sqlc.arg(terminal)::boolean THEN clock_timestamp() ELSE sqlc.arg(next_attempt_at)::timestamptz END,
    lease_expires_at = NULL, last_error_code = 'DELIVERY_FAILED', updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND kind = 'STAFF_INVITATION'
  AND status = 'SENDING' AND attempt_count = sqlc.arg(attempt_count)
RETURNING id;
