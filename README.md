# Lumora

Lumora is a cinema ticketing application maintained as a monorepo.

- `backend/` contains the Go APIs, workers, database migrations, and backend tests.
- `frontend/` is reserved for the React, Vite, and TypeScript web application.
- `api/openapi.yaml` is the language-neutral HTTP contract used to generate frontend TypeScript types.
- `docs/` contains shared architecture and implementation notes.

Run Go commands from `backend/`. Start the local PostgreSQL dependency from the repository root with `docker compose up -d db`.
