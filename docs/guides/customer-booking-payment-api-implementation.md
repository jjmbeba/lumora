# Customer booking and payment API: implementation guide

This guide is a learning path, not a code dump. Implement one slice, inspect its behavior, and review it before moving on. Do not start the next slice while the current one still has unclear ownership or failure behavior.

## Starting point

The worker, Paystack adapter, PostgreSQL payment transitions, and webhook receiver already exist. A sandbox checkout has completed successfully through a SQL-seeded fixture. The fixture bypasses the customer flow, so the next milestone is to create a booking and start payment through the API.

Before coding, read these together:

- `CONTEXT.md` for current product rules.
- `docs/architecture/decisions.md` for idempotency, package boundaries, and transaction rules.
- `docs/architecture/flows/booking-create-idempotency-flow.excalidraw` for booking replay and seat-conflict behavior.
- `migrations/migration.sql` for the constraints the code must satisfy.
- `internal/payment/worker.go` and `internal/payment/postgres/store.go` for the existing payment worker contract and state transitions.

The important boundary is: PostgreSQL owns durable state and atomic seat ownership; Go validates and coordinates operations; Paystack network calls happen after database transactions have committed.

## Step 1 — Specify the booking operation

**Goal:** describe one booking request and its externally visible outcomes before designing handlers or SQL.

**Likely files:** no code file yet; update the booking flow diagram or this guide if the contract changes.

Write down, in your own words:

- Which values the customer supplies: screening, seat IDs, and contact details.
- Which values the server derives: booking reference, prices, total, currency, and five-minute expiry.
- How the client supplies the globally unique booking idempotency key.
- How the customer proves access to booking details and payment retry: send a six-digit code only to the email already stored on the booking; the booking reference alone is not sufficient.
- What a replay with the same key and same request returns, and what same-key/different-request reuse returns.
- What the API returns if any requested seat is already claimed.

Do not accept prices, booking status, expiry, or payment status from the customer. Those values affect money or seat ownership and must come from trusted server/database state.

**Checkpoint:** you can explain what happens when two identical requests arrive at once, and why the client receives the same booking result instead of creating a second booking.

## Step 2 — Add the booking domain types and consuming interface

**Goal:** express the operation in Go without putting SQL details in the domain package.

**Likely files:** create `internal/booking/model.go`, `internal/booking/store.go`, and `internal/booking/service.go`.

Start with the smallest useful types: a booking request, a result returned to the caller, and errors for meaningful business outcomes such as unavailable seats or idempotency-key reuse with a different request. Keep exported fields Go-style (`ScreeningID`, `SeatIDs`, `HoldExpiresAt`); use integer minor currency units for money.

Put a small `Store` interface in `internal/booking`, because the booking service consumes it. Its method should represent the booking operation the application needs, not expose generic SQL operations or transaction controls. The PostgreSQL package will implement it implicitly.

Keep the service focused: canonicalize/fingerprint the request and apply domain rules, then delegate the atomic persistence operation. Avoid building a generic repository framework or a package per type.

**Checkpoint:** the service can be described without mentioning `*sql.DB`, SQL strings, or PostgreSQL row types.

## Step 3 — Implement the atomic PostgreSQL booking write

**Goal:** ensure one booking either owns every requested seat or owns none.

**Likely files:** create `internal/booking/postgres/store.go` and `internal/booking/postgres/store_integration_test.go`.

Implement the store operation as one short transaction. It must resolve the idempotency key, load authoritative screening-seat prices and the screening's single currency, create the booking and immutable price snapshots, and claim all seats. The primary key on `seat_claims(screening_id, seat_id)` is the final concurrency guard when two customers request the same seat. The schema requires every seat in a screening to use that screening currency, so the booking total has one well-defined currency.

Study the existing flow and answer these before writing the query sequence:

1. What database constraint makes the idempotency key safe under concurrent requests?
2. If one of three requested seats conflicts, how do you ensure the other two claims and the booking do not remain?
3. Which rows provide the prices, and where is each price snapshot stored?
4. How do you distinguish a replay from a seat conflict and from an unexpected database error?

Do not keep a transaction open after this operation. Do not call Paystack from the transaction.

**Checkpoint:** a transaction rollback leaves no booking, historical seat rows, or active claims from the failed attempt; a successful booking has matching `bookings_seats` and `seat_claims` rows and a server-calculated total.

## Step 4 — Expose booking creation and protected details over HTTP

**Goal:** translate HTTP input/output while keeping booking rules in the service.

**Implemented:** booking creation is exposed by `POST /bookings`. Email-verification contracts and service live in `internal/booking/email_verification.go`, `internal/booking/email_verification_store.go`, and `internal/booking/email_verification_service.go`; PostgreSQL persistence lives in `internal/booking/postgres/email_verification_store.go`, with schema in `migrations/06-booking-email-verification.sql`. The Resend adapter is in `internal/email/resend/sender.go`. The booking API wires the store, sender, service, limiter, and routes in `cmd/booking-api/main.go`. The challenge and verification routes are `POST /bookings/{reference}/email-verification-challenges` and `POST /bookings/{reference}/email-verifications`. The challenge route returns the same generic `202 Accepted` response for unknown bookings, booking-level limits, and email-delivery failures. Configuration is read from `RESEND_API_KEY`, `EMAIL_FROM`, and `BOOKING_ACCESS_CODE_HMAC_KEY`; never commit their values. The email-sender interface stays narrow and booking-owned, without Resend SDK types.

The HTTP IP limiter is in-memory. It sweeps inactive IPs at most once every five minutes when requests arrive, resets on process restart, and does not share limits across API instances. Use shared enforcement before running multiple instances.

The handler should decode and validate request shape, obtain the idempotency key, call the service, and map known outcomes to appropriate HTTP responses. It should not calculate prices, write SQL, or decide which seats are available. Return a stable booking reference and the original hold expiry on an idempotent replay; do not extend the hold during replay.

Choose a response contract that can later link to payment initialization without pretending that a checkout URL exists before the worker obtains one. Keep that contract small and document the status the client should poll or request next. For protected booking details and payment retry, require successful verification of the booking email; do not treat the booking reference as a secret. The agreed flow sends a six-digit code only to the email stored on the booking. The code is single-use, expires after 10 minutes, permits at most five verification attempts, and is stored as an HMAC-SHA-256 digest keyed by `BOOKING_ACCESS_CODE_HMAC_KEY`. A resend invalidates the earlier code, has a 60-second cooldown, and is capped at five sends per booking per hour. Limit challenge and verification requests to 20 per IP per hour; before scaling to multiple API instances, ensure the IP limiter uses shared enforcement. Return the same generic challenge response whether or not a booking reference exists.

On successful verification, return an opaque bearer token generated from 256 bits of randomness. Store only its SHA-256 digest, scope it to one booking, and expire it after 15 minutes. `GET /bookings/{reference}` now requires this token in the `Authorization: Bearer` header and validates that it belongs to the booking in the route before loading details. Its response includes booking status, total and currency, hold expiry, screening title/auditorium/times, and saved seat-price snapshots; it does not return customer email or phone. Payment retry must use the same booking-scoped token when that endpoint is implemented. Never log verification codes or bearer tokens. Email verification proves access to the mailbox, not strong account authentication.

Use `POST /bookings/{reference}/email-verification-challenges` to request the code; the request contains no email address, and the endpoint returns the same generic `202 Accepted` response whether or not the booking exists. Use `POST /bookings/{reference}/email-verifications` with the code to receive the bearer token. Do not echo the stored email or tell the caller whether the booking reference matched.

**Checkpoint:** handler, service, and PostgreSQL integration tests cover generic challenge responses, code expiry and single use, attempt/send limits, token scope and expiry, and denial of booking details to callers without a valid booking-scoped token. A live Resend smoke test remains an operational check; the local HTTP test should verify that a delivered code returns a token and that the token unlocks only its matching booking's details.

## Step 5 — Create a durable payment initialization

**Goal:** make a valid booking eligible for the existing worker without making a provider call in the API transaction.

**Likely files:** `internal/checkout/service.go` for the application use case and its contracts; `internal/checkout/errors.go` for constructor/input errors; `internal/payment/store.go` for initialization request/result types and its creator contract; `internal/payment/postgres/initialization_store.go` for the transaction that creates or replays payment attempts and initializations; booking service/store fingerprint support; PostgreSQL integration tests; and an HTTP handler under `internal/httpapi`.

The client starts booking and payment with one request. An application-level use case coordinates the operations: first commit the booking and its seat claims, then create or replay the durable payment initialization in a separate transaction. Do not put this orchestration in the HTTP handler, and do not combine it with the booking transaction. The worker remains responsible for contacting Paystack after the initialization is committed.

Plan for the partial-failure boundary: if booking creation commits but initialization creation fails, the booking and hold remain. A retry with the same request must replay the original booking without extending its hold, then safely retry or replay initialization. If initialization committed but the response was lost, the retry must return that same initialization. Include payment method and consistent idempotency/fingerprint behavior in the coordinated request contract; do not let a retry silently change payment method.

The client idempotency key identifies the whole coordinated booking-and-payment-start request, including payment method: same key and same request replays the booking and initialization, while same key with changed input returns a conflict. This operation creates a booking, so a new key is not a way to retry payment for an existing booking. If the customer needs another initialization after a definitive rejection, expose a separate operation targeting that existing booking.

The checkout service canonicalizes the seat order and trimmed contact fields, adds the payment method, and hashes that complete input. It passes the fingerprint to booking creation for durable comparison before asking payment storage to create or replay the initialization.

In one payment-store transaction, create the `PENDING` payment attempt from the booking's saved seat-price snapshots and create its `QUEUED` initialization. Persist the generated provider reference before the worker calls Paystack. On a later retry for the same booking, return the existing unresolved initialization; create another initialization only after the latest one is definitively `REJECTED`. If the hold expired with no unresolved initialization, cancel the booking and payment attempt and release its seat claims.

The operation must verify that the booking is eligible, select the payment method, calculate the request fingerprint, and enforce the existing rule that only one unresolved initialization can exist for a payment attempt. Preserve retry semantics: only a definitive `REJECTED` result proving no checkout was created can allow a new initialization under the same attempt; an uncertain result cannot.

If the customer requests another payment method while an initialization is unresolved, return or point to the existing initialization instead of creating another one. The client can show its current state while the worker/webhook reconciles it. A confirmed `PAID` attempt cannot be initialized again.

For a retry on an existing booking, use an operation such as `POST /bookings/{reference}/payment-initializations`. Give each retry a fresh idempotency key and fingerprint. Replay the same initialization for a matching key and request; reject the key if its request changes. Create a new initialization for the same payment attempt only when its latest initialization is definitively `REJECTED`. Require the same customer verification used for protected booking details; the booking reference alone is not authorization.

**Checkpoint:** after the API operation commits, the worker can claim the `QUEUED` row without a test-only SQL insert. If the API response is lost and the client retries, idempotency returns the same initialization rather than scheduling a second provider request.

## Step 6 — Let the client obtain checkout details and current state

**Goal:** bridge the asynchronous worker and the customer UI.

**Likely files:** extend `internal/httpapi` with a read/status handler; add a focused read method to the package that consumes the store; implement it in the payment PostgreSQL adapter; add handler and store tests.

The initialization may be `QUEUED` or `SUBMITTING` before Paystack returns an authorization URL. Make that pending state explicit in the API contract. Once it becomes `READY`, return the stored authorization URL. Afterward, the same resource should let the client observe payment progress and terminal outcomes without trusting a browser redirect as proof of payment.

Do not make the request wait for the worker or synchronously call Paystack. The existing worker and verified webhook/reconciliation path remain authoritative for payment status.

**Checkpoint:** the customer can refresh or poll after a network interruption and recover the same initialization and checkout URL; a browser redirect alone cannot mark a booking paid.

## Step 7 — Wire dependencies and prove the real flow

**Goal:** remove the manual fixture from the happy path.

**Likely files:** `cmd/payment-api/main.go`, possibly a small `internal/app` package if setup is now shared or unwieldy, plus focused integration tests and local run documentation.

Wire the database store, service, handlers, and existing webhook handler explicitly. Keep the payment worker as its separate process. Start with a local API request that creates the booking and initialization, then observe the worker claim it and return a checkout URL. Complete a sandbox checkout and verify the database reaches `payment_initializations=PAID`, `payment_attempts=PAID`, and `bookings=BOOKED` while retaining the seat claim.

Also exercise the failure boundary: same-key replay, same key with changed seats, seat contention, a worker delay before URL availability, and an ambiguous payment result. The last case must not create a second initialization or release seats just because time passed.

**Checkpoint:** the customer path succeeds without `scripts/seed-payment-happy-path.sql`; the seed script remains useful only for isolated worker/webhook scenarios.

## Work order

Do not implement all seven steps in one pass. Finish Steps 1–3 first and review the transaction behavior before adding HTTP. Then add the payment initialization API and status read. The next concrete work session should begin with Step 1: write down the request, replay, and conflict outcomes, then ask for review before creating the Go types.
