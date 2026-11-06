import { useForm } from "@tanstack/react-form";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import type { components } from "@/api/generated";
import {
  useCompleteStaffLoginMutation,
  useStartStaffLoginMutation,
} from "@/features/staff/mutations/use-staff-auth-mutations";

type LoginChallenge = components["schemas"]["StaffLoginChallenge"];

export const StaffLoginPage = () => {
  const navigate = useNavigate();
  const [challenge, setChallenge] = useState<LoginChallenge | null>(null);
  const startLogin = useStartStaffLoginMutation();
  const completeLogin = useCompleteStaffLoginMutation();

  const credentialsForm = useForm({
    defaultValues: { email: "", password: "" },
    onSubmit: async ({ value }) => {
      try {
        const result = await startLogin.mutateAsync(value);
        setChallenge(result);
        credentialsForm.setFieldValue("password", "");
      } catch {
        // The mutation reports request failures through Sonner.
      }
    },
  });

  const mfaForm = useForm({
    defaultValues: { code: "" },
    onSubmit: async ({ value }) => {
      if (!challenge) {
        return;
      }
      try {
        await completeLogin.mutateAsync({
          challenge: challenge.challenge,
          code: value.code,
        });
        await navigate({
          to: "/staff/accounts",
          search: { page: 1, page_size: 20, search: "" },
        });
      } catch {
        // The mutation reports request failures through Sonner.
      }
    },
  });

  const isSubmitting = startLogin.isPending || completeLogin.isPending;

  return (
    <main className="mx-auto flex min-h-screen max-w-6xl items-center justify-center px-5 py-12">
      <section className="bg-card border-border w-full max-w-md rounded-2xl border p-7 shadow-sm sm:p-9">
        <a className="text-primary text-sm font-semibold" href="/">
          Lumora
        </a>
        <p className="text-muted-foreground mt-8 text-xs font-semibold tracking-[0.18em] uppercase">
          Staff access
        </p>
        <h1 className="mt-2 text-3xl font-semibold tracking-tight">
          {challenge ? "Verify it’s you" : "Sign in"}
        </h1>
        <p className="text-muted-foreground mt-2 text-sm leading-6">
          {challenge
            ? "Enter the six-digit code from your authenticator app to finish signing in."
            : "Use your staff email and password to continue."}
        </p>

        {challenge ? (
          <form
            className="mt-7 space-y-5"
            onSubmit={(event) => {
              event.preventDefault();
              event.stopPropagation();
              void mfaForm.handleSubmit();
            }}
          >
            <mfaForm.Field
              name="code"
              validators={{
                onChange: ({ value }) =>
                  /^\d{6}$/u.test(value)
                    ? undefined
                    : "Enter the six-digit authenticator code.",
              }}
            >
              {(field) => (
                <label
                  className="block space-y-2 text-sm font-medium"
                  htmlFor={field.name}
                >
                  Authenticator code
                  <input
                    autoComplete="one-time-code"
                    className="border-border bg-background focus-visible:ring-ring h-12 w-full rounded-lg border px-3 text-center text-lg tracking-[0.35em] outline-none focus-visible:ring-2"
                    id={field.name}
                    inputMode="numeric"
                    maxLength={6}
                    onBlur={field.handleBlur}
                    onChange={(event) =>
                      field.handleChange(
                        event.target.value.replaceAll(/\D/gu, "").slice(0, 6)
                      )
                    }
                    value={field.state.value}
                  />
                  {field.state.meta.isTouched &&
                  field.state.meta.errors.length > 0 ? (
                    <span className="text-destructive text-xs">
                      {field.state.meta.errors.join(" ")}
                    </span>
                  ) : null}
                </label>
              )}
            </mfaForm.Field>
            <button
              className="bg-primary text-primary-foreground h-11 w-full rounded-lg px-4 font-medium disabled:opacity-60"
              disabled={isSubmitting}
              type="submit"
            >
              {isSubmitting ? "Verifying…" : "Verify and sign in"}
            </button>
            <button
              className="text-muted-foreground hover:text-foreground w-full py-2 text-sm"
              disabled={isSubmitting}
              onClick={() => {
                setChallenge(null);
                mfaForm.reset();
              }}
              type="button"
            >
              Use a different account
            </button>
          </form>
        ) : (
          <form
            className="mt-7 space-y-5"
            onSubmit={(event) => {
              event.preventDefault();
              event.stopPropagation();
              void credentialsForm.handleSubmit();
            }}
          >
            <credentialsForm.Field
              name="email"
              validators={{
                onChange: ({ value }) =>
                  value.includes("@") ? undefined : "Enter a valid email.",
              }}
            >
              {(field) => (
                <label
                  className="block space-y-2 text-sm font-medium"
                  htmlFor={field.name}
                >
                  Email
                  <input
                    autoComplete="username"
                    className="border-border bg-background focus-visible:ring-ring h-11 w-full rounded-lg border px-3 outline-none focus-visible:ring-2"
                    id={field.name}
                    onBlur={field.handleBlur}
                    onChange={(event) => field.handleChange(event.target.value)}
                    type="email"
                    value={field.state.value}
                  />
                </label>
              )}
            </credentialsForm.Field>
            <credentialsForm.Field
              name="password"
              validators={{
                onChange: ({ value }) =>
                  value ? undefined : "Enter your password.",
              }}
            >
              {(field) => (
                <label
                  className="block space-y-2 text-sm font-medium"
                  htmlFor={field.name}
                >
                  Password
                  <input
                    autoComplete="current-password"
                    className="border-border bg-background focus-visible:ring-ring h-11 w-full rounded-lg border px-3 outline-none focus-visible:ring-2"
                    id={field.name}
                    onBlur={field.handleBlur}
                    onChange={(event) => field.handleChange(event.target.value)}
                    type="password"
                    value={field.state.value}
                  />
                </label>
              )}
            </credentialsForm.Field>
            <button
              className="bg-primary text-primary-foreground h-11 w-full rounded-lg px-4 font-medium disabled:opacity-60"
              disabled={isSubmitting}
              type="submit"
            >
              {isSubmitting ? "Signing in…" : "Continue"}
            </button>
          </form>
        )}
      </section>
    </main>
  );
};
