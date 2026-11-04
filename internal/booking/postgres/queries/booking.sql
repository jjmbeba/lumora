-- name: LoadBookingByIdempotencyKey :one
SELECT b.request_fingerprint,
       b.booking_reference,
       b.status::text AS status,
       b.hold_expires_at,
       COALESCE(SUM(bs.unit_price_amount), 0)::bigint AS total_amount_minor,
       COALESCE(MIN(bs.currency), '')::text AS currency,
       COUNT(bs.seat_id)::bigint AS snapshot_count
FROM bookings AS b
LEFT JOIN bookings_seats AS bs ON bs.booking_id = b.id
WHERE b.idempotency_key = $1
GROUP BY b.id, b.request_fingerprint, b.booking_reference, b.status, b.hold_expires_at;

-- name: GetBookingDetails :many
SELECT b.id AS booking_id,
       b.booking_reference,
       b.status::text AS status,
       b.hold_expires_at,
       s.currency,
       COALESCE(SUM(bs.unit_price_amount) OVER (PARTITION BY b.id), 0)::bigint AS total_amount_minor,
       m.name AS movie_title,
       a.name AS auditorium_name,
       s.start_time,
       s.end_time,
       bs.seat_id,
       seat.label AS seat_label,
       bs.unit_price_amount AS seat_price_amount_minor,
       ticket.credential_digest,
       ticket.credential_ciphertext,
       ticket.credential_key_version
FROM bookings AS b
JOIN screenings AS s ON s.id = b.screening_id
JOIN movies AS m ON m.id = s.movie_id
JOIN auditoriums AS a ON a.id = s.auditorium_id
JOIN bookings_seats AS bs ON bs.booking_id = b.id
JOIN seats AS seat ON seat.id = bs.seat_id
LEFT JOIN tickets AS ticket
  ON ticket.booking_id = b.id
 AND ticket.seat_id = bs.seat_id
 AND b.status = 'BOOKED'
WHERE b.booking_reference = $1
ORDER BY seat.label;

-- name: CreateBooking :one
WITH booking_time AS MATERIALIZED (
    SELECT clock_timestamp() AS created_at
)
INSERT INTO bookings (
    booking_reference, idempotency_key, request_fingerprint, customer_email,
    customer_phone, status, screening_id, created_at, hold_expires_at
)
SELECT $1, $2, $3, $4, $5, 'RESERVED', $6,
       booking_time.created_at,
       booking_time.created_at + INTERVAL '5 minutes'
FROM booking_time
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id, hold_expires_at;

-- name: CreateBookingSeatSnapshots :one
WITH inserted AS (
    INSERT INTO bookings_seats (booking_id, seat_id, screening_id, unit_price_amount, currency)
    SELECT $1, ss.seat_id, ss.screening_id, ss.price_amount, s.currency
    FROM screening_seats AS ss
    JOIN screenings AS s ON s.id = ss.screening_id
    WHERE ss.screening_id = $2
      AND ss.seat_id = ANY(sqlc.arg(seat_ids)::int[])
    RETURNING unit_price_amount, currency, seat_id
)
SELECT COALESCE(SUM(unit_price_amount), 0)::bigint AS total_price,
       COALESCE(MIN(currency), '')::text AS currency,
       COUNT(seat_id)::bigint AS snapshot_count
FROM inserted;

-- name: ClaimBookingSeats :execrows
INSERT INTO seat_claims (screening_id, seat_id, booking_id)
SELECT bs.screening_id, bs.seat_id, bs.booking_id
FROM bookings_seats AS bs
WHERE bs.booking_id = $1
ON CONFLICT (screening_id, seat_id) DO NOTHING;
