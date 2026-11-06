import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import type { components } from "@/api/generated";
import { apiRequest, postJSON } from "@/lib/api";
import { getErrorMessage } from "@/lib/error-message";

type StaffInvitationResult = components["schemas"]["StaffInvitationResult"];

const staffRequestHeaders = { "X-Staff-Request": "1" };

export const useCreateStaffInvitationMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      email: string;
      display_name: string;
      role: string;
    }) =>
      postJSON<StaffInvitationResult>("/api/v1/staff/accounts", input, {
        headers: staffRequestHeaders,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["staff", "accounts"] });
      toast.success("Invitation email queued.");
    },
    onError: (error) =>
      toast.error(getErrorMessage(error, "Could not invite staff.")),
  });
};

export const useResendStaffInvitationMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (accountID: number) =>
      postJSON<StaffInvitationResult>(
        `/api/v1/staff/accounts/${accountID}/invitation`,
        {},
        { headers: staffRequestHeaders }
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["staff", "accounts"] });
      toast.success("Invitation email queued again.");
    },
    onError: (error) =>
      toast.error(getErrorMessage(error, "Could not resend invitation.")),
  });
};

export const useDeactivateStaffAccountMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (accountID: number) =>
      apiRequest<undefined>(
        `/api/v1/staff/accounts/${accountID}/deactivation`,
        {
          method: "POST",
          headers: staffRequestHeaders,
        }
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["staff", "accounts"] });
      toast.success("Staff account deactivated.");
    },
    onError: (error) =>
      toast.error(
        getErrorMessage(error, "Could not deactivate staff account.")
      ),
  });
};

export const useStartStaffEnrollmentMutation = () =>
  useMutation({
    mutationFn: (invitation_token: string) =>
      postJSON<components["schemas"]["StaffEnrollmentChallenge"]>(
        "/api/v1/staff/invitations/setup/start",
        { invitation_token }
      ),
    onError: (error) =>
      toast.error(
        getErrorMessage(error, "This invitation is invalid or expired.")
      ),
  });

export const useCompleteStaffEnrollmentMutation = () =>
  useMutation({
    mutationFn: (input: {
      invitation_token: string;
      challenge: string;
      password: string;
      code: string;
    }) =>
      postJSON<components["schemas"]["StaffEnrollmentResult"]>(
        "/api/v1/staff/invitations/setup/complete",
        input
      ),
    onError: (error) =>
      toast.error(getErrorMessage(error, "Could not complete staff setup.")),
  });
