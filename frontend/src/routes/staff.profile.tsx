import { createFileRoute } from "@tanstack/react-router";

import { StaffProfilePage } from "@/components/staff-profile-page";

export const Route = createFileRoute("/staff/profile")({
  component: StaffProfilePage,
});
