import { useSearch } from "@tanstack/react-router";
import { useState } from "react";

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

export const StaffSetupPage = () => {
  const { token } = useSearch({ from: "/staff/setup" });
  const start = useStartStaffEnrollmentMutation();
  const complete = useCompleteStaffEnrollmentMutation();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");

  if (complete.data) {
    return (
      <main className="mx-auto flex min-h-screen max-w-lg items-center px-5 py-12">
        <Card className="w-full rounded-xl border p-6 shadow-none ring-0">
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
        </Card>
      </main>
    );
  }

  return (
    <main className="relative mx-auto flex min-h-screen max-w-lg items-center px-5 py-12">
      <div className="absolute top-5 right-5">
        <ThemeToggle />
      </div>
      <Card className="w-full rounded-xl border p-6 shadow-none ring-0">
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
                complete.mutate({
                  invitation_token: token,
                  challenge: start.data.challenge,
                  password,
                  code,
                });
              }}
            >
              <label className="grid gap-2 text-sm">
                Password
                <Input
                  autoComplete="new-password"
                  minLength={8}
                  type="password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
              </label>
              <label className="grid gap-2 text-sm">
                Six-digit authenticator code
                <Input
                  inputMode="numeric"
                  maxLength={6}
                  value={code}
                  onChange={(event) =>
                    setCode(
                      event.target.value.replaceAll(/\D/gu, "").slice(0, 6)
                    )
                  }
                />
              </label>
              <Button
                disabled={
                  complete.isPending || password.length < 8 || code.length !== 6
                }
              >
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
