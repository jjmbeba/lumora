# Lumora

Lumora is a cinema ticketing application maintained as a monorepo.

- `backend/` contains the Go APIs, workers, migrations, and backend tests.
- `frontend/` contains the Bun-managed React, Vite, and TypeScript application.
- `api/openapi.yaml` is the language-neutral HTTP contract used to generate frontend types.
- `docs/` contains shared architecture and implementation notes.

Run Go commands from `backend/`. Start PostgreSQL from the repository root with `docker compose up -d db`.
Run the web app from `frontend/` with `bun install` followed by `bun run dev`.

For local backend development, start PostgreSQL first, then run `cd backend && go run ./cmd/dev`.
The supervisor loads missing values from the root `.env`, starts both APIs and all three workers,
prefixes their logs, and stops the process group when interrupted. Run the frontend separately
with `cd frontend && bun run dev`.

To route every Resend message to your Resend account email during local development, set
`APP_ENV=development` and `RESEND_TEST_RECIPIENT=your-account-email` in `.env`. This redirects
booking codes, ticket emails, and staff invitations; use test data because messages can contain
credentials and invitation links. Remove `RESEND_TEST_RECIPIENT` to restore normal recipient routing.
The shared sender rejects this override when `APP_ENV` is not `development`.
