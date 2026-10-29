-- Move currency from individual screening-seat prices to the screening.
-- Existing data is migrated only when every screening has priced seats and
-- all of its seat-price rows (and historical booking snapshots) agree.
-- Fresh databases already have the target shape from migration.sql, so this
-- script is a no-op there apart from ensuring the named index/foreign key.
DO $$
DECLARE
    screenings_has_currency boolean;
    screening_seats_has_currency boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'screenings'
          AND column_name = 'currency'
    ) INTO screenings_has_currency;

    SELECT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'screening_seats'
          AND column_name = 'currency'
    ) INTO screening_seats_has_currency;

    IF NOT screenings_has_currency AND screening_seats_has_currency THEN
        IF EXISTS (
            SELECT 1
            FROM screening_seats
            GROUP BY screening_id
            HAVING COUNT(DISTINCT currency) > 1
        ) THEN
            RAISE EXCEPTION 'Cannot migrate screening currency: at least one screening has seat prices in multiple currencies';
        END IF;

        IF EXISTS (
            SELECT 1
            FROM screenings s
            WHERE NOT EXISTS (
                SELECT 1 FROM screening_seats ss WHERE ss.screening_id = s.id
            )
        ) THEN
            RAISE EXCEPTION 'Cannot infer currency for a screening without screening_seats; assign its currency before retrying this migration';
        END IF;

        IF EXISTS (
            SELECT 1
            FROM bookings_seats bs
            JOIN screening_seats ss
              ON ss.screening_id = bs.screening_id
             AND ss.seat_id = bs.seat_id
            WHERE bs.currency <> ss.currency
        ) THEN
            RAISE EXCEPTION 'Cannot migrate screening currency: historical booking-seat currency differs from its screening-seat price';
        END IF;

        ALTER TABLE screenings ADD COLUMN currency varchar(3);

        UPDATE screenings s
        SET currency = prices.currency
        FROM (
            SELECT screening_id, MIN(currency) AS currency
            FROM screening_seats
            GROUP BY screening_id
        ) AS prices
        WHERE prices.screening_id = s.id;

        ALTER TABLE screenings ALTER COLUMN currency SET NOT NULL;
        ALTER TABLE screening_seats DROP COLUMN currency;
    ELSIF NOT screenings_has_currency AND NOT screening_seats_has_currency THEN
        RAISE EXCEPTION 'Cannot migrate screening currency: neither screenings nor screening_seats has a currency column';
    ELSIF screenings_has_currency AND screening_seats_has_currency THEN
        IF EXISTS (
            SELECT 1
            FROM screening_seats ss
            JOIN screenings s ON s.id = ss.screening_id
            WHERE ss.currency <> s.currency
        ) THEN
            RAISE EXCEPTION 'Cannot finish screening-currency migration: screening_seats contains currency values that differ from screenings';
        END IF;

        ALTER TABLE screening_seats DROP COLUMN currency;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM bookings_seats bs
        JOIN screenings s ON s.id = bs.screening_id
        WHERE bs.currency <> s.currency
    ) THEN
        RAISE EXCEPTION 'Cannot enforce screening currency: historical booking-seat currency differs from its screening';
    END IF;

    IF EXISTS (SELECT 1 FROM screenings WHERE currency IS NULL) THEN
        RAISE EXCEPTION 'Cannot enforce screening currency: every screening must have a currency';
    END IF;

    ALTER TABLE screenings ALTER COLUMN currency SET NOT NULL;

    CREATE UNIQUE INDEX IF NOT EXISTS screenings_id_currency_unique
        ON screenings (id, currency);

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = format('%I.%I', current_schema(), 'bookings_seats')::regclass
          AND conname = 'bookings_seats_screening_currency_fkey'
    ) THEN
        ALTER TABLE bookings_seats
            ADD CONSTRAINT bookings_seats_screening_currency_fkey
            FOREIGN KEY (screening_id, currency)
            REFERENCES screenings (id, currency)
            DEFERRABLE INITIALLY IMMEDIATE;
    END IF;
END $$;
