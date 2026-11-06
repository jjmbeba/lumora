import { Button as ButtonPrimitive } from "@base-ui/react/button";
import type { VariantProps } from "class-variance-authority";
import { cn } from "cn";

import { buttonVariants } from "./button-variants";

const Button = ({
  className,
  variant = "default",
  size = "default",
  ...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) => (
  <ButtonPrimitive
    data-slot="button"
    className={cn(buttonVariants({ variant, size, className }))}
    {...props}
  />
);

export { Button };
