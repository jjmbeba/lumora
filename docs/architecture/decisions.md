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
- Issue exactly one ticket per booked seat when a booking becomes `BOOKED`. Ticket creation belongs in the same PostgreSQL transaction as the payment/booking success transition, so retries cannot leave a booked seat without a ticket or issue duplicate tickets. A ticket identifies its booking and seat; review-required or cancelled bookings do not receive tickets. Customers can retrieve tickets through the existing email-verified booking-details endpoint, and the system also emails them after confirmation. Send email only after the booking transaction commits; an email failure must not undo a confirmed booking.
- Keep payment confirmation as the transaction boundary for the v1 booking transition, ticket issuance, and durable confirmation-email job. The existing `MarkPaid` operation coordinates these writes; isolate ticket and email-job persistence in focused helpers rather than introducing a generic transaction framework.
- Make confirmation email delivery durable: persist a PostgreSQL-backed delivery job in the same transaction that confirms the booking and creates its tickets. A worker sends the email after commit and retries transient failures. Delivery retries must not create tickets or otherwise repeat booking side effects.
- The ticket email worker claims due `BOOKING_TICKETS` outbox rows with `FOR UPDATE SKIP LOCKED`, increments `attempt_count` as its fencing generation, and recovers expired `SENDING` leases. Its defaults are a 30-second lease, 15-second send timeout, at most 10 attempts, and exponential retry delay with equal jitter from 5 seconds up to 5 minutes; after the final failed attempt, mark the job `FAILED` for operator review. Use the stable `event_key` as Resend's idempotency key to reduce duplicate delivery after ambiguous network failures; PostgreSQL remains the durable queue and delivery is still at least once.
- Do not create tickets or email jobs for existing `BOOKED` rows in SQL. Before enabling ticket access in a retained environment, use an idempotent application-level backfill to generate encrypted credentials, persist one ticket per booking seat, and enqueue confirmation emails. Disposable local data may be reseeded instead.
- Send one confirmation email per booking, containing all per-seat tickets and their individual QR codes, rather than sending one email per ticket.
- Keep each seat's ticket independently shareable and redeemable. Partial group arrival is allowed: scanning one guest's ticket does not consume any other ticket in the booking, so late guests can be admitted with their own unused QR code.
- Allow ticket redemption at any point through the screening's scheduled `end_time`; there is no short grace period after `start_time`, and tickets cannot be redeemed after the screening ends.
- Include a QR code on each ticket for admission. A successful staff scan atomically redeems that ticket; subsequent scans report it as already used.
- Put 32 cryptographically random bytes, encoded as unpadded URL-safe base64, in each ticket QR code. Store SHA-256 of the raw bytes for lookup and AES-256-GCM ciphertext so email retries and booking-details reads can reproduce the stable QR. Bind encryption to the booking and seat IDs as associated data; keep versioned keys outside PostgreSQL. Never store the credential as plaintext. The QR value carries no booking, customer, or seat details and is treated as a bearer secret. Return it in booking details only after validating that booking's short-lived access token, and only when the booking is `BOOKED`.
- Configure the ticket credential key ring with `TICKET_CREDENTIAL_CURRENT_KEY_VERSION`, a comma-separated `TICKET_CREDENTIAL_KEY_VERSIONS` list, and one matching `TICKET_CREDENTIAL_KEY_<version>` environment variable per retained standard-base64-encoded 32-byte key. The application reads only the listed key variables. Keep prior key versions configured until no persisted ticket requires them.
- Admit a ticket only when it has not already been redeemed and its booking is still `BOOKED`; a booking moved to review or cancellation cannot be admitted even if a ticket was issued earlier.
- Require an authenticated staff account to redeem tickets and record which staff member redeemed each ticket for auditability.
- Create the first owner account through a one-time bootstrap path. Afterward, managers create individual staff accounts through a manager-only frontend flow; do not expose public self-registration.
- Protect first-owner setup with a high-entropy secret configured outside the database, and disable the bootstrap path immediately after the first owner is created.
- Authenticate staff with their individual email and password, then issue an expiring revocable session. Store passwords only as adaptive password hashes, never as plaintext. Do not reuse customer booking-access tokens for staff authentication.
- Require authenticator-app TOTP for every staff account before issuing a full session. Provide 10 single-use recovery codes at MFA enrollment; persist only their digests and consume them atomically. Store TOTP secrets encrypted with a versioned key kept outside PostgreSQL. MFA sign-in/enrollment challenges are short-lived and never authorize ticket redemption.
- The initial implementation contract and required environment key ring are tracked in [staff-authentication.md](staff-authentication.md); bootstrap/enrollment and manager operations remain implementation work.
- Keep scanner sessions valid for at most 8 hours, with explicit logout and immediate revocation when an account is deactivated.
- Expire a scanner session after 15 minutes of inactivity, even if its work-shift maximum has not elapsed.
- Use two staff roles in v1: `MANAGER` can provision/deactivate staff and redeem tickets; `SCANNER` can redeem tickets only. The first owner account is a manager.
- When a manager creates a staff account, email a single-use setup link for the new staff member to choose a password; keep the account inactive until setup completes. Deactivating an account revokes its active sessions.
- Do not allow deactivation of the last active manager; losing all active managers would lock out staff administration.
- Require a live backend connection for ticket scans in v1 so the scan can check current booking status and redeem the ticket atomically; offline admission and later conflict resolution are deferred.

## Idempotency and provider recovery

- Booking creation uses a globally unique idempotency key. Compare a request fingerprint on replay and reject same-key/different-request reuse. The fingerprint is not unique.
- Payment initialization requests use globally unique keys recorded in `payment_initialization_receipts`. Each receipt stores the request fingerprint, requested payment method, and returned initialization. Multiple receipts can point to one unresolved initialization, including when their requested methods differ. A matching replay returns the recorded initialization; reuse with different request details conflicts. Creating or returning an initialization and recording its receipt must commit in one transaction. Paystack references are generated and persisted before provider calls.
- The client idempotency key for the combined booking-and-payment-start request covers the payment method as well as the booking inputs. Repeating the same key and request replays the same booking and initialization; reusing that key with changed input returns a conflict. A request with a different key must still return the existing unresolved initialization for that payment attempt rather than create another one. The application flow must persist enough request identity to enforce same-key conflicts across the booking/init partial-failure boundary.
- Reconcile an ambiguous provider result using the stored reference before retrying. A worker lease expiry signals recovery is needed; it does not prove Paystack was never contacted.
- Keep at most one unresolved initialization per payment attempt (`QUEUED`, `SUBMITTING`, `READY`, `UNKNOWN`, or `REVIEW_REQUIRED`). `REVIEW_REQUIRED` is unresolved but not worker-claimable; retain terminal `FAILED`, `REJECTED`, and `CANCELLED` rows as history.

## Go structure

- Use one Go module at the repository root and a modular-monolith architecture. Do not split into services without a concrete need.
- Put executable entry points in `cmd/api`, `cmd/payment-worker`, and `cmd/ticket-email-worker`. Keep their behavior in shared internal packages so they can run as separate processes without duplicating rules.
- Put application implementation under `internal/`, organized around domain responsibilities such as `booking`, `screening`, and `payment`. Use `httpapi`, `config`, and `app` for transport, configuration, and dependency wiring.
- Keep PostgreSQL adapters near the feature they implement, such as `internal/booking/postgres`. Define small interfaces in the consuming package; concrete implementations satisfy them implicitly.
- Keep numbered Goose SQL migrations under the root `migrations/` directory. Run them explicitly with `go run ./cmd/migrate up` before starting APIs or workers; check state with `go run ./cmd/migrate status`. Do not run migrations from service startup. The migration command embeds the SQL files and uses an explicit Goose provider.
- Use sqlc v1.31.1 for typed reads and writes in the booking, payment, and ticket PostgreSQL adapters. Keep named query files inside each adapter's `queries/` directory, generate into its `dbgen/` package, and use Goose's `migrations/` directory as sqlc's schema input. Goose applies migrations; sqlc only analyzes the up migrations and generates code. Regenerate with `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate` after changing queries or schema.
- Keep generated database types inside each PostgreSQL adapter and map them to domain types there. The adapter retains transaction ownership; generated queries called inside a transaction must use a query object bound to that existing `*sql.Tx`.
- Use `GENERATED BY DEFAULT AS IDENTITY` for surrogate `id` primary keys. Inserts should omit generated IDs and use `RETURNING id` when the caller needs the assigned value. Explicit IDs remain available for fixtures, but do not advance the identity sequence; reseed the sequence or avoid explicit IDs before relying on generated values.
- Keep the layout lean: do not create packages for every layer or generic `common`, `util`, `types`, or `repository` packages without a concrete need.

Example starting layout (create packages as implementation needs them; this is a guide, not a requirement to scaffold every directory at once):

```text
.
├── cmd/
│   ├── api/
│   │   └── main.go
│   ├── payment-worker/
│   │   └── main.go
│   └── ticket-email-worker/
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
