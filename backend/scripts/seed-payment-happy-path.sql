-- Local test fixture only. Run against the Compose test database, not production.
-- Creates a reserved booking with one seat and a queued Paystack initialization.
DO $$
DECLARE
    created_at_value timestamptz := clock_timestamp();
    id_auditorium integer;
    id_movie integer;
    id_seat integer;
    id_screening integer;
    id_booking integer;
    id_payment_attempt integer;
    id_initialization integer;
    payment_reference text := 'local-happy-path-' || to_char(clock_timestamp(), 'YYYYMMDDHH24MISSUS');
BEGIN
    INSERT INTO auditoriums (name, created_at)
    VALUES ('Local Happy Path Auditorium', created_at_value)
    RETURNING id INTO id_auditorium;

    INSERT INTO movies (name, description, runtime_minutes, created_at)
    VALUES ('Local Happy Path Movie', 'Temporary local payment test fixture', 90, created_at_value)
    RETURNING id INTO id_movie;

    INSERT INTO seats (label, auditorium_id, created_at)
    VALUES ('HP-1', id_auditorium, created_at_value)
    RETURNING id INTO id_seat;

    INSERT INTO screenings (movie_id, auditorium_id, currency, start_time, end_time, created_at)
    VALUES (id_movie, id_auditorium, 'KES',
        created_at_value + interval '2 hours', created_at_value + interval '4 hours', created_at_value)
    RETURNING id INTO id_screening;

    INSERT INTO screening_seats (screening_id, seat_id, auditorium_id, price_amount)
    VALUES (id_screening, id_seat, id_auditorium, 2500);

    INSERT INTO bookings (
        booking_reference, idempotency_key, request_fingerprint,
        customer_email, customer_phone, status, screening_id, hold_expires_at, created_at
    ) VALUES (
        'local-booking-' || to_char(created_at_value, 'YYYYMMDDHH24MISSUS'),
        'local-booking-key-' || to_char(created_at_value, 'YYYYMMDDHH24MISSUS'),
        'local-happy-path-fingerprint',
        'sandbox.buyer@example.com', '+254700000000', 'RESERVED', id_screening,
        created_at_value + interval '30 minutes', created_at_value
    ) RETURNING id INTO id_booking;

    INSERT INTO bookings_seats (booking_id, seat_id, screening_id, unit_price_amount, currency)
    VALUES (id_booking, id_seat, id_screening, 2500, 'KES');

    INSERT INTO seat_claims (screening_id, seat_id, booking_id)
    VALUES (id_screening, id_seat, id_booking);

    INSERT INTO payment_attempts (booking_id, status, amount, currency, created_at)
    VALUES (id_booking, 'PENDING', 2500, 'KES', created_at_value)
    RETURNING id INTO id_payment_attempt;

    INSERT INTO payment_initializations (
        payment_attempt_id, paystack_reference, payment_method, status,
        worker_attempt_count,
        next_attempt_at, lease_expires_at, created_at, updated_at
    ) VALUES (
        id_payment_attempt, payment_reference, 'CARD', 'QUEUED',
        0, created_at_value, NULL, created_at_value, created_at_value
    ) RETURNING id INTO id_initialization;

    INSERT INTO payment_initialization_receipts (
        idempotency_key, request_fingerprint, requested_payment_method, payment_initialization_id
    ) VALUES (
        'local-initialization-key-' || to_char(created_at_value, 'YYYYMMDDHH24MISSUS'),
        'local-happy-path-fingerprint', 'CARD', id_initialization
    );
END $$;

SELECT pi.id AS initialization_id,
       pi.paystack_reference,
       pi.status AS initialization_status,
       pa.status AS payment_status,
       b.status AS booking_status,
       b.hold_expires_at
FROM payment_initializations pi
JOIN payment_attempts pa ON pa.id = pi.payment_attempt_id
JOIN bookings b ON b.id = pa.booking_id
WHERE pi.paystack_reference LIKE 'local-happy-path-%'
ORDER BY pi.created_at DESC
LIMIT 1;
