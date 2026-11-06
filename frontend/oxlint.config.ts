import { defineConfig } from "oxlint";
import core from "ultracite/oxlint/core";
import react from "ultracite/oxlint/react";
import tanstack from "ultracite/oxlint/tanstack";

export default defineConfig({
  env: core.env,
  extends: [core, react, tanstack],
  ignorePatterns: [...core.ignorePatterns, "src/api/generated.ts"],
  plugins: ["react", "typescript", "oxc"],
  rules: {
    "react/rules-of-hooks": "error",
    "react/only-export-components": [
      "warn",
      {
        allowConstantExport: true,
        allowExportNames: ["Route"],
      },
    ],
  },
});
