import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import type { components } from "@/api/generated";
import { apiRequest, postJSON } from "@/lib/api";
import { getErrorMessage } from "@/lib/error-message";

type LoginChallenge = components["schemas"]["StaffLoginChallenge"];
type LoginResult = components["schemas"]["StaffLoginResult"];

export const useStartStaffLoginMutation = () =>
  useMutation({
    mutationFn: (input: { email: string; password: string }) =>
      postJSON<LoginChallenge>("/staff/auth/login", input),
    onError: (error) =>
      toast.error(getErrorMessage(error, "Unable to sign in right now.")),
    onSuccess: () => toast.success("Password accepted. Enter your MFA code."),
  });

export const useCompleteStaffLoginMutation = () =>
  useMutation({
    mutationFn: (input: { challenge: string; code: string }) =>
      postJSON<LoginResult>("/staff/auth/mfa", input),
    onError: (error) =>
      toast.error(getErrorMessage(error, "Unable to complete sign in.")),
    onSuccess: () => toast.success("Signed in successfully."),
  });

export const useLogoutStaffMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiRequest<undefined>("/staff/auth/logout", {
        method: "POST",
        headers: { "X-Staff-Request": "1" },
      }),
    onSuccess: () => {
      queryClient.clear();
      toast.success("Signed out.");
    },
    onError: (error) =>
      toast.error(getErrorMessage(error, "Unable to sign out.")),
  });
};
