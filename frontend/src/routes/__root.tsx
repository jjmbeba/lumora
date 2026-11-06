import { createRootRoute, Outlet } from "@tanstack/react-router";

export const Route = createRootRoute({
  component: () => (
    <main className="bg-background text-foreground min-h-screen">
      <Outlet />
    </main>
  ),
});
