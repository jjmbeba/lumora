import { Menu as MenuPrimitive } from "@base-ui/react/menu";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

const DropdownMenu = MenuPrimitive.Root;
const DropdownMenuTrigger = MenuPrimitive.Trigger;

const DropdownMenuContent = ({
  className,
  sideOffset = 8,
  align = "end",
  ...props
}: MenuPrimitive.Popup.Props &
  Pick<MenuPrimitive.Positioner.Props, "align" | "sideOffset">) => (
  <MenuPrimitive.Portal>
    <MenuPrimitive.Positioner
      align={align}
      side="bottom"
      sideOffset={sideOffset}
      className="z-50 outline-none"
    >
      <MenuPrimitive.Popup
        className={cn(
          "border-border bg-popover text-popover-foreground min-w-60 rounded-xl border p-1.5 shadow-lg outline-none",
          className
        )}
        {...props}
      />
    </MenuPrimitive.Positioner>
  </MenuPrimitive.Portal>
);

const DropdownMenuLabel = ({ className, ...props }: ComponentProps<"div">) => (
  <div className={cn("px-2 py-1.5", className)} {...props} />
);

const DropdownMenuItem = ({
  className,
  ...props
}: MenuPrimitive.Item.Props) => (
  <MenuPrimitive.Item
    className={cn(
      "focus:bg-accent focus:text-accent-foreground data-highlighted:bg-accent data-highlighted:text-accent-foreground flex min-h-9 items-center gap-2 rounded-lg px-2 text-sm outline-none select-none data-disabled:pointer-events-none data-disabled:opacity-50 cursor-pointer",
      className
    )}
    {...props}
  />
);

const DropdownMenuSeparator = ({
  className,
  ...props
}: MenuPrimitive.Separator.Props) => (
  <MenuPrimitive.Separator
    className={cn("bg-border -mx-1 my-1 h-px", className)}
    {...props}
  />
);

export {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
};
