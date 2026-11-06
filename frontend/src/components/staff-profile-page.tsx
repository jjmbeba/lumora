import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

import { StaffHeader } from "@/components/staff-header";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useStaffSessionQuery } from "@/features/staff/queries/use-staff-session";
import { ApiError } from "@/lib/api";

export const StaffProfilePage = () => {
  const navigate = useNavigate();
  const sessionQuery = useStaffSessionQuery();

  useEffect(() => {
    if (
      sessionQuery.error instanceof ApiError &&
      sessionQuery.error.status === 401
    ) {
      navigate({ to: "/staff/login" }).catch(() =>
        window.location.assign("/staff/login")
      );
    }
  }, [navigate, sessionQuery.error]);

  if (sessionQuery.isPending) {
    return (
      <main className="mx-auto min-h-screen max-w-6xl px-5 py-10 sm:px-8">
        <div className="border-border flex items-center justify-between border-b pb-6">
          <Skeleton className="h-12 w-48 rounded-md" />
          <Skeleton className="size-10 rounded-full" />
        </div>
      </main>
    );
  }

  if (sessionQuery.isError || !sessionQuery.data) {
    return (
      <main className="mx-auto min-h-screen max-w-6xl px-5 py-10 sm:px-8">
        <Card className="border-border bg-card mt-8 rounded-xl border p-6 shadow-none ring-0">
          <CardHeader className="gap-2 p-0">
            <CardTitle className="font-semibold">Profile unavailable</CardTitle>
            <CardDescription className="text-muted-foreground mt-0 text-sm">
              Your staff profile could not be loaded.
            </CardDescription>
          </CardHeader>
          <Button
            className="mt-5"
            onClick={() => sessionQuery.refetch()}
            variant="outline"
          >
            Try again
          </Button>
        </Card>
      </main>
    );
  }

  const { account } = sessionQuery.data;
  return (
    <main className="mx-auto min-h-screen max-w-6xl px-5 py-10 sm:px-8">
      <StaffHeader
        eyebrow="Lumora · Account"
        title="Profile"
        session={sessionQuery.data}
      />
      <Card className="border-border bg-card mt-8 rounded-xl border p-6 shadow-none ring-0">
        <CardHeader className="gap-2 p-0">
          <CardTitle className="font-semibold">Your staff profile</CardTitle>
          <CardDescription className="text-muted-foreground mt-0 text-sm">
            This page is a placeholder. Profile management will be added later.
          </CardDescription>
        </CardHeader>
        <dl className="mt-6 grid gap-4 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">Name</dt>
            <dd className="mt-1 font-medium">{account.display_name}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Email</dt>
            <dd className="mt-1 font-medium">{account.email}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Role</dt>
            <dd className="mt-1 font-medium">
              {account.role === "MANAGER" ? "Manager" : "Scanner"}
            </dd>
          </div>
        </dl>
      </Card>
    </main>
  );
};
