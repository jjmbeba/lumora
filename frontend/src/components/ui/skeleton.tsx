import { cn } from "cn";
import type { ComponentProps } from "react";

const Skeleton = ({ className, ...props }: ComponentProps<"div">) => (
  <div
    data-slot="skeleton"
    aria-hidden="true"
    className={cn("bg-muted rounded-md", className)}
    {...props}
  />
);

export { Skeleton };
