import { useQuery } from "@tanstack/react-query";

import type { components } from "@/api/generated";
import { ApiError, apiRequest } from "@/lib/api";

export type StaffSession = components["schemas"]["StaffSession"];

export const staffSessionQueryKey = ["staff", "session"] as const;

export const useStaffSessionQuery = () =>
  useQuery({
    queryKey: staffSessionQueryKey,
    queryFn: () => apiRequest<StaffSession>("/api/v1/staff/auth/session"),
    retry: (failureCount, error) =>
      error instanceof ApiError && error.status < 500
        ? false
        : failureCount < 2,
  });
