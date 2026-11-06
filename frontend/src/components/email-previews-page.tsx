import { Link } from "@tanstack/react-router";
import {
  ArrowLeft,
  Clapperboard,
  Clock3,
  MapPin,
  QrCode,
  TicketCheck,
} from "lucide-react";
import type { ReactNode } from "react";

import { ThemeToggle } from "@/components/theme-toggle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";

interface EmailFrameProps {
  category: string;
  from: string;
  subject: string;
  children: ReactNode;
}

const EmailFrame = ({ category, from, subject, children }: EmailFrameProps) => (
  <Card className="border-border overflow-hidden rounded-xl border p-0 shadow-none ring-0">
    <div className="bg-muted/70 border-border grid gap-2 border-b px-5 py-4 text-sm sm:grid-cols-[5rem_1fr]">
      <span className="text-muted-foreground">From</span>
      <span className="font-medium">{from}</span>
      <span className="text-muted-foreground">Subject</span>
      <span className="font-medium">{subject}</span>
    </div>
    <div className="p-5 sm:p-8">
      <Badge className="mb-5" variant="secondary">
        {category}
      </Badge>
      {children}
    </div>
  </Card>
);

const EmailBrand = () => (
  <div className="mb-7 flex items-center gap-2.5">
    <span className="bg-primary text-primary-foreground grid size-9 place-items-center rounded-lg">
      <Clapperboard aria-hidden="true" className="size-4" />
    </span>
    <span className="text-base font-semibold tracking-tight">Lumora</span>
  </div>
);

export const EmailPreviewsPage = () => (
  <main className="mx-auto flex min-h-screen max-w-5xl flex-col px-5 py-8 sm:px-8 sm:py-10">
    <header className="border-border mb-9 flex items-center justify-between border-b pb-5">
      <Link
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-2 text-sm font-medium"
        to="/"
      >
        <ArrowLeft aria-hidden="true" className="size-4" />
        Lumora home
      </Link>
      <ThemeToggle />
    </header>

    <div className="mx-auto mb-8 w-full max-w-2xl">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <p className="text-muted-foreground text-xs font-semibold tracking-[0.18em] uppercase">
          Transactional email
        </p>
        <Badge variant="outline">Prototype</Badge>
      </div>
      <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">
        Email previews
      </h1>
      <p className="text-muted-foreground mt-3 text-sm leading-6 sm:text-base">
        A first look at how Lumora’s staff and booking emails could feel: clear,
        calm, and easy to scan.
      </p>
      <p className="text-muted-foreground mt-3 text-xs leading-5">
        Examples use fictional details. Ticket screening information and QR
        placeholders are illustrative; this page does not send messages or
        contain usable admission credentials.
      </p>
    </div>

    <div className="mx-auto grid w-full max-w-2xl gap-6 pb-10">
      <EmailFrame
        category="Staff invitation"
        from="Lumora Staff <staff@lumora.example>"
        subject="You’re invited to join Lumora"
      >
        <EmailBrand />
        <h2 className="text-xl font-semibold tracking-tight">
          Welcome to the team, Amina
        </h2>
        <p className="text-muted-foreground mt-3 text-sm leading-6">
          You’ve been invited to join the Lumora cinema team as a Scanner. Set
          up your password and authenticator app to activate your account.
        </p>
        <Button className="mt-6" type="button">
          Set up your account
        </Button>
        <p className="text-muted-foreground mt-5 text-xs leading-5">
          This single-use link expires on November 10, 2026. If you weren’t
          expecting this invitation, you can ignore this email.
        </p>
      </EmailFrame>

      <EmailFrame
        category="Booking verification"
        from="Lumora Bookings <bookings@lumora.example>"
        subject="Your booking verification code"
      >
        <EmailBrand />
        <h2 className="text-xl font-semibold tracking-tight">
          Confirm it’s you
        </h2>
        <p className="text-muted-foreground mt-3 text-sm leading-6">
          Enter this code to view your booking. It expires in 10 minutes.
        </p>
        <div className="bg-muted text-foreground my-6 rounded-lg px-5 py-4 text-center font-mono text-3xl font-semibold tracking-[0.22em]">
          481 206
        </div>
        <p className="text-muted-foreground text-xs leading-5">
          If you didn’t request a code, you can ignore this email. Your booking
          remains protected.
        </p>
      </EmailFrame>

      <EmailFrame
        category="Tickets confirmed"
        from="Lumora Bookings <bookings@lumora.example>"
        subject="Your tickets for The Last Lantern"
      >
        <EmailBrand />
        <div className="mb-5 flex items-start justify-between gap-4">
          <div>
            <p className="text-muted-foreground text-sm">Booking confirmed</p>
            <h2 className="mt-1 text-xl font-semibold tracking-tight">
              The Last Lantern
            </h2>
          </div>
          <TicketCheck
            aria-hidden="true"
            className="text-primary mt-1 size-6"
          />
        </div>
        <div className="border-border grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
          <p className="flex items-center gap-2">
            <Clock3
              aria-hidden="true"
              className="text-muted-foreground size-4"
            />
            Friday, November 14 · 7:30 PM
          </p>
          <p className="flex items-center gap-2">
            <MapPin
              aria-hidden="true"
              className="text-muted-foreground size-4"
            />
            Lumora Cinema · Screen 2
          </p>
          <p className="text-muted-foreground sm:col-span-2">
            Booking reference{" "}
            <span className="text-foreground font-mono">LMR-Q7X2</span>
          </p>
        </div>
        <div className="mt-5 grid gap-3 sm:grid-cols-2">
          {["D4", "D5"].map((seat) => (
            <div
              className="bg-muted/60 flex items-center gap-3 rounded-lg p-3"
              key={seat}
            >
              <span className="border-border bg-card grid size-16 shrink-0 place-items-center rounded-md border">
                <QrCode
                  aria-hidden="true"
                  className="text-muted-foreground size-9"
                />
              </span>
              <span>
                <span className="text-muted-foreground block text-xs">
                  Seat
                </span>
                <span className="font-semibold">{seat}</span>
              </span>
            </div>
          ))}
        </div>
        <p className="text-muted-foreground mt-5 text-xs leading-5">
          Keep each ticket’s QR code ready to be scanned at the cinema. Each
          guest can use the QR code for their own seat.
        </p>
      </EmailFrame>

      <EmailFrame
        category="Group booking · 10 tickets"
        from="Lumora Bookings <bookings@lumora.example>"
        subject="10 tickets for The Last Lantern"
      >
        <EmailBrand />
        <div className="mb-5 flex items-start justify-between gap-4">
          <div>
            <p className="text-muted-foreground text-sm">Booking confirmed</p>
            <h2 className="mt-1 text-xl font-semibold tracking-tight">
              The Last Lantern
            </h2>
          </div>
          <Badge variant="outline">10 tickets</Badge>
        </div>
        <div className="border-border grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
          <p className="flex items-center gap-2">
            <Clock3
              aria-hidden="true"
              className="text-muted-foreground size-4"
            />
            Friday, November 14 · 7:30 PM
          </p>
          <p className="flex items-center gap-2">
            <MapPin
              aria-hidden="true"
              className="text-muted-foreground size-4"
            />
            Lumora Cinema · Screen 2
          </p>
          <p className="text-muted-foreground sm:col-span-2">
            Booking reference{" "}
            <span className="text-foreground font-mono">LMR-10SEATS</span>
          </p>
        </div>
        <h3 className="mt-6 mb-2 text-sm font-semibold">Your tickets</h3>
        <div className="bg-muted/50 flex flex-wrap gap-2 rounded-lg p-4">
          {Array.from({ length: 10 }, (_, index) => (
            <Badge
              className="font-mono"
              key={`A${index + 1}`}
              variant="outline"
            >
              A{index + 1}
            </Badge>
          ))}
        </div>
        <p className="text-muted-foreground mt-4 text-sm leading-6">
          Each seat has its own ticket and QR code. Open the ticket list to
          share individual passes with your guests.
        </p>
        <Button className="mt-5" type="button">
          <QrCode aria-hidden="true" className="size-4" />
          View all 10 tickets
        </Button>
      </EmailFrame>
    </div>

    <footer className="border-border text-muted-foreground mt-auto border-t pt-5 text-center text-xs">
      Visual prototype only · Lumora transactional email concepts
    </footer>
  </main>
);
