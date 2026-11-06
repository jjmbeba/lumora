import { createFileRoute } from "@tanstack/react-router";

import { EmailPreviewsPage } from "@/components/email-previews-page";

export const Route = createFileRoute("/email-previews")({
  component: EmailPreviewsPage,
});
