import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Clapperboard } from "lucide-react";

import { ThemeToggle } from "@/components/theme-toggle";

export const HomePage = () => (
  <div className="mx-auto flex min-h-screen max-w-6xl flex-col px-6 py-8 sm:px-10">
    <header className="border-border flex items-center justify-between gap-3 border-b pb-6">
      <div className="flex items-center gap-3">
        <span className="bg-primary text-primary-foreground grid size-10 place-items-center rounded-xl">
          <Clapperboard aria-hidden="true" className="size-5" />
        </span>
        <span className="text-lg font-semibold tracking-tight">Lumora</span>
      </div>
      <ThemeToggle />
    </header>
    <section className="grid flex-1 content-center gap-8 py-16 lg:grid-cols-[1.2fr_0.8fr] lg:items-center">
      <div className="max-w-2xl">
        <p className="text-muted-foreground mb-4 text-sm font-medium tracking-[0.18em] uppercase">
          Cinema operations
        </p>
        <h1 className="max-w-xl text-5xl leading-[1.04] font-semibold tracking-tighter sm:text-7xl">
          Every screening, in sync.
        </h1>
        <p className="text-muted-foreground mt-6 max-w-lg text-lg leading-8">
          Manage bookings, payments, and the people who keep each show moving.
        </p>
        <Link
          to="/staff/login"
          className="bg-primary text-primary-foreground focus-visible:ring-ring mt-9 inline-flex items-center gap-2 rounded-lg px-5 py-3 font-medium transition-opacity hover:opacity-90 focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none"
        >
          Staff sign in <ArrowUpRight aria-hidden="true" className="size-4" />
        </Link>
      </div>
      <div
        aria-hidden="true"
        className="relative hidden min-h-80 items-center justify-center lg:flex"
      >
        <div className="border-border absolute size-72 rounded-full border" />
        <div className="border-border absolute size-56 rounded-full border" />
        <div className="bg-primary/10 absolute size-40 rounded-full" />
        <div className="bg-primary text-primary-foreground shadow-primary/20 relative grid size-20 place-items-center rounded-2xl shadow-xl">
          <Clapperboard className="size-9" />
        </div>
      </div>
    </section>
    <footer className="border-border text-muted-foreground flex flex-wrap items-center justify-between gap-3 border-t pt-5 text-sm">
      <span>Booking and venue tools are coming online next.</span>
      <Link
        className="hover:text-foreground underline-offset-4 hover:underline"
        to="/email-previews"
      >
        Email previews
      </Link>
    </footer>
  </div>
);
