import { useForm } from "@tanstack/react-form";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useDeferredValue, useEffect, useState } from "react";
import * as v from "valibot";

import type { components } from "@/api/generated";
import { StaffHeader } from "@/components/staff-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
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
import {
  useCreateStaffInvitationMutation,
  useDeactivateStaffAccountMutation,
  useResendStaffInvitationMutation,
} from "@/features/staff/mutations/use-staff-management-mutations";
import { useStaffAccountsQuery } from "@/features/staff/queries/use-staff-accounts";
import { useStaffSessionQuery } from "@/features/staff/queries/use-staff-session";
import { ApiError } from "@/lib/api";
import { getErrorMessage } from "@/lib/error-message";

const inviteFormSchema = v.object({
  display_name: v.pipe(v.string(), v.trim(), v.minLength(1, "Enter a name.")),
  email: v.pipe(v.string(), v.trim(), v.email("Enter a valid email address.")),
  role: v.picklist(["MANAGER", "SCANNER"]),
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
  const sessionQuery = useStaffSessionQuery();
  const { data, error } = accountsQuery;

  useEffect(() => {
    if (
      (error instanceof ApiError && error.status === 401) ||
      (sessionQuery.error instanceof ApiError &&
        sessionQuery.error.status === 401)
    ) {
      queryClient.clear();
      void navigate({ to: "/staff/login" });
    }
  }, [error, navigate, queryClient, sessionQuery.error]);

  const [inviteOpen, setInviteOpen] = useState(false);
  const [accountToDeactivate, setAccountToDeactivate] = useState<
    components["schemas"]["StaffAccount"] | null
  >(null);
  const createInvitation = useCreateStaffInvitationMutation();
  const resendInvitation = useResendStaffInvitationMutation();
  const deactivateAccount = useDeactivateStaffAccountMutation();

  const inviteForm = useForm({
    defaultValues: {
      display_name: "",
      email: "",
      role: "SCANNER" as components["schemas"]["StaffIdentity"]["role"],
    },
    onSubmit: async ({ value }) => {
      try {
        await createInvitation.mutateAsync(value);
        inviteForm.reset();
        setInviteOpen(false);
      } catch {
        // Mutation reports API errors through Sonner.
      }
    },
  });

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
                <TableHead className="px-4 py-3 font-medium" scope="col">
                  Actions
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
                  <TableCell className="flex gap-2 px-4 py-3">
                    {account.status === "INVITED" ? (
                      <Button
                        disabled={resendInvitation.isPending}
                        onClick={() => resendInvitation.mutate(account.id)}
                        size="sm"
                        variant="outline"
                      >
                        Resend
                      </Button>
                    ) : null}
                    {sessionQuery.data?.account.id === account.id ? (
                      <span className="text-muted-foreground px-2 text-xs">
                        You
                      </span>
                    ) : account.status !== "DISABLED" && sessionQuery.data ? (
                      <Button
                        disabled={deactivateAccount.isPending}
                        onClick={() => setAccountToDeactivate(account)}
                        size="sm"
                        variant="outline"
                      >
                        Deactivate
                      </Button>
                    ) : null}
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
      {sessionQuery.data ? (
        <StaffHeader
          eyebrow="Lumora · Team"
          title="Staff accounts"
          session={sessionQuery.data}
        />
      ) : (
        <header className="border-border flex items-center justify-between border-b pb-6">
          <div>
            <p className="text-muted-foreground text-xs font-semibold tracking-[0.18em] uppercase">
              Lumora · Team
            </p>
            <h1 className="mt-2 text-3xl font-semibold tracking-tight">
              Staff accounts
            </h1>
          </div>
          <Skeleton className="size-10 rounded-full" />
        </header>
      )}

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
            <div className="flex w-full gap-3 sm:max-w-xl">
              <label className="relative block min-w-0 flex-1">
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
              <Dialog
                open={inviteOpen}
                onOpenChange={(open) => {
                  setInviteOpen(open);
                  if (!open) inviteForm.reset();
                }}
              >
                <DialogTrigger render={<Button variant="outline" />}>
                  Invite staff
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader>
                    <DialogTitle>Invite a staff member</DialogTitle>
                    <DialogDescription>
                      We’ll email them a single-use link to set up their account
                      and authenticator.
                    </DialogDescription>
                  </DialogHeader>
                  <form
                    className="grid gap-4"
                    onSubmit={(event) => {
                      event.preventDefault();
                      event.stopPropagation();
                      void inviteForm.handleSubmit();
                    }}
                  >
                    <inviteForm.Field
                      name="display_name"
                      validators={{
                        onBlur: validateWith(
                          inviteFormSchema.entries.display_name
                        ),
                        onSubmit: validateWith(
                          inviteFormSchema.entries.display_name
                        ),
                      }}
                    >
                      {(field) => (
                        <label
                          className="grid gap-2 text-sm font-medium"
                          htmlFor={field.name}
                        >
                          Name
                          <Input
                            id={field.name}
                            value={field.state.value}
                            onBlur={field.handleBlur}
                            onChange={(event) =>
                              field.handleChange(event.target.value)
                            }
                            aria-invalid={
                              field.state.meta.isTouched &&
                              field.state.meta.errors.length > 0
                            }
                          />
                          {field.state.meta.isTouched &&
                          field.state.meta.errors.length > 0 ? (
                            <span className="text-destructive text-xs">
                              {fieldErrorText(field.state.meta.errors)}
                            </span>
                          ) : null}
                        </label>
                      )}
                    </inviteForm.Field>
                    <inviteForm.Field
                      name="email"
                      validators={{
                        onBlur: validateWith(inviteFormSchema.entries.email),
                        onSubmit: validateWith(inviteFormSchema.entries.email),
                      }}
                    >
                      {(field) => (
                        <label
                          className="grid gap-2 text-sm font-medium"
                          htmlFor={field.name}
                        >
                          Email
                          <Input
                            autoComplete="email"
                            id={field.name}
                            type="email"
                            value={field.state.value}
                            onBlur={field.handleBlur}
                            onChange={(event) =>
                              field.handleChange(event.target.value)
                            }
                            aria-invalid={
                              field.state.meta.isTouched &&
                              field.state.meta.errors.length > 0
                            }
                          />
                          {field.state.meta.isTouched &&
                          field.state.meta.errors.length > 0 ? (
                            <span className="text-destructive text-xs">
                              {fieldErrorText(field.state.meta.errors)}
                            </span>
                          ) : null}
                        </label>
                      )}
                    </inviteForm.Field>
                    <inviteForm.Field name="role">
                      {(field) => (
                        <label className="grid gap-2 text-sm font-medium">
                          Role
                          <Select
                            value={field.state.value}
                            onValueChange={(value) =>
                              field.handleChange(
                                value === "MANAGER" ? "MANAGER" : "SCANNER"
                              )
                            }
                          >
                            <SelectTrigger
                              aria-label="Staff role"
                              className="border-border bg-background text-foreground focus-visible:border-primary focus-visible:ring-primary/20 h-10 w-full focus-visible:ring-2"
                            >
                              <SelectValue>
                                {field.state.value === "MANAGER"
                                  ? "Manager"
                                  : "Scanner"}
                              </SelectValue>
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="SCANNER">Scanner</SelectItem>
                              <SelectItem value="MANAGER">Manager</SelectItem>
                            </SelectContent>
                          </Select>
                        </label>
                      )}
                    </inviteForm.Field>
                    <Button
                      className="h-10"
                      disabled={createInvitation.isPending}
                      type="submit"
                    >
                      {createInvitation.isPending
                        ? "Sending invitation…"
                        : "Send invitation"}
                    </Button>
                  </form>
                </DialogContent>
              </Dialog>
            </div>
          </div>

          {renderDirectoryContent()}
        </>
      )}

      <Dialog
        open={accountToDeactivate !== null}
        onOpenChange={(open) => {
          if (!open) setAccountToDeactivate(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deactivate staff account?</DialogTitle>
            <DialogDescription>
              {accountToDeactivate
                ? `${accountToDeactivate.display_name} will no longer be able to sign in.`
                : "This staff member will no longer be able to sign in."}
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2">
            <Button
              onClick={() => setAccountToDeactivate(null)}
              variant="outline"
            >
              Cancel
            </Button>
            <Button
              disabled={!accountToDeactivate || deactivateAccount.isPending}
              onClick={() => {
                if (!accountToDeactivate) return;
                deactivateAccount.mutate(accountToDeactivate.id, {
                  onSuccess: () => setAccountToDeactivate(null),
                });
              }}
              variant="destructive"
            >
              {deactivateAccount.isPending ? "Deactivating…" : "Deactivate"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </main>
  );
};
