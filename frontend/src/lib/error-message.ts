import { ApiError } from "@/lib/api";

export const getErrorMessage = (error: unknown, fallback: string) => {
  if (error instanceof ApiError) {
    return error.message || fallback;
  }

  if (error instanceof Error && error.message) {
    return error.message;
  }

  return fallback;
};
