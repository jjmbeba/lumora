# Lumora frontend

React + Vite + TypeScript app using TanStack Router for routing, TanStack Query for server state, Tailwind CSS v4, and shadcn/ui components. Bun manages dependencies and the lockfile.

## Commands

```sh
bun install
bun run dev
bun run lint
bun run build
bun run api:types
```

Vite proxies `/staff` and `/bookings` to the local booking API at `http://localhost:8081` so browser requests remain same-origin during development.

For staff sign-in and logout during local development, set the booking API's `STAFF_FRONTEND_ORIGIN` to `http://localhost:5173`. The browser sends this origin automatically; the frontend adds the `X-Staff-Request: 1` header to logout requests.
