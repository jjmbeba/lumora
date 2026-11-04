-- name: GetLatestInitializationForBooking :one
SELECT pi.id, pi.status::text AS status, pi.payment_method::text AS payment_method,
       pi.paystack_reference, COALESCE(pi.authorization_url, '') AS authorization_url
FROM payment_initializations AS pi
JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
JOIN bookings AS b ON b.id = pa.booking_id
WHERE b.booking_reference = $1
ORDER BY pi.id DESC
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
ORDER BY pi.id DESC
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
ORDER BY id DESC
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
