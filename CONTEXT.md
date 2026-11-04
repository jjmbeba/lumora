# Cinema Ticketing Backend — Project Context

Last updated: 2026-11-03

## Purpose and working style

Build a reliable end-to-end backend for a single independent cinema, using Go. The learner wants to understand the design and implement the system personally. Act as a direct technical guide and reviewer: explain trade-offs, challenge gaps, and review the learner's drafts. Do not silently implement substantial backend work unless explicitly asked. Advance one bounded artifact or decision at a time.

## Product scope and decisions

- One independent cinema at one location; a screening is a scheduled showing of a movie in an auditorium.
- Go backend. PostgreSQL has been selected as the database.
- The initial payment provider is Paystack, with card and M-Pesa checkout required. Merchant onboarding and sandbox validation remain gates. Direct Daraja integration is deferred until there is a legitimate merchant shortcode; Daraja sandbox behavior was considered unreliable.
- A booking reserves the selected seats for five minutes. Seat claims must be atomic: all selected seats are claimed or none are.
- Require verification through the booking email before returning booking details or allowing payment retry; a booking reference alone does not authorize access. The agreed flow uses a six-digit single-use code, HMAC-SHA-256 digest, 10-minute code lifetime, five attempts, 60-second resend cooldown, five sends per booking/hour, and a 15-minute booking-scoped opaque bearer token. Store only code/token digests; never log raw codes or tokens.
- Booking states: `RESERVED`, `PAYMENT_REVIEW`, `BOOKED`, `CANCELLED`.
- A ticket is an admission credential for one seat on one screening. A confirmed booking gets exactly one ticket per booked seat; customers retrieve them through the email-verified booking-details endpoint and receive them by email after confirmation.
- `MarkPaid` is the v1 transaction boundary for confirming payment/booking, issuing tickets, and queuing the durable confirmation email; use focused persistence helpers rather than a generic transaction framework.
- Existing bookings that became `BOOKED` before ticket issuance are legacy rows and are not backfilled by SQL. Before enabling ticket access in any retained environment, run an idempotent application-level backfill that creates encrypted ticket credentials and email jobs; disposable local data can instead be reseeded.
- Booking confirmation email delivery is durable: a PostgreSQL-backed job is committed with the booking/tickets and retried independently after commit; email failure does not undo confirmation.
- Send a single confirmation email per booking containing every ticket and its QR code.
- Tickets are independently shareable and redeemable; group members can check in separately, and late guests use their own still-unused ticket.
- An unused ticket can be redeemed through its screening's scheduled `end_time`, with no short cutoff after `start_time`; redemption is rejected after the screening ends.
- Each ticket includes a QR code for admission. The first successful scan redeems it atomically; later scans are rejected as already used.
- Ticket QR codes contain 32 cryptographically random bytes encoded as unpadded URL-safe base64. Store SHA-256 of the raw bytes for lookup and AES-256-GCM ciphertext for re-rendering, bound to the booking and seat IDs; keep versioned encryption keys outside PostgreSQL. Configure `TICKET_CREDENTIAL_CURRENT_KEY_VERSION`, `TICKET_CREDENTIAL_KEY_VERSIONS` (for example, `1,2`), and matching `TICKET_CREDENTIAL_KEY_<version>` values containing standard-base64-encoded 32-byte keys. Retain old key versions while tickets encrypted with them remain. Never store the credential as plaintext or include booking/customer/seat details in the QR.
- Admission requires an unused ticket and a booking still in `BOOKED`; review or cancelled bookings are not admitted even if tickets were issued earlier.
- Ticket redemption requires authenticated staff and records the staff member who redeemed it.
- Create the first owner account through a one-time bootstrap path; managers then provision staff through a manager-only frontend flow, with no public signup.
- Protect first-owner setup with a high-entropy secret configured outside the database; disable bootstrap immediately after creating the first owner.
- Staff sign in with individual email/password credentials and receive an expiring revocable session; store only adaptive password hashes, never plaintext, and keep staff auth separate from customer booking tokens.
- Scanner sessions last at most 8 hours; staff can log out, and deactivation revokes sessions immediately.
- Expire scanner sessions after 15 minutes of inactivity.
- Staff roles: `MANAGER` can provision/deactivate staff and redeem tickets; `SCANNER` can redeem tickets only. The first owner is a manager.
- New staff receive a single-use setup link to choose a password; accounts remain inactive until setup. Deactivation revokes active sessions.
- Ticket scans require a live backend connection in v1 to validate current booking state and redeem once; offline scanning is deferred.
- Payment states: `PENDING`, `PAID`, `FAILED`, `CANCELLED`.
- If a hold expires while payment is ambiguous, keep the seats unavailable. Move the booking to review when automated reconciliation cannot resolve payment. A confirmed terminal failure/cancellation releases claims. Retrying after a terminal payment outcome requires a new booking and seat selection.
- Paystack `abandoned` remains pending while the booking hold is valid. Once the hold expires, the initialization and non-cancelled booking move to review while payment state and existing seat claims are preserved. A late `charge.success` after cancellation also requires staff review and never reacquires released seat claims. A verified `reversed` transaction moves the booking and initialization to review, preserves payment status and existing claims, and never reacquires claims already released by cancellation.
- Each booking has one payment attempt. It may have multiple child payment-initialization records. Each initialization has its own idempotency key, payment method, Paystack reference, state, and checkout URL when available.
- Permit a new initialization only when no previous initialization exists or the latest one was definitively `REJECTED` because Paystack confirmed no checkout was created. Do not retry while a prior result is active or uncertain. In this known-no-checkout case, an expired hold cancels the booking and releases its claims. If payment may have reached Paystack and remains unresolved at expiry, move the booking to review and preserve existing claims. Initialization rejection is not payment failure.
- Initialization states currently modeled: `QUEUED`, `SUBMITTING`, `READY`, `FAILED`, `REJECTED`, `UNKNOWN`, `CANCELLED`, `REVIEW_REQUIRED`. `CANCELLED` is not used for Paystack `abandoned`; unresolved payments after hold expiry move to `REVIEW_REQUIRED`. A verified `reversed` result moves a non-cancelled booking to review while preserving payment status and existing claims; it never reacquires claims already released.
- `payment_initializations` is intended to be the durable queue for this one provider job; no separate outbox table is planned for the first version.
- Idempotency key identifies a request and must be unique at the chosen scope. A request fingerprint is compared on replay to reject same-key/different-request reuse; fingerprints need not be unique.
- Paystack references are generated and persisted before provider calls. A lost/ambiguous provider response must be reconciled using that stored reference before any retry.
- A worker lease expiry is a recovery signal, not proof the provider was never contacted. On recovery, reclaim the row atomically and reconcile by reference before repeating initialization.

## Go project structure and design conventions

Agreed starting structure: a single Go module at the repository root, organized as a modular monolith. Keep commands in `cmd/`, implementation packages under `internal/`, and SQL migrations under a root `migrations/` directory. Start with `cmd/api` and `cmd/payment-worker`; keep the worker's behavior reusable from internal packages so the API and worker remain deployable as separate processes without becoming separate services.

Organize internal packages around domain responsibilities such as `booking`, `screening`, and `payment`, with `httpapi`, `config`, and `app` for transport, configuration, and dependency wiring. Keep PostgreSQL-specific implementations close to the feature they serve (for example, `internal/booking/postgres`). Define small interfaces in the package that consumes them (for example, `booking.Store`); PostgreSQL implementations satisfy them implicitly. Avoid creating a package for every layer or generic `common`, `util`, `types`, or `repository` buckets without a concrete need.

Treat this layout as a guide, not a requirement to scaffold every package immediately. Put focused Go tests beside their packages and add PostgreSQL integration tests for transaction and locking guarantees. Schema migrations live under `migrations/`.

Agreed design defaults:

- Modular monolith with domain-oriented packages.
- Small consumer-owned interfaces at real boundaries (PostgreSQL and Paystack), with explicit constructor wiring in `internal/app` rather than global dependencies or a DI framework.
- Explicit transaction boundaries for booking/seat claims and worker claims; do not hold a transaction open during provider calls.
- Define explicit allowed state transitions for bookings, payments, and payment initializations in Go. SQL updates must also guard on the expected current status and check affected rows so concurrent changes cannot silently apply a stale transition. Fence worker completion using the worker attempt/generation token so an expired worker cannot overwrite a newer attempt.
- Avoid event sourcing, CQRS, generic repository frameworks, and separate services unless a concrete requirement justifies their cost.

## Current artifacts

- `docs/briefs/customer-consultant-brief-draft.md` — customer/consultant brief.
- `docs/architecture/cinema-ticketing-schema.dbml` — logical schema draft. It documents the conditional uniqueness rule, but DBML cannot enforce it; PostgreSQL migration must create the partial unique index.
- `docs/architecture/flows/booking-create-idempotency-flow.excalidraw` — booking creation and seat-claim idempotency flow.
- `docs/architecture/flows/payment-initialization-flow.excalidraw` — payment initialization flow, including queue claim, lease recovery, and retry policy.
- `docs/architecture/payment-worker-recovery.md` — agreed queue claim, lease recovery, reconciliation, and stale-worker fencing protocol.
- `docs/guides/customer-booking-payment-api-implementation.md` — staged implementation guide for the customer booking and payment-initialization flow.
- `docs/research/kenya-online-payment-options.md` — provider research.
- `docs/research/go-project-structure.md` — official Go guidance and the proposed project structure.
- `docs/architecture/decisions.md` — accepted product, architecture, and design decisions plus deferred choices.
- `migrations/00001_core_schema.sql` through `migrations/00007_ticketing_staff_email_outbox.sql` — versioned Goose SQL migrations. Run `go run ./cmd/migrate up` before starting services and `go run ./cmd/migrate status` to inspect the database version. Docker Compose starts PostgreSQL only; it no longer applies schema scripts through the image initialization hook.

## Important database invariants to implement

- At most one active/unresolved initialization per payment attempt. In PostgreSQL, the partial unique index covers `QUEUED`, `SUBMITTING`, `READY`, `UNKNOWN`, and `REVIEW_REQUIRED`; allow terminal `FAILED`, `REJECTED`, and `CANCELLED` rows as history.
- Unique Paystack reference and idempotency key; fingerprint is not unique.
- Surrogate `id` primary keys use PostgreSQL `GENERATED BY DEFAULT AS IDENTITY`; runtime inserts omit the ID and use `RETURNING id` when needed.
- Worker claim/reclaim must be transactional so concurrent workers cannot process the same row as owner. A lease should be updated atomically with the status and attempt counter.
- Do not hold a database transaction open during a Paystack network call.
- Seat claim primary key is `(screening_id, seat_id)`; foreign keys must keep the seat, screening, auditorium, booking, and historical booking-seat row aligned.
- Each screening has one currency. Store per-seat prices on screening-seat rows and snapshot unit price/currency on booking-seat rows; PostgreSQL foreign keys keep booking snapshots in the screening currency.

## Current stage and next step

The Go payment worker, Paystack adapter, PostgreSQL state transitions, signed webhook inbox, email verification flow, and token-protected booking-details endpoint are implemented. The details endpoint now reveals each booked seat’s decrypted QR credential only after validating the booking access token; reserved bookings do not include ticket data. The customer booking/payment flow has reached `PAID`/`PAID`/`BOOKED` in the Paystack sandbox, and webhook replay was checked locally. `MarkPaid` creates one encrypted ticket per saved booking seat and queues one durable ticket email in the same transaction. The ticket email worker claims outbox jobs with leases, decrypts stable ticket credentials, renders inline QR images, and retries delivery with Resend idempotency keys. The schema is managed by embedded Goose migrations; run `go run ./cmd/migrate up` before starting services. `cinema_ticketing_test` still needs its disposable schema recreated through Goose to establish the Goose version history. Focused PostgreSQL tests for `MarkPaid` and outbox claim/retry/fencing passed on 2026-11-04. The `internal/ticket` credential cipher is configured from a versioned environment key ring. The logical schema is in [the DBML schema](docs/architecture/cinema-ticketing-schema.dbml).

Next: before enabling tickets in an environment with retained pre-feature `BOOKED` rows, run an idempotent application-level backfill for their ticket credentials and email jobs; disposable local data can be reseeded. Then implement staff authentication/onboarding and online ticket redemption. Keep the accepted invariants in this file and [the architecture decisions](docs/architecture/decisions.md).

## How to resume in a new Codex chat

Open `/home/jjmbeba/Desktop/projects/cinema-ticketing-backend` as the project, then ask the new chat to read this file and the relevant artifact before continuing. Start with the local customer-flow smoke test. Preserve this file as the concise source of current decisions and update it when decisions change.
