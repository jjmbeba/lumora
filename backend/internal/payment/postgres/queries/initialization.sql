-- name: GetLatestInitializationForBooking :one
SELECT pi.id, pi.status::text AS status, pi.payment_method::text AS payment_method,
       pi.paystack_reference, COALESCE(pi.authorization_url, '') AS authorization_url
FROM payment_initializations AS pi
JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
JOIN bookings AS b ON b.id = pa.booking_id
WHERE b.booking_reference = $1
ORDER BY pi.created_at DESC, pi.id DESC
LIMIT 1;

-- name: LockBookingForInitialization :one
SELECT id, status::text AS status, hold_expires_at > clock_timestamp() AS hold_active
FROM bookings
WHERE booking_reference = $1
FOR UPDATE;

-- name: LoadInitializationReceiptByKey :one
SELECT pi.id, pi.status::text AS status, pi.payment_method::text AS payment_method,
       pi.paystack_reference, COALESCE(pi.authorization_url, '') AS authorization_url,
       receipt.request_fingerprint, b.booking_reference,
       receipt.requested_payment_method::text AS requested_payment_method
FROM payment_initialization_receipts AS receipt
JOIN payment_initializations AS pi ON pi.id = receipt.payment_initialization_id
JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
JOIN bookings AS b ON b.id = pa.booking_id
WHERE receipt.idempotency_key = $1;

-- name: LoadUnresolvedInitializationForBooking :one
SELECT pi.id, pi.status::text AS status, pi.payment_method::text AS payment_method,
       pi.paystack_reference, COALESCE(pi.authorization_url, '') AS authorization_url
FROM payment_initializations AS pi
JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
JOIN bookings AS b ON b.id = pa.booking_id
WHERE b.booking_reference = $1
  AND pi.status IN ('QUEUED', 'SUBMITTING', 'READY', 'UNKNOWN', 'REVIEW_REQUIRED')
ORDER BY pi.created_at DESC, pi.id DESC
LIMIT 1;

-- name: LoadPaymentAttemptForBooking :one
SELECT id, status::text AS status
FROM payment_attempts
WHERE booking_id = $1
FOR UPDATE;

-- name: GetBookingPriceSnapshotTotals :one
SELECT COALESCE(SUM(unit_price_amount), 0)::bigint AS total_amount,
       COALESCE(MIN(currency), '')::text AS currency,
       COUNT(*)::bigint AS seat_count
FROM bookings_seats
WHERE booking_id = $1;

-- name: LockExpiredBookingByReference :one
SELECT id
FROM bookings
WHERE booking_reference = $1
  AND status = 'RESERVED'
  AND hold_expires_at <= clock_timestamp()
FOR UPDATE;

-- name: LoadLatestInitializationForAttempt :one
SELECT id, status::text AS status, payment_method::text AS payment_method,
       paystack_reference, COALESCE(authorization_url, '') AS authorization_url
FROM payment_initializations
WHERE payment_attempt_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetBookingSeatIDs :many
SELECT seat_id
FROM bookings_seats
WHERE booking_id = $1
ORDER BY seat_id;

-- name: ClaimNextPaymentInitialization :one
SELECT pi.id AS initialization_id, pi.status::text AS previous_status,
       pi.payment_method::text AS payment_method, pi.paystack_reference,
       pa.currency, pa.amount, b.customer_email, b.customer_phone
FROM payment_initializations AS pi
JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
JOIN bookings AS b ON b.id = pa.booking_id
WHERE (pi.status IN ('QUEUED', 'UNKNOWN', 'READY')
       AND (pi.next_attempt_at IS NULL OR pi.next_attempt_at <= clock_timestamp()))
   OR (pi.status = 'SUBMITTING' AND pi.lease_expires_at <= clock_timestamp())
ORDER BY COALESCE(pi.next_attempt_at, pi.lease_expires_at, pi.created_at), pi.id
LIMIT 1
FOR UPDATE OF pi SKIP LOCKED;

-- name: LockClaimForPendingVerification :one
SELECT pa.id AS payment_attempt_id, pa.status::text AS payment_status,
       b.id AS booking_id, b.status::text AS booking_status,
       b.hold_expires_at, b.hold_expires_at <= clock_timestamp() AS hold_expired,
       EXISTS (SELECT 1 FROM webhook_inbox wi
               WHERE wi.reference = pi.paystack_reference AND wi.event_type = 'charge.success') AS success_webhook_recorded
FROM payment_initializations pi
JOIN payment_attempts pa ON pa.id = pi.payment_attempt_id
JOIN bookings b ON b.id = pa.booking_id
WHERE pi.id = $1 AND pi.status = 'SUBMITTING' AND pi.worker_attempt_count = $2
FOR UPDATE OF pi, pa, b;

-- name: CreatePaymentInitialization :one
INSERT INTO payment_initializations (
    payment_attempt_id, paystack_reference, payment_method, status, created_at, updated_at
)
VALUES ($1, $2, $3, 'QUEUED', clock_timestamp(), clock_timestamp())
RETURNING id, status::text AS status, payment_method::text AS payment_method,
          paystack_reference, COALESCE(authorization_url, '') AS authorization_url;

-- name: SavePaymentInitializationReceipt :execrows
INSERT INTO payment_initialization_receipts (
    idempotency_key, request_fingerprint, requested_payment_method, payment_initialization_id
)
VALUES ($1, $2, $3, $4)
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: CreatePaymentAttempt :one
INSERT INTO payment_attempts (booking_id, status, amount, currency, created_at)
VALUES ($1, 'PENDING', $2, $3, clock_timestamp())
ON CONFLICT (booking_id) DO NOTHING
RETURNING id, status::text AS status;

-- name: CancelExpiredBooking :exec
UPDATE bookings
SET status = 'CANCELLED'
WHERE id = $1 AND status = 'RESERVED';

-- name: CancelExpiredBookingAfterRejectedInitialization :one
UPDATE bookings SET status = 'CANCELLED'
WHERE bookings.id = $1 AND EXISTS (
    SELECT 1 FROM payment_attempts AS pa
    WHERE pa.booking_id = $1 AND pa.status = 'PENDING'
      AND (SELECT pi.status FROM payment_initializations AS pi
           WHERE pi.payment_attempt_id = pa.id
           ORDER BY pi.created_at DESC, pi.id DESC LIMIT 1) = 'REJECTED'
)
RETURNING bookings.id;

-- name: CancelExpiredPaymentAttempt :exec
UPDATE payment_attempts
SET status = 'CANCELLED'
WHERE booking_id = $1 AND status = 'PENDING';

-- name: ReleaseBookingSeatClaims :exec
DELETE FROM seat_claims WHERE booking_id = $1;

-- name: InsertTicket :exec
INSERT INTO tickets (
    booking_id, seat_id, credential_digest, credential_ciphertext, credential_key_version
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (booking_id, seat_id) DO NOTHING;

-- name: EnqueueTicketEmail :exec
INSERT INTO email_outbox (event_key, kind, recipient_email, booking_id)
VALUES ($1, 'BOOKING_TICKETS', $2, $3)
ON CONFLICT (booking_id) WHERE kind = 'BOOKING_TICKETS' DO NOTHING;

-- name: RecordWebhookEvent :exec
INSERT INTO webhook_inbox (event_type, transaction_id, reference, receipt_time, provider_paid_at, provider_status)
VALUES ($1, $2, $3, clock_timestamp(), $4, $5)
ON CONFLICT (reference) DO NOTHING;

-- name: ScheduleVerificationFromWebhook :exec
UPDATE payment_initializations
SET next_attempt_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE paystack_reference = $1 AND status IN ('READY', 'UNKNOWN');

-- name: MoveCancelledInitializationToReview :one
UPDATE payment_initializations
SET status = 'REVIEW_REQUIRED', lease_expires_at = NULL,
    next_attempt_at = NULL, updated_at = clock_timestamp()
WHERE paystack_reference = $1 AND status = 'CANCELLED'
RETURNING payment_attempt_id;

-- name: MoveBookingToPaymentReview :exec
UPDATE bookings
SET status = 'PAYMENT_REVIEW'
WHERE bookings.id = (SELECT pa.booking_id FROM payment_attempts AS pa WHERE pa.id = $1)
  AND bookings.status <> 'CANCELLED';

-- name: LockClaimPaymentAndBooking :one
SELECT pa.id AS payment_attempt_id, pa.status::text AS payment_status,
       b.id AS booking_id, b.status::text AS booking_status
FROM payment_initializations pi
JOIN payment_attempts pa ON pa.id = pi.payment_attempt_id
JOIN bookings b ON b.id = pa.booking_id
WHERE pi.id = $1 AND pi.status = 'SUBMITTING' AND pi.worker_attempt_count = $2
FOR UPDATE OF pi, pa, b;

-- name: LockClaimForPaidTransition :one
SELECT pa.id AS payment_attempt_id, b.id AS booking_id,
       pa.status::text AS payment_status, b.status::text AS booking_status,
       b.customer_email
FROM payment_initializations pi
JOIN payment_attempts pa ON pa.id = pi.payment_attempt_id
JOIN bookings b ON b.id = pa.booking_id
WHERE pi.id = $1 AND pi.status = 'SUBMITTING' AND pi.worker_attempt_count = $2
FOR UPDATE OF pi, pa, b;

-- name: ClaimPaymentInitialization :one
UPDATE payment_initializations
SET status = 'SUBMITTING',
    worker_attempt_count = worker_attempt_count + 1,
    next_attempt_at = NULL,
lease_expires_at = clock_timestamp() + (sqlc.arg(lease_micros)::double precision * interval '1 microsecond'),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)
RETURNING worker_attempt_count;

-- name: MarkPaymentInitializationReady :execrows
UPDATE payment_initializations
SET status = 'READY',
    authorization_url = sqlc.arg(authorization_url),
    lease_expires_at = NULL,
    next_attempt_at = CASE WHEN EXISTS (
        SELECT 1 FROM webhook_inbox AS wi
        WHERE wi.reference = payment_initializations.paystack_reference
          AND wi.event_type = 'charge.success'
    ) THEN clock_timestamp() ELSE LEAST(
        sqlc.arg(next_attempt_at)::timestamptz,
        (SELECT b.hold_expires_at
         FROM payment_attempts AS pa
         JOIN bookings AS b ON b.id = pa.booking_id
         WHERE pa.id = payment_initializations.payment_attempt_id)
    ) END,
    updated_at = clock_timestamp()
WHERE payment_initializations.id = sqlc.arg(id) AND status = 'SUBMITTING' AND worker_attempt_count = sqlc.arg(worker_attempt_count);

-- name: SchedulePaymentVerification :execrows
UPDATE payment_initializations
SET status = sqlc.arg(status)::"PaymentInitializationStatus",
    lease_expires_at = NULL,
    next_attempt_at = CASE WHEN sqlc.arg(immediate)::boolean THEN clock_timestamp() ELSE LEAST(sqlc.arg(next_attempt_at)::timestamptz, sqlc.arg(hold_expires_at)::timestamptz) END,
    updated_at = clock_timestamp()
WHERE payment_initializations.id = sqlc.arg(id) AND status = 'SUBMITTING' AND worker_attempt_count = sqlc.arg(worker_attempt_count);

-- name: UpdateClaimedPaymentInitializationStatus :one
UPDATE payment_initializations
SET status = sqlc.arg(status)::"PaymentInitializationStatus",
    lease_expires_at = NULL,
    next_attempt_at = NULL,
    updated_at = clock_timestamp()
WHERE payment_initializations.id = sqlc.arg(id) AND status = 'SUBMITTING' AND worker_attempt_count = sqlc.arg(worker_attempt_count)
RETURNING payment_attempt_id;

-- name: UpdatePaymentAttemptStatus :one
UPDATE payment_attempts
SET status = sqlc.arg(status)::"PaymentStatus"
WHERE payment_attempts.id = sqlc.arg(id) AND status = sqlc.arg(previous_status)::"PaymentStatus"
RETURNING id;

-- name: UpdateBookingStatus :one
UPDATE bookings
SET status = sqlc.arg(status)::"BookingStatus"
WHERE bookings.id = sqlc.arg(id) AND status = sqlc.arg(previous_status)::"BookingStatus"
RETURNING id;

-- name: MarkPaymentInitializationRejected :execrows
UPDATE payment_initializations
SET status = 'REJECTED', rejection_reason = sqlc.arg(rejection_reason),
    lease_expires_at = NULL, next_attempt_at = NULL, updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'SUBMITTING'
  AND worker_attempt_count = sqlc.arg(worker_attempt_count);

-- name: MarkPaymentInitializationCancelled :execrows
UPDATE payment_initializations
SET status = 'CANCELLED', lease_expires_at = NULL,
    next_attempt_at = NULL, updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'SUBMITTING'
  AND worker_attempt_count = sqlc.arg(worker_attempt_count);
