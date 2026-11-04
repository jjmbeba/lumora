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
