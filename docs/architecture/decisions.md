# Cinema Ticketing Backend Decisions

Accepted decisions for the project. `CONTEXT.md` remains the concise resume guide; this file is the durable decision record. Decisions may be revised when new evidence or requirements justify it.

## Product and backend scope

- Build an end-to-end backend in Go for one independent cinema at one location. A screening is a scheduled showing of a movie in an auditorium.
- Use PostgreSQL as the database.
- Use Paystack as the first payment provider, with card and M-Pesa checkout required. Merchant onboarding and sandbox validation remain gates. Defer direct Daraja integration until there is a legitimate merchant shortcode.
- Use Resend for v1 transactional booking-access emails. Keep it behind a narrow email-sender interface owned by the consuming booking package; express that contract using application-owned message data, not Resend SDK types, and keep provider-specific request/response handling in `internal/email/resend`. A future provider switch should require replacing the adapter and its configuration/wiring, not changing booking policy or HTTP handlers. Do not add a multi-provider framework until the product needs one. Read `RESEND_API_KEY` and `EMAIL_FROM` from configuration. Production sending requires a verified sender domain whose DNS we control. The free tier's 100-email daily cap is a provider-wide limit; move to a paid plan or another approved capacity before expected traffic exceeds it. See `docs/research/email-provider-selection.md` for the dated provider comparison.

## Booking and payment behavior

- A booking holds selected seats for five minutes. Claim all seats atomically or claim none.
- Each screening has exactly one currency. Per-seat prices are stored in that currency; a booking snapshots the price and currency for each selected seat. PostgreSQL constrains booking-seat snapshots to the screening currency, so a booking total is never a sum across currencies.
- Require proof of access to the booking email before returning booking details or allowing payment retry. A booking reference identifies a booking but does not authorize access. Send a six-digit, single-use code to the email already stored on the booking; do not accept a replacement email address from the requester. Store the code as an HMAC-SHA-256 digest keyed with `BOOKING_ACCESS_CODE_HMAC_KEY`, not plaintext. A code expires after 10 minutes, allows at most five verification attempts, and only one code may be active per booking. Resending invalidates the previous code, enforces a 60-second cooldown, and is limited to five sends per booking per hour. Limit challenge and verification requests to 20 per IP per hour; apply the IP limit at the HTTP edge and use shared enforcement before running multiple API instances. Return the same generic challenge response for known and unknown booking references. After successful verification, return a cryptographically random opaque bearer token scoped to that booking, valid for 15 minutes; store only its SHA-256 digest. Never log codes or bearer tokens. Email verification proves mailbox access; it is not treated as strong account authentication.
- Booking states are `RESERVED`, `PAYMENT_REVIEW`, `BOOKED`, and `CANCELLED`. Payment states are `PENDING`, `PAID`, `FAILED`, and `CANCELLED`.
- If a hold expires while payment is ambiguous, keep the seats unavailable. Move the booking to review if reconciliation cannot resolve payment. Release seat claims only after confirmed terminal failure or cancellation. A terminal payment outcome requires a new booking and seat selection to retry.
- Paystack `abandoned` is treated as pending while the booking hold remains valid, because checkout can later succeed. Once `hold_expires_at` has passed, move the initialization to `REVIEW_REQUIRED` and a non-cancelled booking to `PAYMENT_REVIEW`, preserving payment state and existing seat claims. Do not release seats based on `abandoned` alone.
- A `charge.success` webhook arriving after an initialization and booking were already cancelled moves the initialization to `REVIEW_REQUIRED`, but leaves the booking cancelled and does not restore released seat claims. Staff reconciliation is required; the webhook does not book seats.
- A verified Paystack `reversed` transaction is not an ordinary payment failure. Mark its initialization `REVIEW_REQUIRED`, move a non-cancelled booking to `PAYMENT_REVIEW`, preserve the payment-attempt status, and retain existing seat claims. If the booking was already cancelled and claims released, do not reacquire seats; keep the initialization unresolved for staff reconciliation.
- Each booking has one payment attempt and may have multiple child payment-initialization records. Each initialization stores its own payment method, provider reference, state, and checkout URL when available.
- The customer starts booking and payment through one client request. An application-level use case commits the booking and seat claims first, then creates or replays the payment attempt and durable payment initialization together in a separate transaction. A failure between those commits leaves the booking held; retrying the same request must replay that booking and safely continue or replay initialization. The worker remains the only component that calls Paystack. The request contract must include the payment method and define idempotency/fingerprinting across both operations.
- Permit a new initialization only if no prior initialization exists or the latest one was definitively rejected because Paystack confirmed no checkout was created. After that known-no-checkout rejection, a hold that expires before retry cancels the booking and releases its claims. If payment may have reached Paystack and remains unresolved at hold expiry, move it to review and preserve existing claims. An initialization rejection is not a payment failure.
- If a customer tries to switch payment methods while an initialization is unresolved, do not create another initialization. Return or identify the existing initialization so the client can show its status while the worker/webhook reconciles it. A confirmed paid payment cannot be initialized again; only a definitive rejection proving no checkout was created permits another initialization under the existing retry rules.
- Retry payment for an existing booking through a separate operation, such as `POST /bookings/{reference}/payment-initializations`, rather than resubmitting the booking-and-payment-start request with a new key. Each retry uses a fresh idempotency key and fingerprint; a matching replay returns the same initialization, and reuse with changed input conflicts. Create a new initialization for the same payment attempt only after the latest initialization is definitively `REJECTED`. An unresolved or paid attempt cannot be retried. The booking reference alone does not authorize this operation; require the booking-scoped, expiring access granted after the customer verifies access to the booking email.
- Initialization states are `QUEUED`, `SUBMITTING`, `READY`, `FAILED`, `REJECTED`, `UNKNOWN`, `CANCELLED`, and `REVIEW_REQUIRED`. `FAILED` means Paystack verification confirmed a terminal failed payment; it is distinct from `REJECTED`, which means Paystack confirmed that no checkout was created. `CANCELLED` is reserved for an explicit cancellation outcome, not Paystack `abandoned`. `REVIEW_REQUIRED` quarantines conflicting, reversed, or unresolved-after-hold outcomes; workers do not claim it.
- Use `payment_initializations` as the durable queue for this provider job in the first version; no separate outbox table.
- Keep payment work database-backed in v1. Reconsider Redis Streams for v2 if the product grows into multiple asynchronous workflows that need independent consumer groups and replay—for example, a confirmed booking must reliably fan out to ticket issuance, customer notifications, and analytics, each scaling and recovering independently. In that design, write an outbox event in the same PostgreSQL transaction as the booking/payment state change, then publish it to Redis Streams; consumers must remain idempotent. PostgreSQL remains the source of truth for bookings and payments, and Redis must not become a second authority for payment status. Do not add Streams just to implement the current single payment-initialization worker.

## Idempotency and provider recovery

- Booking creation uses a globally unique idempotency key. Compare a request fingerprint on replay and reject same-key/different-request reuse. The fingerprint is not unique.
- Payment initialization requests use globally unique keys recorded in `payment_initialization_receipts`. Each receipt stores the request fingerprint, requested payment method, and returned initialization. Multiple receipts can point to one unresolved initialization, including when their requested methods differ. A matching replay returns the recorded initialization; reuse with different request details conflicts. Creating or returning an initialization and recording its receipt must commit in one transaction. Paystack references are generated and persisted before provider calls.
- The client idempotency key for the combined booking-and-payment-start request covers the payment method as well as the booking inputs. Repeating the same key and request replays the same booking and initialization; reusing that key with changed input returns a conflict. A request with a different key must still return the existing unresolved initialization for that payment attempt rather than create another one. The application flow must persist enough request identity to enforce same-key conflicts across the booking/init partial-failure boundary.
- Reconcile an ambiguous provider result using the stored reference before retrying. A worker lease expiry signals recovery is needed; it does not prove Paystack was never contacted.
- Keep at most one unresolved initialization per payment attempt (`QUEUED`, `SUBMITTING`, `READY`, `UNKNOWN`, or `REVIEW_REQUIRED`). `REVIEW_REQUIRED` is unresolved but not worker-claimable; retain terminal `FAILED`, `REJECTED`, and `CANCELLED` rows as history.

## Go structure

- Use one Go module at the repository root and a modular-monolith architecture. Do not split into services without a concrete need.
- Put executable entry points in `cmd/api` and `cmd/payment-worker`. Keep their behavior in shared internal packages so they can run as separate processes without duplicating rules.
- Put application implementation under `internal/`, organized around domain responsibilities such as `booking`, `screening`, and `payment`. Use `httpapi`, `config`, and `app` for transport, configuration, and dependency wiring.
- Keep PostgreSQL adapters near the feature they implement, such as `internal/booking/postgres`. Define small interfaces in the consuming package; concrete implementations satisfy them implicitly.
- Keep SQL migration files under the root `migrations/` directory. The base schema is in `migrations/migration.sql`; `migrations/webhooks.sql` creates the webhook inbox; `migrations/identity-defaults.sql` upgrades existing integer IDs to identity columns; and `migrations/04-screening-currency.sql` upgrades per-seat currencies to one currency per screening.
- Use `GENERATED BY DEFAULT AS IDENTITY` for surrogate `id` primary keys. Inserts should omit generated IDs and use `RETURNING id` when the caller needs the assigned value. Explicit IDs remain available for fixtures, but do not advance the identity sequence; reseed the sequence or avoid explicit IDs before relying on generated values.
- Keep the layout lean: do not create packages for every layer or generic `common`, `util`, `types`, or `repository` packages without a concrete need.

Example starting layout (create packages as implementation needs them; this is a guide, not a requirement to scaffold every directory at once):

```text
.
├── cmd/
│   ├── api/
│   │   └── main.go
│   └── payment-worker/
│       └── main.go
├── internal/
│   ├── app/                 # dependency wiring and process setup
│   ├── config/              # environment and runtime configuration
│   ├── httpapi/             # HTTP routes, handlers, middleware
│   ├── observability/       # shared logger and metrics setup
│   ├── booking/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── postgres/         # booking persistence adapter
│   ├── screening/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── postgres/
│   └── payment/
│       ├── model.go
│       ├── service.go
│       ├── worker.go
│       ├── postgres/         # payment and worker-claim adapter
│       └── paystack/         # provider adapter
├── migrations/
└── go.mod
```

Feature packages own their domain behavior and consuming interfaces; adapters stay beside the feature. `cmd/*/main.go` should stay small and delegate setup to `internal/app`.

## Design defaults

- Use small interfaces at real boundaries, including database and provider clients, and wire dependencies explicitly through constructors in `internal/app`. Do not add a DI framework or global dependencies by default.
- Make transaction boundaries explicit around booking/seat claims and worker claim/reclaim. Commit before making provider network calls.
- Define allowed booking, payment, and initialization state transitions in Go. Guard SQL updates on the expected current status and check affected rows. Fence worker completion with an attempt/generation token so an expired worker cannot overwrite a newer attempt.
- Claim due `QUEUED`/`UNKNOWN`/`READY` rows and expired `SUBMITTING` rows transactionally with `FOR UPDATE SKIP LOCKED`; increment the attempt generation and set the lease in that short transaction, then commit before provider calls. `QUEUED` rows initialize; `READY` rows verify the stored reference; expired or unknown rows reconcile by stored reference before any initialization retry. Keep unresolved results in `UNKNOWN`; do not assume Paystack initialization is idempotent without provider evidence. See [payment worker recovery](payment-worker-recovery.md).
- Avoid event sourcing, CQRS, and generic repository frameworks unless a concrete requirement justifies their complexity.

## Observability

- Instrument API and worker behavior with structured logs and metrics. For the payment worker, include provider outcomes and `429` rate-limit responses, categorized errors, retries and scheduled backoff, due-work age, unresolved `UNKNOWN` payments, lease expirations, reconciliation outcomes, and stale-generation updates rejected. Use bounded metric labels; do not label by payment, booking, customer, or provider-reference IDs, and never log credentials or sensitive payment details.
- Choose and wire the metrics backend during deployment planning. Prometheus collection and Grafana dashboards/alerts are the expected starting option, not a v1 prerequisite. First define useful measurements and expose them from the processes; add Prometheus and Grafana when deployment monitoring is established. Keep logs useful on their own during local development.

## Deferred decisions and gates

- Confirm Paystack merchant eligibility and validate card and M-Pesa flows in the available sandbox.
- Choose the HTTP router, SQL driver/query approach, migration runner, and whether migrations are embedded when implementation needs them.
- The draft PostgreSQL migration executes successfully against a fresh PostgreSQL 18 database. This proves DDL execution only; worker concurrency behavior still needs implementation-level tests. No Go module or backend implementation exists yet.
