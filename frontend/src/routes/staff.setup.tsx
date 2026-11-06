import { createFileRoute } from "@tanstack/react-router";

import { StaffSetupPage } from "@/components/staff-setup-page";

export const Route = createFileRoute("/staff/setup")({
  validateSearch: (search: Record<string, unknown>) => ({
    token: typeof search.token === "string" ? search.token : "",
  }),
  component: StaffSetupPage,
});
