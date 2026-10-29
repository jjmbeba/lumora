# Payment initialization worker claim and recovery

This protocol uses `payment_initializations` as the durable queue. A worker owns a row only while its `SUBMITTING` lease is valid. The transaction that claims a row is short and commits before any Paystack request.

## Claim candidates

A worker may claim:

- A `QUEUED` row whose `next_attempt_at` is null or due. This is the first initialization attempt.
- An `UNKNOWN` row whose `next_attempt_at` is null or due. This is a reconciliation attempt.
- A `READY` row whose `next_attempt_at` is due. This is a scheduled payment-verification attempt.
- A `SUBMITTING` row whose lease has expired. This is recovery after a worker may have contacted Paystack.

## Atomic claim

Within one database transaction, select an eligible row using `FOR UPDATE SKIP LOCKED`, remember its previous status, and update it atomically:

```sql
BEGIN;

SELECT id, status
FROM payment_initializations
WHERE (
    status IN ('QUEUED', 'UNKNOWN', 'READY')
    AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp())
  ) OR (
    status = 'SUBMITTING'
    AND lease_expires_at <= clock_timestamp()
  )
ORDER BY COALESCE(next_attempt_at, lease_expires_at, created_at), id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- Using the selected id and previous status:
UPDATE payment_initializations
SET status = 'SUBMITTING',
    worker_attempt_count = worker_attempt_count + 1,
    lease_expires_at = clock_timestamp() + interval '60 seconds',
    next_attempt_at = NULL,
    updated_at = clock_timestamp()
WHERE id = :id;

COMMIT;
```

The interval is illustrative; configure the lease to exceed the provider HTTP timeout with a safety margin. Use the database-returned `worker_attempt_count` as this claim's generation token. The selected previous status determines the work: initialize only from `QUEUED`; verify the stored reference from `READY`; reconcile first from `UNKNOWN` or expired `SUBMITTING`.

PostgreSQL documents `SKIP LOCKED` as useful for queue-like consumers that should avoid waiting on rows claimed by other workers. It intentionally does not provide a general consistent view, which is appropriate here because workers seek available jobs rather than a complete snapshot. See [PostgreSQL locking clauses](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE).

## Provider work and state updates

- `Provider.Initialize` has an explicit uncertainty contract: a non-nil error does not prove Paystack did nothing. A timeout can happen after Paystack accepted the request, and a malformed/lost response can hide a successful checkout. Treat any initialization error as an unknown outcome, persist/retry as `UNKNOWN`, and reconcile using the stored reference before another initialization call. Return `ResultStatusRejected` with no error only when Paystack's response definitively confirms that no checkout was created. Return `ResultStatusUnknown` with no error when the adapter received a usable response that still does not establish the outcome. The worker must not infer a definitive rejection from a generic error.
- For `QUEUED`, use the already-persisted Paystack reference. Never generate a replacement reference during a worker retry.
- For expired `SUBMITTING` and due `UNKNOWN`, verify/reconcile by that stored reference before considering another initialization request.
- If Paystack definitively confirms checkout creation and the checkout URL is available, transition to `READY`, clear the lease, and set `next_attempt_at` to schedule the first verification.
- When a due `READY` row is claimed, verify the stored Paystack reference. If payment remains pending, return it to `READY`, clear the lease, and set `next_attempt_at` for the next verification. If verification is uncertain, transition to `UNKNOWN` and schedule reconciliation with backoff.
- If verification confirms the payment transaction has terminally failed while the payment attempt is `PENDING`, transition the initialization and payment attempt to `FAILED`, cancel the booking, and delete its active `seat_claims` in one database transaction; retain `bookings_seats` as history and clear the lease and retry time. An already-`FAILED` attempt may complete this cleanup idempotently. If the booking is already `BOOKED`, or the payment attempt is already `PAID` or `CANCELLED`, quarantine the conflict by setting the initialization to `REVIEW_REQUIRED` and a non-cancelled booking to `PAYMENT_REVIEW`; preserve payment status and seat claims. If the booking was already cancelled, do not reacquire seats. This is different from `REJECTED`, which means Paystack confirmed no checkout was created.
- Paystack `abandoned` means checkout has not completed yet; it is not terminal for our booking flow because the same transaction can later succeed. Treat it as pending and schedule verification while the booking hold is valid. The PostgreSQL transition checks `hold_expires_at` under the same claim-fenced transaction as the reschedule. Once the hold has expired, move the initialization to `REVIEW_REQUIRED` and a non-cancelled booking to `PAYMENT_REVIEW`; preserve payment-attempt status and existing seat claims for staff reconciliation. Do not automatically cancel/release seats based only on `abandoned`.
- If a `charge.success` webhook arrives after an initialization was already cancelled and its booking's seat claims were released, move the initialization to `REVIEW_REQUIRED` and retain the booking's existing cancelled state. Do not reacquire seats or automatically mark the payment/booking paid/booked; staff must reconcile the late payment.
- If verification returns Paystack status `reversed`, set the initialization to `REVIEW_REQUIRED` and a non-cancelled booking to `PAYMENT_REVIEW` in one fenced transaction. Preserve payment-attempt status and seat claims because reversal may mean a refund or chargeback. If the booking was already cancelled, do not reacquire claims that may now belong to another booking; keep the initialization unresolved for staff reconciliation.
- `MarkFailed` locks the initialization, payment attempt, and booking before choosing a transition, so it acts on their current states. If verification reports failure while the payment attempt is already `PAID` or `CANCELLED`, preserve that payment status, move a non-cancelled booking to `PAYMENT_REVIEW`, and move the initialization to `REVIEW_REQUIRED` in one transaction. `REVIEW_REQUIRED` is excluded from worker claims but included in the one-unresolved-initialization constraint so another initialization cannot be created during review. A `BOOKED` booking is also a conflict; retain its seat claims and quarantine it for review.
- If Paystack definitively confirms that no checkout was created, transition to `REJECTED` and clear the lease. A later customer retry creates a new initialization with a new key and reference.
- If the result remains uncertain, transition to `UNKNOWN`, clear the lease, and set `next_attempt_at` with backoff. Keep this row active so a second initialization cannot be created. If reconciliation remains unresolved, follow the existing booking-review policy.
- If a transaction is found but the checkout URL cannot be recovered, do not create another initialization. Keep the outcome unresolved for review; `READY` requires the checkout details the customer needs.

Every worker-owned result update must be fenced:

```sql
UPDATE payment_initializations
SET status = :new_status,
    lease_expires_at = NULL,
    next_attempt_at = :next_attempt_at,
    updated_at = clock_timestamp()
WHERE id = :id
  AND status = 'SUBMITTING'
  AND worker_attempt_count = :claimed_generation;
```

If zero rows are updated, the lease was superseded or the state changed. Discard that worker's result; it must not overwrite the newer owner's outcome. Increment the generation on every claim and never reset it.

The generation fence protects database writes, not an HTTP request already in flight. Therefore an expired worker may still complete a Paystack call after another worker takes ownership. Do not treat a lease as provider-side cancellation or assume that repeating `POST /transaction/initialize` with the same reference is idempotent. Paystack documents a unique transaction reference and a verification endpoint, but the public API documentation does not promise idempotent initialization. Keep ambiguous outcomes in `UNKNOWN` until provider behavior can establish a definitive no-checkout result; validate that behavior in the merchant sandbox before automating any retry after an ambiguous response. See [Paystack transaction API](https://paystack.com/docs/api/transaction/) and [transaction errors](https://paystack.com/docs/api/errors/transaction/).

## Database support and limits

- `worker_attempt_count` must be nonnegative; `SUBMITTING` rows must have a lease and non-`SUBMITTING` rows must not.
- The partial unique index for unresolved states prevents parallel or retried initialization rows per payment attempt.
- The due-work index covers `QUEUED`, `UNKNOWN`, and `READY`; the expired-lease index covers `SUBMITTING`.
- The indexes aid candidate lookup but do not implement the claim protocol. The application transaction and generation-guarded updates provide the ownership behavior.
- Verify/reconcile responses, especially how a missing reference is interpreted after an ambiguous initialization, remain provider-specific integration behavior to prove in the sandbox.

## Possible v2 evolution

Redis Streams is not part of this v1 claim protocol. A concrete reason to revisit it would be a v2 booking-confirmation workflow that must fan out to separately scaled ticket-issuance, notification, and analytics consumers, with consumer-group recovery and event replay. If adopted, record the booking/payment state and an outbox event together in PostgreSQL, then publish the event to Streams. Treat Stream delivery as at-least-once: consumers must be idempotent, and PostgreSQL remains authoritative for booking and payment state. Do not mirror the current payment-initialization queue into Redis without a separate need; two competing work queues would add coordination and recovery failure modes.
