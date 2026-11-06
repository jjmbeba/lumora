-- name: LoadTicketEmailItems :many
SELECT b.booking_reference, m.name AS movie_title, screening.start_time,
       auditorium.name AS auditorium_name, s.label AS seat_label, t.seat_id,
       t.credential_digest, t.credential_ciphertext, t.credential_key_version
FROM bookings AS b
JOIN tickets AS t ON t.booking_id = b.id
JOIN seats AS s ON s.id = t.seat_id
JOIN screenings AS screening ON screening.id = b.screening_id
JOIN movies AS m ON m.id = screening.movie_id
JOIN auditoriums AS auditorium ON auditorium.id = screening.auditorium_id
WHERE b.id = $1
ORDER BY t.seat_id;

-- name: ClaimNextTicketEmail :one
WITH candidate AS (
    SELECT id
    FROM email_outbox
    WHERE kind = 'BOOKING_TICKETS'
      AND ((status = 'QUEUED' AND next_attempt_at <= clock_timestamp())
        OR (status = 'SENDING' AND lease_expires_at <= clock_timestamp()))
    ORDER BY next_attempt_at, id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE email_outbox AS eo
SET status = 'SENDING',
    attempt_count = eo.attempt_count + 1,
    lease_expires_at = clock_timestamp() + (sqlc.arg(lease_micros)::bigint * interval '1 microsecond'),
    updated_at = clock_timestamp()
FROM candidate
WHERE eo.id = candidate.id
RETURNING eo.id, eo.booking_id, eo.recipient_email, eo.event_key, eo.attempt_count;

-- name: MarkTicketEmailSent :one
UPDATE email_outbox
SET status = 'SENT', lease_expires_at = NULL, last_error_code = NULL,
    sent_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'SENDING' AND attempt_count = sqlc.arg(attempt_count)
RETURNING id;

-- name: RetryTicketEmail :one
UPDATE email_outbox
SET status = CASE WHEN sqlc.arg(terminal)::boolean THEN 'FAILED'::"EmailOutboxStatus" ELSE 'QUEUED'::"EmailOutboxStatus" END,
    next_attempt_at = CASE WHEN sqlc.arg(terminal)::boolean THEN clock_timestamp() ELSE sqlc.arg(next_attempt_at)::timestamptz END,
    lease_expires_at = NULL, last_error_code = 'DELIVERY_FAILED', updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'SENDING' AND attempt_count = sqlc.arg(attempt_count)
RETURNING id;
