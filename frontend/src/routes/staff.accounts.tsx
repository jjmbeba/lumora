import { createFileRoute } from "@tanstack/react-router";

import { StaffAccountsPage } from "@/components/staff-accounts-page";

export const Route = createFileRoute("/staff/accounts")({
  validateSearch: (search: Record<string, unknown>) => ({
    page: Math.max(1, Math.trunc(Number(String(search.page ?? "1"))) || 1),
    page_size: Math.min(
      100,
      Math.max(1, Math.trunc(Number(String(search.page_size ?? "20"))) || 20)
    ),
    search:
      typeof search.search === "string" ? search.search.slice(0, 100) : "",
  }),
  component: StaffAccountsPage,
});
