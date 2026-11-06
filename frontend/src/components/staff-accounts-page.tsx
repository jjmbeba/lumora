import { useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { LogOut, Search } from "lucide-react";
import { useDeferredValue, useEffect } from "react";

import { useLogoutStaffMutation } from "@/features/staff/mutations/use-staff-auth-mutations";
import { useStaffAccountsQuery } from "@/features/staff/queries/use-staff-accounts";
import { ApiError } from "@/lib/api";
import { getErrorMessage } from "@/lib/error-message";

const dateFormatter = new Intl.DateTimeFormat("en-KE", {
  dateStyle: "medium",
});

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
        <output className="text-muted-foreground mt-8 text-sm">
          Loading staff accounts…
        </output>
      );
    }

    if (accountsQuery.isError) {
      return (
        <div
          className="border-destructive/30 bg-destructive/5 text-destructive mt-8 rounded-lg border p-4 text-sm"
          role="alert"
        >
          {getErrorMessage(
            accountsQuery.error,
            "The staff directory could not be loaded."
          )}
          <button
            className="ml-3 font-semibold underline"
            onClick={async () => {
              await accountsQuery.refetch();
            }}
            type="button"
          >
            Try again
          </button>
        </div>
      );
    }

    if (!data) {
      return null;
    }

    return (
      <>
        <div className="border-border mt-6 overflow-x-auto rounded-xl border">
          <table className="w-full min-w-155 border-collapse text-left text-sm">
            <thead className="bg-muted/60 text-muted-foreground">
              <tr>
                <th className="px-4 py-3 font-medium" scope="col">
                  Name
                </th>
                <th className="px-4 py-3 font-medium" scope="col">
                  Email
                </th>
                <th className="px-4 py-3 font-medium" scope="col">
                  Role
                </th>
                <th className="px-4 py-3 font-medium" scope="col">
                  Status
                </th>
                <th className="px-4 py-3 font-medium" scope="col">
                  Created
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {data.accounts.map((account) => (
                <tr key={account.id}>
                  <td className="px-4 py-3 font-medium">
                    {account.display_name}
                  </td>
                  <td className="text-muted-foreground px-4 py-3">
                    {account.email}
                  </td>
                  <td className="px-4 py-3">{account.role}</td>
                  <td className="px-4 py-3">{account.status}</td>
                  <td className="text-muted-foreground px-4 py-3">
                    {dateFormatter.format(new Date(account.created_at))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
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
              <select
                className="border-border bg-background text-foreground h-9 rounded-lg border px-2"
                onChange={(event) =>
                  navigate({
                    search: (previous) => ({
                      ...previous,
                      page: 1,
                      page_size: Number(event.target.value),
                    }),
                  })
                }
                value={searchState.page_size}
              >
                {[10, 20, 50, 100].map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <div className="flex gap-2">
            <button
              className="border-border hover:bg-muted h-9 rounded-lg border px-3 text-sm disabled:cursor-not-allowed disabled:opacity-50"
              disabled={searchState.page <= 1 || accountsQuery.isFetching}
              onClick={() =>
                navigate({
                  search: (previous) => ({
                    ...previous,
                    page: searchState.page - 1,
                  }),
                })
              }
              type="button"
            >
              Previous
            </button>
            <button
              className="border-border hover:bg-muted h-9 rounded-lg border px-3 text-sm disabled:cursor-not-allowed disabled:opacity-50"
              disabled={!data.has_more || accountsQuery.isFetching}
              onClick={() =>
                navigate({
                  search: (previous) => ({
                    ...previous,
                    page: searchState.page + 1,
                  }),
                })
              }
              type="button"
            >
              Next
            </button>
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
        <button
          className="border-border hover:bg-muted inline-flex h-10 items-center gap-2 rounded-lg border px-4 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-60"
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
          type="button"
        >
          <LogOut aria-hidden="true" className="size-4" />
          {logoutMutation.isPending ? "Signing out…" : "Sign out"}
        </button>
      </header>

      {isForbidden ? (
        <section className="border-border mt-8 rounded-xl border p-6">
          <h2 className="font-semibold">Manager access required</h2>
          <p className="text-muted-foreground mt-2 text-sm">
            You are signed in, but your staff role cannot view this directory.
          </p>
        </section>
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
              <input
                className="border-border bg-background focus-visible:ring-ring h-10 w-full rounded-lg border pr-3 pl-9 text-sm outline-none focus-visible:ring-2"
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
