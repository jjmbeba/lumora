# Go project structure for the cinema backend

**Recommendation:** start with one Go module at the repository root, one API command, and a separate payment worker command. Keep application packages under `internal/`, organize behavior around the cinema’s domain, and keep PostgreSQL migrations as SQL files in a root `migrations/` directory. This is a proposed starting point, not a layout mandated by Go.

Go’s official server-project guidance recommends `cmd/` for commands and `internal/` for server implementation packages; the `internal` rule prevents imports from outside the allowed parent tree. Go also explicitly recognizes that server layouts vary with protocols, deployment, and non-Go assets. ([Organizing a Go module](https://go.dev/doc/modules/layout))

## Proposed initial tree

```text
go.mod
go.sum
cmd/
  api/
    main.go                 # process entry point; flags/config and lifecycle
  payment-worker/
    main.go                 # queue worker entry point
internal/
  app/                      # dependency construction and process wiring
  config/                   # parse and validate runtime configuration
  httpapi/                  # HTTP routes, decoding, response mapping
  booking/                  # booking and atomic seat-claim use cases
    postgres/               # booking persistence adapter
  screening/                # screening and seat-map behavior
    postgres/               # screening persistence adapter
  payment/                  # attempts, initialization state, reconciliation policy
    postgres/               # payment persistence and worker-claim SQL
  paystack/                 # provider client and provider-specific mapping
migrations/
  000001_*.sql
docs/
```

This is intentionally a modest modular monolith: the API and worker are separate processes/commands, but share one module, one database, and internal packages. The payment worker is justified by the durable initialization queue and lease recovery already in the domain context; it should reuse payment behavior rather than duplicate payment rules. Keep cross-package wiring in `internal/app` so feature packages do not import the command binaries or one another in cycles.

## Package and dependency rules

- Treat a directory as a package boundary only when it gives a coherent responsibility and usable API. Prefer names such as `booking`, `payment`, and `screening`; avoid catch-all `common`, `util`, `types`, or `interfaces` packages. The Go team’s package guidance says names should explain contents, and package naming can reveal a poor abstraction boundary. ([Package names](https://go.dev/blog/package-names), [Code Review Comments](https://go.dev/wiki/CodeReviewComments))
- Keep imports flowing inward from transport and persistence adapters to domain/use-case code, with `app` doing construction. Do not create a package for every noun or layer in advance. A package may span multiple `.go` files without requiring another directory.
- Keep the module path tied to the eventual repository location when known. The root `go.mod` describes the module; package directories below it form the module’s import paths. Start with one module: Go’s official module guidance says this is simpler to manage, while independently versioned submodules add release and tagging work. Split only if code truly needs independent versioning or external reuse. ([Managing module source](https://go.dev/doc/modules/managing-source))
- Keep package names short, lowercase, and non-redundant in use (`payment.Initialization`, not `payment.PaymentInitialization`). Avoid a universal exported API/types package. ([Package names](https://go.dev/blog/package-names))

## Tests, configuration, and SQL migrations

- Put focused package tests beside the code in `*_test.go` files and run them with `go test`; the standard Go test framework is package-oriented. Add database-backed integration tests only when they exercise PostgreSQL behavior that a unit test cannot establish, especially transaction/locking invariants. ([How to Write Go Code: Testing](https://go.dev/doc/code#Testing))
- Keep `main.go` small: load and validate configuration, construct dependencies, start the server or worker, and coordinate shutdown. Treat the exact configuration mechanism (environment variables, flags, or files) as a product/deployment decision; Go’s layout guidance does not prescribe one.
- Store ordered, reviewable `.sql` migration files under `migrations/`, outside `internal/` so operational tooling can locate them without importing application packages. Decide the migration runner and whether SQL is embedded later. Go’s `embed` package can include files at compile time, but it is a mechanism, not a migration policy. ([embed package](https://pkg.go.dev/embed))
- Keep database transaction boundaries explicit in the persistence/application operation. Go’s `database/sql` documentation describes `sql.Tx` as the mechanism for grouping operations that must commit or roll back together; the cinema’s atomic seat claim and worker claim/reclaim rules should remain visible as transactions in their owning operations. ([Executing transactions](https://go.dev/doc/database/execute-transactions))

## What this proposal does not settle

It does not select an HTTP router, SQL driver/query generator, migration tool, dependency-injection framework, or exact repository interfaces. Those are separate choices; selecting them before the first vertical slice would add structure without evidence. In particular, Go does not require a `service/repository/model` three-layer layout, nor does it prescribe a domain-driven folder tree.

## Sources and scope

Sources are official Go documentation and Go-maintained guidance, accessed 2026-10-13. They establish language/tool conventions and supported mechanisms, not proof that this particular tree is optimal. The tree is an inference from the project’s current single-cinema scope, PostgreSQL choice, booking transaction needs, and durable payment-initialization worker.
