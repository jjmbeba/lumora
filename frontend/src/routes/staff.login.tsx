import { createFileRoute } from "@tanstack/react-router";

import { StaffLoginPage } from "@/components/staff-login-page";

export const Route = createFileRoute("/staff/login")({
  component: StaffLoginPage,
});
