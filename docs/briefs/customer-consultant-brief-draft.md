# Customer–Consultant Challenge: Reliable Cinema Ticketing Backend

**Status:** Draft v2.0 — customer, primary pain, five-minute checkout holds, Paystack M-Pesa and card checkout, payment ambiguity handling, retry policy, booking/payment transitions, and per-seat screening pricing recorded; remaining workflow discovery continues

**Implementation language:** Go

**Reference:** [Backend from First Principles](https://backend-from-first-principle.vercel.app/)

## Engagement premise

A single independent cinema wants a dependable service for publishing movie screenings, selling seats, and admitting customers. You are an independent consultant asked to investigate the booking workflow, recommend a design, build the backend, and hand over a product the cinema team can operate.

The system could list standard and IMAX-format screenings if the customer offers them. The hypothetical customer is the cinema operator. The project is not to recreate IMAX’s business or technology.

This is a fictional customer scenario. The problem statement is a starting hypothesis, not a claim about a real cinema’s requirements.

## Agreed challenge premise

The cinema’s primary problem for this challenge is seat conflicts during busy showtimes: customers and staff need confidence that two customers cannot both end up with the same seat for the same screening. This is the customer outcome the first release must address.

Other cinema sales and admission failures may also cost revenue or customer trust, but they remain discovery topics rather than agreed scope:

- Two customers may try to buy the same seat at nearly the same time.
- A retry or delayed payment notification may create duplicate or contradictory bookings.
- A screening change or cancellation may leave customers and staff uncertain about tickets and refunds.
- Door staff may be unable to validate a ticket or identify a booking that needs investigation.

Discovery must establish which failures matter to the customer, how the current process handles them, and what business rules apply.

## Consultant outcomes

The engagement should leave the customer with:

1. An agreed problem statement, user workflows, constraints, and success measures.
2. A documented API and data contract with explicit booking and payment state semantics.
3. A Go backend that implements the agreed workflows and protects the customer’s critical invariants.
4. Evidence from appropriate tests, including database-backed and concurrent booking checks.
5. A repeatable deployment and configuration process, with security and operational guidance.
6. Handover documentation: how to use, deploy, monitor, back up, restore, and troubleshoot the service.

## Candidate first release

Subject to discovery, a first release could support:

- Cinema staff creating movies, auditoriums, seat layouts, and scheduled screenings.
- Customers browsing screenings and available seats through a documented API.
- Customers holding selected seats for five minutes during checkout, then completing or abandoning a booking. If the hold expires while payment is ambiguous, keep the seats unavailable until payment is resolved. If automated reconciliation cannot resolve it, flag it for staff review and keep the seats unavailable until staff act; do not silently make them bookable again.
- Recording payment outcomes and issuing tickets only under agreed conditions.
- Staff looking up a booking by the cinema’s booking reference and resolving incomplete or ambiguous transactions. The backend uses the linked Paystack transaction reference to check payment status; a ticket number is issued only after verified payment.
- Door staff validating a ticket and recording admission.

The current customer scope is one independent cinema at one location. Kenya is the working market. Paystack is selected for the initial online payment integration, and the first release must offer both M-Pesa and card checkout, subject to confirming merchant eligibility and validating both methods in Paystack's sandbox. Direct production Daraja verification is outside the first-release scope: this is a fictional cinema without a live Paybill/Till, and the sandbox experience has been unreliable. A deterministic local payment simulator will support development of payment and booking behavior; it does not count as proof of Paystack or Daraja production behavior. The release does not yet commit to a frontend, refunds, seat transfers, offline door operation, chain management, dynamic pricing, or any specific infrastructure. Those are customer decisions, not assumed technical defaults.

## Candidate acceptance outcomes

These should be refined with the customer before implementation:

- **Seat integrity:** a seat cannot be confirmed twice for the same screening; simultaneous booking attempts preserve this invariant. A temporary hold prevents another customer from checking out the same seat concurrently.
- **Hold lifecycle:** the initial seat hold lasts five minutes. After it expires, seats remain unavailable while payment is pending or otherwise ambiguous. If automatic reconciliation cannot resolve payment, flag the booking for staff review and continue reserving the seats until staff act. When Paystack confirms a final failure, fail the booking and release its seats.
- **Retry safety:** repeating an eligible booking request or payment notification does not create a second booking or ticket. A new customer payment attempt is allowed only after a provider-confirmed final failure; because the failed booking releases its seats, the customer must select and hold available seats again. Duplicate external charges are prevented through the provider’s supported idempotency mechanism where available; otherwise ambiguous attempts are surfaced for reconciliation rather than blindly retried.
- **Payment ambiguity:** a timeout or missing callback remains distinguishable from a confirmed failure. Staff locate the booking by its cinema booking reference; the backend checks Paystack using the linked provider transaction reference. Staff may request a provider recheck or escalate, but cannot manually mark payment successful. Staff rely on provider-verified transaction status rather than a customer-provided screenshot or ticket number.
- **Payment integration:** Paystack is the selected provider, with M-Pesa and card checkout required for the first release, subject to merchant onboarding and sandbox validation for both methods. Verify callback signatures, transaction status, retry/reconciliation behavior, and payment creation retry semantics against current official guidance. Local simulation supports domain testing but is not provider-integration evidence.
- **Authorization:** each role can perform only its agreed actions; tests demonstrate unauthorized access to another customer’s booking or a staff-only action is rejected.
- **Admission integrity:** the customer’s chosen re-entry policy is enforced consistently, including concurrent scans.
- **Operability:** a new operator can configure and deploy the service, identify failures from its telemetry, and follow a tested backup/restore procedure.

Numeric availability, latency, traffic, recovery, and retention targets are intentionally unset. They must come from customer workload and risk discovery, then be made measurable.

## Draft booking and payment states

These states capture the agreed checkout behavior and will be refined as workflow discovery continues:

| Booking state | Meaning |
|---|---|
| `RESERVED` | Seats are held during the five-minute checkout window. |
| `PAYMENT_REVIEW` | Payment remains unresolved after automatic checks; seats stay unavailable and staff may review, recheck, or escalate. Staff cannot manually mark payment successful. |
| `BOOKED` | Paystack success has been verified; tickets may be issued. |
| `CANCELLED` | The customer cancelled before payment was initiated, or a verified terminal outcome permits cancellation; seats are released. Paystack `abandoned` alone is not terminal. |

| Payment attempt state | Meaning |
|---|---|
| `PENDING` | No final provider result is known, including a timeout or a customer leaving checkout. |
| `PAID` | Paystack success has been verified. |
| `FAILED` | Paystack confirmed a final failure; store the failure reason separately. |
| `CANCELLED` | The payment was explicitly cancelled or a verified terminal cancellation was confirmed; Paystack `abandoned` remains `PENDING`. |

The five-minute checkout deadline is not a payment outcome. Automatic verification continues while the booking hold is valid. If payment is still unresolved when the hold expires, move the booking to `PAYMENT_REVIEW` and keep its current seat claims unavailable for staff review. A customer closing checkout does not by itself prove provider cancellation. Paystack `abandoned` means checkout is incomplete; it remains retryable during the hold and does not release seats by itself. A late `charge.success` after cancellation requires staff reconciliation and never restores released claims. A verified `reversed` status moves the booking to `PAYMENT_REVIEW` and keeps existing claims for reconciliation. A server-side verified success books the seats and allows ticket issuance. Success verification must match the stored Paystack transaction reference for the payment attempt, expected amount, currency, and successful provider status. A new customer attempt after failure requires selecting and holding available seats again.

### Agreed state transitions

| Current booking state | Event | New booking state | Required condition |
|---|---|---|---|
| `RESERVED` | Automatic reconciliation finishes without a final result | `PAYMENT_REVIEW` | The five-minute hold has expired; payment remains unresolved; seats remain unavailable. |
| `RESERVED` | Paystack payment is verified | `BOOKED` | Server-side verification matches the stored payment-attempt reference, expected amount and currency, and successful provider status. |
| `RESERVED` | Paystack confirms a terminal unsuccessful outcome | `CANCELLED` | The result matches the stored payment-attempt reference and confirms final failure or cancellation; `abandoned` is excluded; release seats. |
| `PAYMENT_REVIEW` | Staff-triggered recheck verifies payment | `BOOKED` | Same payment-reference, amount, currency, and successful-status checks as above. |
| `PAYMENT_REVIEW` | Paystack confirms a terminal unsuccessful outcome | `CANCELLED` | The result matches the stored payment-attempt reference and confirms final failure or cancellation; `abandoned` is excluded; release seats. |
| `RESERVED` | Customer cancels before a payment attempt is created | `CANCELLED` | No Paystack payment attempt has started; release seats. |

Payment attempt transitions are separate from booking transitions: `PENDING` becomes `PAID`, `FAILED`, or `CANCELLED` only after the corresponding Paystack result is verified against that attempt's stored reference. If a recheck remains unresolved, the booking stays in `PAYMENT_REVIEW` and the payment remains `PENDING`. Duplicate callbacks or rechecks must not issue duplicate tickets or otherwise apply fulfillment twice.

### First-pass data model decisions

- A screening is a scheduled movie showing in one auditorium; it records start and end times. A movie stores its runtime separately from the scheduled auditorium occupancy interval.
- Each seat offered in a screening has a screening-specific price. The booking-seat record snapshots the price and currency at booking time; payment attempts record the total being charged.
- Each booking has a unique customer-facing `booking_reference`, separate from its internal database ID, plus purchaser contact details.
- Each booking has one payment attempt with its amount and currency, and may have multiple payment-initialization records. Each initialization stores its own idempotency key, payment method (`CARD` or `MPESA`), Paystack reference, status, and checkout URL when available. Only one initialization may be active or unresolved for an attempt at a time. If Paystack definitively rejects initialization and confirms no checkout was created, a new initialization may be created under the same payment attempt while the original booking hold remains valid; if this known-no-checkout case reaches hold expiry before another initialization is created, cancel the booking and release its seats. If payment may have reached Paystack and remains unresolved at expiry, move the booking to review and preserve its existing claims. Do not treat initialization rejection as a terminal payment failure. A confirmed payment failure ends that booking; retry then requires a new booking and seat selection.
- `bookings_seats` preserves the historical seat selection for a booking. `seat_claims` records current seat occupancy and is removed when a confirmed failure or cancellation releases the seats. Composite foreign keys keep both records aligned with the booking's screening and auditorium layout.
- Positive movie runtime and payment amount, screening end after start, and hold expiry after booking creation are data constraints in the first-pass schema.

The editable DBML draft is [cinema-ticketing-schema.dbml](../architecture/cinema-ticketing-schema.dbml). It does not yet model the webhook receipt workflow, issued tickets/admission scans, or the database-specific rule that prevents overlapping screenings in one auditorium; those remain later design steps.

## Discovery questions to resolve

- Who manages movie listings, auditoriums, seat layouts, screenings, bookings, support, and admission?
- Are all seats assigned, or are some screenings general admission? Can customers select adjacent seats?
- Can the hypothetical cinema qualify for a Paystack merchant account, and will both M-Pesa and card checkout be enabled in its account and sandbox?
- What are Paystack’s current callback, retry, refund, settlement, and reconciliation rules for the enabled Kenyan methods?
- Is M-Pesa mandatory, or can checkout offer alternatives such as cards or Airtel Money when a mobile-money rail is unavailable?
- What should happen when a screening is moved, cancelled, or starts late?
- Can bookings be changed, transferred, refunded, or used for re-entry?
- How many locations, auditoriums, screenings, seats, concurrent checkouts, and staff should the system support?
- What customer data may be collected, how long may it be retained, and who may access it?
- What deployment, budget, delivery, support, and recovery constraints does the cinema have?

## Learning and delivery sequence

1. **Discovery and proposal:** interview the customer (role-played as needed), record assumptions, define success measures, and present a scoped proposal.
2. **Contract and domain:** describe workflows, resources and state transitions, API semantics, authorization rules, and failure behavior before choosing framework details.
3. **Core service:** implement the agreed vertical workflow in Go with persistence, migrations, validation, and clear error handling.
4. **Reliability:** prove seat allocation and retry invariants; add background work or payment reconciliation only where the agreed workflow requires it.
5. **Security and quality:** review threats and access boundaries; add unit, integration, contract, and concurrency tests appropriate to the risks.
6. **Production delivery:** package, configure, deploy, observe, shut down cleanly, and document recovery and support procedures.
7. **Customer handover:** demonstrate acceptance outcomes, explain known limits, deliver operator material, and conduct a retrospective.

At each stage, you make and explain the design and implementation decisions. The coach reviews your reasoning and evidence, asks targeted questions, and teaches concepts at the point they become useful.

## Curriculum mapping

Likely core topics include HTTP and routing, serialization, authentication and authorization, validation, API design, databases, error handling, configuration, logging and observability, graceful shutdown, security, concurrency, deployment, automated testing, and API documentation.

Caching, full-text search, gRPC, Kafka, WebSockets, Kubernetes, and AI-agent workflows remain optional investigations unless discovery yields a concrete requirement. The aim is to apply the relevant engineering principles and explain the trade-offs, not to install every technology named in the reference.

## Decisions still open

- **Customer:** one independent cinema at one location is the agreed working scope.
- **Primary pain:** prevent seat conflicts during busy showtimes.
- **Checkout:** selected seats are held for five minutes. If payment is ambiguous when the hold expires, keep seats unavailable. If automated reconciliation cannot resolve it, flag for staff review and retain the seats until staff act. Staff may request a provider recheck or escalate, but may not manually mark payment successful. A provider-confirmed final failure fails the booking and releases the seats. The customer may try again only by selecting and holding available seats again.
- **Online payment:** Paystack is selected for the initial integration, with M-Pesa and card checkout required. Merchant onboarding and sandbox validation for both methods remain gates. Direct production Daraja verification is deferred until a legitimate merchant shortcode is available; local simulation is not treated as real-provider verification.
- **Data model:** the first-pass DBML records a screening-specific price for each offered seat and snapshots the unit price on each booking-seat row. It also separates historical booking-seat selections from active seat claims; one booking has one payment attempt; a unique booking reference is distinct from the internal ID. See [the schema draft](../architecture/cinema-ticketing-schema.dbml).
- **Workflow and release boundary:** to be agreed after discovery.
- **Seat-hold and payment rules:** to be discovered before selecting persistence and integration behavior.
- **Service targets and operating environment:** to be set from customer constraints.
- **Refund, cancellation, re-entry, and offline policies:** to be discovered before selecting state transitions.
