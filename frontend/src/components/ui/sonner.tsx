import { Toaster as Sonner } from "sonner";
import type { ToasterProps } from "sonner";

export const Toaster = (props: ToasterProps) => (
  <Sonner theme="light" richColors position="top-right" {...props} />
);
