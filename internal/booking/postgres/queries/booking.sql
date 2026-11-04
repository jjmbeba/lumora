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
