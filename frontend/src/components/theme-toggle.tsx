import { Monitor, Moon, Sun } from "lucide-react";

import type { Theme } from "@/components/theme-context";
import { useTheme } from "@/components/theme-context";

const options: { value: Theme; label: string; Icon: typeof Sun }[] = [
  { value: "light", label: "Light theme", Icon: Sun },
  { value: "dark", label: "Dark theme", Icon: Moon },
  { value: "system", label: "Use system theme", Icon: Monitor },
];

export const ThemeToggle = () => {
  const { theme, setTheme } = useTheme();

  return (
    <fieldset className="border-border bg-card/80 inline-flex items-center rounded-full border p-1 shadow-sm backdrop-blur">
      <legend className="sr-only">Color theme</legend>
      {options.map(({ value, label, Icon }) => (
        <button
          aria-label={label}
          aria-pressed={theme === value}
          className="text-muted-foreground hover:text-foreground focus-visible:ring-ring aria-pressed:bg-primary aria-pressed:text-primary-foreground flex size-9 items-center justify-center rounded-full transition-colors focus-visible:ring-2 focus-visible:outline-none"
          key={value}
          onClick={() => setTheme(value)}
          title={label}
          type="button"
        >
          <Icon aria-hidden="true" className="size-4" />
        </button>
      ))}
    </fieldset>
  );
};
