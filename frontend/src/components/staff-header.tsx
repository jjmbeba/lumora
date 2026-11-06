import { useNavigate } from "@tanstack/react-router";
import { LogOut, UserRound } from "lucide-react";

import { ThemeToggle } from "@/components/theme-toggle";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useLogoutStaffMutation } from "@/features/staff/mutations/use-staff-auth-mutations";
import type { StaffSession } from "@/features/staff/queries/use-staff-session";

interface StaffHeaderProps {
  eyebrow: string;
  title: string;
  session: StaffSession;
}

const initials = (name: string) =>
  name
    .trim()
    .split(/\s+/u)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");

export const StaffHeader = ({ eyebrow, title, session }: StaffHeaderProps) => {
  const navigate = useNavigate();
  const logoutMutation = useLogoutStaffMutation();
  const account = session.account;

  const signOut = async () => {
    try {
      await logoutMutation.mutateAsync();
      await navigate({ to: "/staff/login" });
    } catch {
      // The mutation reports request failures through Sonner.
    }
  };

  return (
    <header className="border-border flex flex-wrap items-center justify-between gap-4 border-b pb-6">
      <div>
        <p className="text-muted-foreground text-xs font-semibold tracking-[0.18em] uppercase">
          {eyebrow}
        </p>
        <h1 className="mt-2 text-3xl font-semibold tracking-tight">{title}</h1>
      </div>
      <div className="flex items-center gap-3">
        <ThemeToggle />
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label={`Open account menu for ${account.display_name}`}
            render={
              <Button className="size-10 rounded-full p-0" variant="outline" />
            }
          >
            <Avatar aria-hidden="true" className="size-8 text-xs">
              <AvatarFallback>{initials(account.display_name)}</AvatarFallback>
            </Avatar>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuLabel>
              <p className="truncate text-sm font-semibold">
                {account.display_name}
              </p>
              <p className="text-muted-foreground mt-0.5 truncate text-xs">
                {account.email}
              </p>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => void navigate({ to: "/staff/profile" })}
            >
              <UserRound aria-hidden="true" className="size-4" />
              Profile
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={logoutMutation.isPending}
              onClick={() => void signOut()}
              className="text-destructive"
            >
              <LogOut aria-hidden="true" className="size-4" />
              {logoutMutation.isPending ? "Signing out…" : "Log out"}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
};
