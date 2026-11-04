-- name: LockBookingForEmailVerificationChallenge :one
SELECT id, customer_email
FROM bookings
WHERE booking_reference = $1
FOR UPDATE;

-- name: GetEmailVerificationSendLimits :one
WITH database_time AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now
)
SELECT database_time.now AS database_now,
       COUNT(attempt.id)::int AS send_count,
       COALESCE(MAX(attempt.created_at), database_time.now - INTERVAL '1 hour')::timestamptz AS last_sent_at
FROM database_time
LEFT JOIN booking_verification_send_attempts AS attempt
       ON attempt.booking_id = $1
      AND attempt.created_at > database_time.now - INTERVAL '1 hour'
GROUP BY database_time.now;

-- name: LockBookingForEmailVerification :one
SELECT id
FROM bookings
WHERE booking_reference = $1
FOR UPDATE;

-- name: LockEmailVerificationChallenge :one
SELECT code_digest,
       failed_attempts,
       expires_at > clock_timestamp() AS is_unexpired,
       (consumed_at IS NULL)::boolean AS is_unconsumed
FROM booking_verification_challenges
WHERE booking_id = $1
FOR UPDATE;

-- name: ValidateBookingToken :one
SELECT EXISTS (
    SELECT 1
    FROM booking_tokens AS token
    JOIN bookings AS booking ON booking.id = token.booking_id
    WHERE booking.booking_reference = $1
      AND token.token_digest = $2
      AND token.revoked_at IS NULL
      AND token.expires_at > clock_timestamp()
) AS valid;

-- name: PruneBookingEmailVerificationSendAttempts :exec
DELETE FROM booking_verification_send_attempts
WHERE booking_id = sqlc.arg(booking_id)
  AND created_at <= sqlc.arg(prune_before)::timestamptz - INTERVAL '1 hour';

-- name: UpsertBookingEmailVerificationChallenge :exec
INSERT INTO booking_verification_challenges (
    booking_id, code_digest, expires_at, failed_attempts, consumed_at, updated_at
)
VALUES ($1, $2, $3, 0, NULL, clock_timestamp())
ON CONFLICT (booking_id) DO UPDATE
SET code_digest = EXCLUDED.code_digest,
    expires_at = EXCLUDED.expires_at,
    failed_attempts = 0,
    consumed_at = NULL,
    updated_at = clock_timestamp();

-- name: RecordBookingEmailVerificationSendAttempt :exec
INSERT INTO booking_verification_send_attempts (booking_id, created_at)
VALUES ($1, clock_timestamp());

-- name: RecordRejectedBookingEmailVerificationAttempt :exec
UPDATE booking_verification_challenges
SET failed_attempts = failed_attempts + 1,
    consumed_at = CASE
        WHEN failed_attempts + 1 >= $2 THEN clock_timestamp()
        ELSE consumed_at
    END,
    updated_at = clock_timestamp()
WHERE booking_id = $1;

-- name: ConsumeBookingEmailVerificationChallenge :execrows
UPDATE booking_verification_challenges
SET consumed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE booking_id = $1
  AND consumed_at IS NULL
  AND expires_at > clock_timestamp();

-- name: CreateBookingToken :exec
INSERT INTO booking_tokens (booking_id, token_digest, expires_at)
VALUES ($1, $2, $3);
