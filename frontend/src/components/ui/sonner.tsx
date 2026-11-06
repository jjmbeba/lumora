import { Toaster as Sonner } from "sonner";
import type { ToasterProps } from "sonner";

import { useTheme } from "@/components/theme-context";

export const Toaster = (props: ToasterProps) => {
  const { resolvedTheme } = useTheme();
  return (
    <Sonner theme={resolvedTheme} richColors position="top-right" {...props} />
  );
};
