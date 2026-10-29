-- Move existing initialization request identities into a receipt per request.
-- Fresh schemas already have receipts; rerunning this migration is safe.
BEGIN;

LOCK TABLE payment_initializations IN ACCESS EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS payment_initialization_receipts (
    idempotency_key varchar PRIMARY KEY,
    request_fingerprint varchar NOT NULL,
    requested_payment_method "PaymentMethod" NOT NULL,
    payment_initialization_id integer NOT NULL REFERENCES payment_initializations(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

DO $$
DECLARE
    has_key boolean;
    has_fingerprint boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'payment_initializations'
          AND column_name = 'idempotency_key'
    ), EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'payment_initializations'
          AND column_name = 'request_fingerprint'
    ) INTO has_key, has_fingerprint;

    IF has_key <> has_fingerprint THEN
        RAISE EXCEPTION 'Cannot migrate initialization receipts: legacy identity columns are incomplete';
    END IF;

    IF has_key THEN
        INSERT INTO payment_initialization_receipts
            (idempotency_key, request_fingerprint, requested_payment_method, payment_initialization_id, created_at)
        SELECT idempotency_key, request_fingerprint, payment_method, id, created_at
        FROM payment_initializations;

        ALTER TABLE payment_initializations DROP COLUMN idempotency_key;
        ALTER TABLE payment_initializations DROP COLUMN request_fingerprint;
    END IF;
END $$;

COMMIT;
