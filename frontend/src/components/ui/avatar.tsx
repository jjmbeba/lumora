import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

const Avatar = ({ className, ...props }: ComponentProps<"span">) => (
  <span
    className={cn(
      "bg-primary/10 text-primary inline-flex size-8 shrink-0 items-center justify-center overflow-hidden rounded-full font-semibold",
      className
    )}
    {...props}
  />
);

const AvatarFallback = ({ className, ...props }: ComponentProps<"span">) => (
  <span className={cn("leading-none", className)} {...props} />
);

export { Avatar, AvatarFallback };
