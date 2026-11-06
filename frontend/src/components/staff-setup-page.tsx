import { useForm } from "@tanstack/react-form";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Eye, EyeOff } from "lucide-react";
import { useState } from "react";
import * as v from "valibot";
import { ThemeToggle } from "@/components/theme-toggle";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  useCompleteStaffEnrollmentMutation,
  useStartStaffEnrollmentMutation,
} from "@/features/staff/mutations/use-staff-management-mutations";

const setupFormSchema = v.object({
  password: v.pipe(
    v.string(),
    v.minLength(8, "Use a password with at least 8 characters.")
  ),
  code: v.pipe(
    v.string(),
    v.regex(/^\d{6}$/u, "Enter the six-digit authenticator code.")
  ),
});

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

export const StaffSetupPage = () => {
  const navigate = useNavigate();
  const { token } = useSearch({ from: "/staff/setup" });
  const start = useStartStaffEnrollmentMutation();
  const complete = useCompleteStaffEnrollmentMutation();
  const [showPassword, setShowPassword] = useState(false);

  const setupForm = useForm({
    defaultValues: { password: "", code: "" },
    onSubmit: async ({ value }) => {
      if (!start.data) {
        return;
      }

      try {
        await complete.mutateAsync({
          invitation_token: token,
          challenge: start.data.challenge,
          password: value.password,
          code: value.code,
        });
      } catch {
        // The mutation reports request failures through Sonner.
      }
    },
  });

  if (complete.data) {
    return (
      <main className="mx-auto flex min-h-screen max-w-xl items-center px-5 py-12">
        <Card className="border-border w-full rounded-xl border p-8 shadow-none ring-0">
          <CardHeader className="p-0">
            <CardTitle>Save your recovery codes</CardTitle>
            <CardDescription>
              These are shown once. Store them somewhere safe before closing
              this page.
            </CardDescription>
          </CardHeader>
          <ul className="grid grid-cols-2 gap-2 font-mono text-sm">
            {complete.data.recovery_codes.map((recoveryCode) => (
              <li className="bg-muted rounded-md p-2" key={recoveryCode}>
                {recoveryCode}
              </li>
            ))}
          </ul>
          <Link to="/staff/login">
            Continue to staff sign in
          </Link>
        </Card>
      </main>
    );
  }

  return (
    <main className="relative mx-auto flex min-h-screen max-w-xl items-center px-5 py-12">
      <div className="absolute top-5 right-5">
        <ThemeToggle />
      </div>
      <Card className="border-border w-full rounded-xl border p-8 shadow-none ring-0">
        <CardHeader className="p-0">
          <CardTitle>Set up your Lumora staff account</CardTitle>
          <CardDescription>
            Create your password and enroll an authenticator app.
          </CardDescription>
        </CardHeader>
        {start.data ? (
          <>
            <p className="text-sm">
              Add this key to your authenticator app:{" "}
              <code className="font-mono font-semibold">
                {start.data.totp_secret}
              </code>
            </p>
            <form
              className="grid gap-4"
              onSubmit={(event) => {
                event.preventDefault();
                event.stopPropagation();
                void setupForm.handleSubmit();
              }}
            >
              <setupForm.Field
                name="password"
                validators={{
                  onBlur: validateWith(setupFormSchema.entries.password),
                  onSubmit: validateWith(setupFormSchema.entries.password),
                }}
              >
                {(field) => (
                  <div className="space-y-2 text-sm font-medium">
                    <label htmlFor={field.name}>Password</label>
                    <div className="relative">
                      <Input
                        autoComplete="new-password"
                        className="bg-muted border-border focus-visible:border-ring focus-visible:ring-ring h-12 pr-12 focus-visible:ring-2 focus-visible:ring-offset-0"
                        id={field.name}
                        aria-describedby={`${field.name}-hint`}
                        aria-invalid={field.state.meta.errors.length > 0}
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
                    {field.state.meta.errors.length > 0 ? (
                      <span
                        className="text-destructive text-xs"
                        id={`${field.name}-hint`}
                      >
                        {fieldErrorText(field.state.meta.errors)}
                      </span>
                    ) : null}
                  </div>
                )}
              </setupForm.Field>
              <setupForm.Field
                name="code"
                validators={{
                  onBlur: validateWith(setupFormSchema.entries.code),
                  onSubmit: validateWith(setupFormSchema.entries.code),
                }}
              >
                {(field) => (
                  <label
                    className="grid gap-2 text-sm font-medium"
                    htmlFor={field.name}
                  >
                    Six-digit authenticator code
                    <Input
                      id={field.name}
                      inputMode="numeric"
                      maxLength={6}
                      aria-describedby={`${field.name}-hint`}
                      aria-invalid={field.state.meta.errors.length > 0}
                      value={field.state.value}
                      onBlur={field.handleBlur}
                      onChange={(event) =>
                        field.handleChange(
                          event.target.value.replaceAll(/\D/gu, "").slice(0, 6)
                        )
                      }
                    />
                    {field.state.meta.errors.length > 0 ? (
                      <span
                        className="text-destructive text-xs"
                        id={`${field.name}-hint`}
                      >
                        {fieldErrorText(field.state.meta.errors)}
                      </span>
                    ) : null}
                  </label>
                )}
              </setupForm.Field>
              <Button disabled={complete.isPending} type="submit">
                {complete.isPending ? "Setting up…" : "Complete setup"}
              </Button>
            </form>
          </>
        ) : (
          <Button
            disabled={!token || start.isPending}
            onClick={() => start.mutate(token)}
          >
            {start.isPending ? "Checking invitation…" : "Begin account setup"}
          </Button>
        )}
      </Card>
    </main>
  );
};
