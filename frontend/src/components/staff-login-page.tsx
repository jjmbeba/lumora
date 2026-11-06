import { useForm } from "@tanstack/react-form";
import { useNavigate } from "@tanstack/react-router";
import { Clapperboard, Eye, EyeOff } from "lucide-react";
import { useState } from "react";
import * as v from "valibot";

import type { components } from "@/api/generated";
import { ThemeToggle } from "@/components/theme-toggle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  useCompleteStaffLoginMutation,
  useStartStaffLoginMutation,
} from "@/features/staff/mutations/use-staff-auth-mutations";

type LoginChallenge = components["schemas"]["StaffLoginChallenge"];

const emailSchema = v.pipe(v.string(), v.email("Enter a valid email address."));
const passwordSchema = v.pipe(
  v.string(),
  v.minLength(1, "Enter your password.")
);
const mfaCodeSchema = v.pipe(
  v.string(),
  v.regex(/^\d{6}$/u, "Enter the six-digit authenticator code.")
);

const validateWith =
  <T,>(schema: v.GenericSchema<T>) =>
  ({ value }: { value: T }) => {
    const result = v.safeParse(schema, value);
    return result.success
      ? undefined
      : result.issues.map((issue) => issue.message);
  };

const fieldErrorText = (errors: unknown[]) =>
  [...new Set(errors.map(String))].join(" ");

export const StaffLoginPage = () => {
  const navigate = useNavigate();
  const [challenge, setChallenge] = useState<LoginChallenge | null>(null);
  const [showPassword, setShowPassword] = useState(false);
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
    <main className="relative flex min-h-screen items-center justify-center px-5 py-24 sm:py-20">
      <div className="absolute top-5 right-5 sm:top-7 sm:right-8">
        <ThemeToggle />
      </div>
      <section className="w-full max-w-sm">
        <div className="mb-12 flex justify-center">
          <a aria-label="Lumora home" href="/">
            <span className="bg-primary text-primary-foreground grid size-10 place-items-center rounded-xl">
              <Clapperboard aria-hidden="true" className="size-5" />
            </span>
          </a>
        </div>
        <div className="text-center">
          <p className="text-muted-foreground mb-3 text-xs font-semibold tracking-[0.18em] uppercase">
            Lumora · Staff access
          </p>
          <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">
            {challenge ? "Verify it’s you" : "Sign in to Lumora"}
          </h1>
          <p className="text-muted-foreground mx-auto mt-3 max-w-xs text-sm leading-6">
            {challenge
              ? "Enter the six-digit code from your authenticator app to finish signing in."
              : "Sign in with your staff account to manage Lumora."}
          </p>
        </div>

        {challenge ? (
          <form
            className="mt-8 space-y-5"
            onSubmit={(event) => {
              event.preventDefault();
              event.stopPropagation();
              void mfaForm.handleSubmit();
            }}
          >
            <mfaForm.Field
              name="code"
              validators={{
                onBlur: validateWith(mfaCodeSchema),
                onSubmit: validateWith(mfaCodeSchema),
              }}
            >
              {(field) => (
                <label
                  className="block space-y-2 text-sm font-medium"
                  htmlFor={field.name}
                >
                  Authenticator code
                  <Input
                    autoComplete="one-time-code"
                    aria-describedby={`${field.name}-hint`}
                    aria-invalid={
                      field.state.meta.isTouched &&
                      field.state.meta.errors.length > 0
                    }
                    className="bg-muted h-14 text-center text-xl tracking-[0.45em]"
                    disabled={isSubmitting}
                    inputMode="numeric"
                    maxLength={6}
                    onBlur={field.handleBlur}
                    onChange={(event) =>
                      field.handleChange(
                        event.target.value.replaceAll(/\D/gu, "").slice(0, 6)
                      )
                    }
                    type="text"
                    value={field.state.value}
                  />
                  {field.state.meta.isTouched &&
                  field.state.meta.errors.length > 0 ? (
                    <span
                      className="text-destructive text-xs"
                      id={`${field.name}-hint`}
                    >
                      {fieldErrorText(field.state.meta.errors)}
                    </span>
                  ) : null}
                </label>
              )}
            </mfaForm.Field>
            <Button
              className="h-12 w-full rounded-md px-4 font-semibold"
              disabled={isSubmitting}
              type="submit"
            >
              {isSubmitting ? "Verifying…" : "Verify and sign in"}
            </Button>
            <Button
              className="text-muted-foreground hover:text-foreground w-full py-2 text-sm underline-offset-4 hover:underline"
              variant="ghost"
              disabled={isSubmitting}
              onClick={() => {
                setChallenge(null);
                mfaForm.reset();
              }}
              type="button"
            >
              Use a different account
            </Button>
          </form>
        ) : (
          <form
            className="mt-8 space-y-5"
            onSubmit={(event) => {
              event.preventDefault();
              event.stopPropagation();
              void credentialsForm.handleSubmit();
            }}
          >
            <credentialsForm.Field
              name="email"
              validators={{
                onBlur: validateWith(emailSchema),
                onSubmit: validateWith(emailSchema),
              }}
            >
              {(field) => (
                <label
                  className="block space-y-2 text-sm font-medium"
                  htmlFor={field.name}
                >
                  Email
                  <Input
                    autoComplete="username"
                    className="bg-muted h-12"
                    id={field.name}
                    aria-describedby={`${field.name}-hint`}
                    aria-invalid={
                      field.state.meta.isTouched &&
                      field.state.meta.errors.length > 0
                    }
                    onBlur={field.handleBlur}
                    onChange={(event) => field.handleChange(event.target.value)}
                    inputMode="email"
                    type="text"
                    value={field.state.value}
                  />
                  {field.state.meta.isTouched &&
                  field.state.meta.errors.length > 0 ? (
                    <span
                      className="text-destructive text-xs"
                      id={`${field.name}-hint`}
                    >
                      {fieldErrorText(field.state.meta.errors)}
                    </span>
                  ) : null}
                </label>
              )}
            </credentialsForm.Field>
            <credentialsForm.Field
              name="password"
              validators={{
                onBlur: validateWith(passwordSchema),
                onSubmit: validateWith(passwordSchema),
              }}
            >
              {(field) => (
                <div className="space-y-2 text-sm font-medium">
                  <label htmlFor={field.name}>Password</label>
                  <div className="relative">
                    <Input
                      autoComplete="current-password"
                      className="bg-muted border-border focus-visible:border-ring focus-visible:ring-ring h-12 pr-12 focus-visible:ring-2 focus-visible:ring-offset-0"
                      id={field.name}
                      aria-describedby={`${field.name}-hint`}
                      aria-invalid={
                        field.state.meta.isTouched &&
                        field.state.meta.errors.length > 0
                      }
                      onBlur={field.handleBlur}
                      onChange={(event) =>
                        field.handleChange(event.target.value)
                      }
                      type={showPassword ? "text" : "password"}
                      value={field.state.value}
                    />
                    <Button
                      aria-label={
                        showPassword ? "Hide password" : "Show password"
                      }
                      aria-pressed={showPassword}
                      className="text-muted-foreground hover:text-foreground absolute top-0 right-0 h-12 w-11 rounded-l-none rounded-r-md"
                      variant="ghost"
                      onClick={() => setShowPassword((visible) => !visible)}
                      type="button"
                    >
                      {showPassword ? (
                        <EyeOff aria-hidden="true" className="size-4" />
                      ) : (
                        <Eye aria-hidden="true" className="size-4" />
                      )}
                    </Button>
                  </div>
                  {field.state.meta.isTouched &&
                  field.state.meta.errors.length > 0 ? (
                    <span
                      className="text-destructive text-xs"
                      id={`${field.name}-hint`}
                    >
                      {fieldErrorText(field.state.meta.errors)}
                    </span>
                  ) : null}
                </div>
              )}
            </credentialsForm.Field>
            <Button
              className="h-12 w-full rounded-md px-4 font-semibold"
              disabled={isSubmitting}
              type="submit"
            >
              {isSubmitting ? "Signing in…" : "Continue"}
            </Button>
          </form>
        )}
        <p className="text-muted-foreground mt-14 text-center text-xs">
          Authorized Lumora staff only
        </p>
      </section>
    </main>
  );
};
