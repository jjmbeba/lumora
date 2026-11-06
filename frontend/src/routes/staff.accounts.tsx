import { createFileRoute } from "@tanstack/react-router";

const StaffAccountsPage = () => (
  <section className="mx-auto max-w-6xl px-6 py-10 sm:px-10">
    <p className="text-muted-foreground text-sm font-medium tracking-[0.18em] uppercase">
      Lumora · Team
    </p>
    <h1 className="mt-4 text-4xl font-semibold tracking-tight">
      Staff accounts
    </h1>
    <p className="text-muted-foreground mt-3 max-w-xl">
      The manager directory will connect to the staff API in the next
      implementation step.
    </p>
  </section>
);

export const Route = createFileRoute("/staff/accounts")({
  component: StaffAccountsPage,
});
