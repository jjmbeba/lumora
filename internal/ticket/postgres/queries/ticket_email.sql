-- name: LoadTicketEmailItems :many
SELECT b.booking_reference, s.label AS seat_label, t.seat_id,
       t.credential_digest, t.credential_ciphertext, t.credential_key_version
FROM bookings AS b
JOIN tickets AS t ON t.booking_id = b.id
JOIN seats AS s ON s.id = t.seat_id
WHERE b.id = $1
ORDER BY t.seat_id;
