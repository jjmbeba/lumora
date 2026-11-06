import { useQuery } from "@tanstack/react-query";

import type { components } from "@/api/generated";
import { ApiError, apiRequest } from "@/lib/api";

type StaffAccountPage = components["schemas"]["StaffAccountPage"];

export interface StaffAccountSearch {
  page: number;
  page_size: number;
  search: string;
}

export const useStaffAccountsQuery = (params: StaffAccountSearch) =>
  useQuery({
    queryKey: ["staff", "accounts", params],
    queryFn: () => {
      const query = new URLSearchParams({
        page: String(params.page),
        page_size: String(params.page_size),
      });
      if (params.search) {
        query.set("search", params.search);
      }
      return apiRequest<StaffAccountPage>(`/staff/accounts?${query}`);
    },
    retry: (failureCount, error) =>
      error instanceof ApiError && error.status < 500
        ? false
        : failureCount < 2,
  });
