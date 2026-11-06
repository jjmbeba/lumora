import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import type { components } from "@/api/generated";
import { ApiError, apiRequest, postJSON } from "@/lib/api";
import { getErrorMessage } from "@/lib/error-message";

const getAuthFailureMessage = (
  error: unknown,
  phase: "credentials" | "mfa"
) => {
  if (error instanceof ApiError && error.status === 401) {
    return phase === "credentials"
      ? "Email or password is incorrect."
      : "The authenticator code is invalid or the sign-in challenge has expired.";
  }

  if (error instanceof ApiError && error.status >= 500) {
    return "We couldn’t reach the sign-in service. Please try again shortly.";
  }

  if (error instanceof TypeError) {
    return "Unable to connect to the sign-in service. Check your connection and try again.";
  }

  return getErrorMessage(
    error,
    phase === "credentials"
      ? "Unable to sign in. Please try again."
      : "Unable to verify your code. Please try again."
  );
};

type LoginChallenge = components["schemas"]["StaffLoginChallenge"];
type LoginResult = components["schemas"]["StaffLoginResult"];

export const useStartStaffLoginMutation = () =>
  useMutation({
    mutationFn: (input: { email: string; password: string }) =>
      postJSON<LoginChallenge>("/api/v1/staff/auth/login", input),
    onError: (error) =>
      toast.error(getAuthFailureMessage(error, "credentials")),
    onSuccess: () => toast.success("Password accepted. Enter your MFA code."),
  });

export const useCompleteStaffLoginMutation = () =>
  useMutation({
    mutationFn: (input: { challenge: string; code: string }) =>
      postJSON<LoginResult>("/api/v1/staff/auth/mfa", input),
    onError: (error) => toast.error(getAuthFailureMessage(error, "mfa")),
    onSuccess: () => toast.success("Signed in successfully."),
  });

export const useLogoutStaffMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiRequest<undefined>("/api/v1/staff/auth/logout", {
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
