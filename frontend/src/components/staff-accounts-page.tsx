import { useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { LogOut, Search } from "lucide-react";
import { useDeferredValue, useEffect } from "react";

import { ThemeToggle } from "@/components/theme-toggle";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useLogoutStaffMutation } from "@/features/staff/mutations/use-staff-auth-mutations";
import { useStaffAccountsQuery } from "@/features/staff/queries/use-staff-accounts";
import { ApiError } from "@/lib/api";
import { getErrorMessage } from "@/lib/error-message";

const dateFormatter = new Intl.DateTimeFormat("en-KE", {
  dateStyle: "medium",
});

const statusBadgeClassName = {
  ACTIVE: "border-primary/30 bg-primary/5 text-primary",
  DISABLED: "border-destructive/30 bg-destructive/5 text-destructive",
  INVITED: "border-border bg-muted text-muted-foreground",
} as const;

export const StaffAccountsPage = () => {
  const navigate = useNavigate({ from: "/staff/accounts" });
  const searchState = useSearch({ from: "/staff/accounts" });
  const queryClient = useQueryClient();
  const deferredSearch = useDeferredValue(searchState.search);
  const accountsQuery = useStaffAccountsQuery({
    ...searchState,
    search: deferredSearch,
  });
  const { data, error } = accountsQuery;

  useEffect(() => {
    if (error instanceof ApiError && error.status === 401) {
      queryClient.clear();
      navigate({ to: "/staff/login" });
    }
  }, [error, navigate, queryClient]);

  const logoutMutation = useLogoutStaffMutation();

  const isForbidden = error instanceof ApiError && error.status === 403;

  const renderDirectoryContent = () => {
    if (accountsQuery.isPending) {
      return (
        <div
          aria-busy="true"
          aria-label="Loading staff accounts"
          className="border-border mt-6 overflow-x-auto rounded-xl border"
          role="status"
        >
          <span className="sr-only">Loading staff accounts</span>
          <Table aria-hidden="true" className="min-w-155 border-collapse">
            <TableHeader className="bg-muted/60">
              <TableRow>
                {["Name", "Email", "Role", "Status", "Created"].map(
                  (heading) => (
                    <TableHead className="px-4 py-3" key={heading}>
                      <Skeleton className="h-4 w-16 animate-none rounded-sm" />
                    </TableHead>
                  )
                )}
              </TableRow>
            </TableHeader>
            <TableBody className="divide-border divide-y">
              {Array.from({ length: 4 }, (_, row) => (
                <TableRow key={row}>
                  {["w-32", "w-48", "w-20", "w-20", "w-28"].map(
                    (width, column) => (
                      <TableCell className="px-4 py-3" key={column}>
                        <Skeleton
                          className={`h-4 animate-none ${width} max-w-full rounded-sm`}
                        />
                      </TableCell>
                    )
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      );
    }

    if (accountsQuery.isError) {
      return (
        <Alert className="border-destructive/30 bg-destructive/5 text-destructive mt-8 rounded-lg p-4">
          <AlertTitle className="sr-only">Staff directory error</AlertTitle>
          <AlertDescription className="text-destructive flex flex-wrap items-center gap-3">
            {getErrorMessage(
              accountsQuery.error,
              "The staff directory could not be loaded."
            )}
            <Button
              className="h-auto p-0 font-semibold underline underline-offset-2"
              variant="link"
              size="default"
              onClick={async () => {
                await accountsQuery.refetch();
              }}
              type="button"
            >
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      );
    }

    if (!data) {
      return null;
    }

    return (
      <>
        <div className="border-border mt-6 overflow-x-auto rounded-xl border">
          <Table className="min-w-155 border-collapse text-left text-sm">
            <TableHeader className="bg-muted/60 text-muted-foreground">
              <TableRow>
                <TableHead className="px-4 py-3 font-medium" scope="col">
                  Name
                </TableHead>
                <TableHead className="px-4 py-3 font-medium" scope="col">
                  Email
                </TableHead>
                <TableHead className="px-4 py-3 font-medium" scope="col">
                  Role
                </TableHead>
                <TableHead className="px-4 py-3 font-medium" scope="col">
                  Status
                </TableHead>
                <TableHead className="px-4 py-3 font-medium" scope="col">
                  Created
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody className="divide-border divide-y">
              {data.accounts.map((account) => (
                <TableRow key={account.id}>
                  <TableCell className="px-4 py-3 font-medium">
                    {account.display_name}
                  </TableCell>
                  <TableCell className="text-muted-foreground px-4 py-3">
                    {account.email}
                  </TableCell>
                  <TableCell className="px-4 py-3">{account.role}</TableCell>
                  <TableCell className="px-4 py-3">
                    <Badge
                      className={statusBadgeClassName[account.status]}
                      variant="outline"
                    >
                      {account.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground px-4 py-3">
                    {dateFormatter.format(new Date(account.created_at))}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {data.accounts.length === 0 ? (
            <p className="text-muted-foreground px-4 py-10 text-center text-sm">
              {searchState.search
                ? "No accounts match that search."
                : "No staff accounts yet."}
            </p>
          ) : null}
        </div>

        <nav
          aria-label="Staff account pages"
          className="mt-4 flex items-center justify-between"
        >
          <div className="flex items-center gap-4">
            <p className="text-muted-foreground text-sm">
              Page {searchState.page}
            </p>
            <label className="text-muted-foreground flex items-center gap-2 text-sm">
              Rows
              <Select
                onValueChange={(value) =>
                  navigate({
                    search: (previous) => ({
                      ...previous,
                      page: 1,
                      page_size: Number(value),
                    }),
                  })
                }
                value={String(searchState.page_size)}
              >
                <SelectTrigger
                  aria-label="Rows per page"
                  className="border-border bg-background h-9 rounded-lg px-2"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {[10, 20, 50, 100].map((size) => (
                    <SelectItem key={size} value={String(size)}>
                      {size}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
          </div>
          <div className="flex gap-2">
            <Button
              className="h-9 rounded-lg px-3 text-sm"
              disabled={searchState.page <= 1 || accountsQuery.isFetching}
              onClick={() =>
                navigate({
                  search: (previous) => ({
                    ...previous,
                    page: searchState.page - 1,
                  }),
                })
              }
              variant="outline"
            >
              Previous
            </Button>
            <Button
              className="h-9 rounded-lg px-3 text-sm"
              disabled={!data.has_more || accountsQuery.isFetching}
              onClick={() =>
                navigate({
                  search: (previous) => ({
                    ...previous,
                    page: searchState.page + 1,
                  }),
                })
              }
              variant="outline"
            >
              Next
            </Button>
          </div>
        </nav>
      </>
    );
  };

  return (
    <main className="mx-auto min-h-screen max-w-6xl px-5 py-10 sm:px-8">
      <header className="border-border flex flex-wrap items-center justify-between gap-4 border-b pb-6">
        <div>
          <p className="text-muted-foreground text-xs font-semibold tracking-[0.18em] uppercase">
            Lumora · Team
          </p>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight">
            Staff accounts
          </h1>
        </div>
        <div className="flex items-center gap-3">
          <ThemeToggle />
          <Button
            className="h-10 rounded-lg px-4 text-sm font-medium"
            disabled={logoutMutation.isPending}
            onClick={async () => {
              try {
                await logoutMutation.mutateAsync();
                queryClient.clear();
                await navigate({ to: "/staff/login" });
              } catch {
                // The mutation reports request failures through Sonner.
              }
            }}
            variant="outline"
          >
            <LogOut aria-hidden="true" className="size-4" />
            {logoutMutation.isPending ? "Signing out…" : "Sign out"}
          </Button>
        </div>
      </header>

      {isForbidden ? (
        <Card className="border-border bg-card mt-8 rounded-xl border p-6 shadow-none ring-0">
          <CardHeader className="gap-2 p-0">
            <CardTitle className="font-semibold">
              Manager access required
            </CardTitle>
            <CardDescription className="text-muted-foreground mt-0 text-sm">
              You are signed in, but your staff role cannot view this directory.
            </CardDescription>
          </CardHeader>
        </Card>
      ) : (
        <>
          <div className="mt-8 flex flex-col justify-between gap-4 sm:flex-row sm:items-end">
            <div>
              <h2 className="text-xl font-semibold">Team directory</h2>
              <p className="text-muted-foreground mt-1 text-sm">
                Find staff accounts by name or email.
              </p>
            </div>
            <label className="relative block w-full sm:max-w-sm">
              <span className="sr-only">Search staff accounts</span>
              <Search
                aria-hidden="true"
                className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2"
              />
              <Input
                className="bg-background h-10 rounded-lg pr-3 pl-9 text-sm"
                onChange={(event) =>
                  navigate({
                    search: (previous) => ({
                      ...previous,
                      search: event.target.value.slice(0, 100),
                      page: 1,
                    }),
                    replace: true,
                  })
                }
                placeholder="Search name or email"
                type="search"
                value={searchState.search}
              />
            </label>
          </div>

          {renderDirectoryContent()}
        </>
      )}
    </main>
  );
};
