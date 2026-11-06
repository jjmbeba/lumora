export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export const apiRequest = async <T>(
  path: string,
  init: RequestInit = {}
): Promise<T> => {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, {
    ...init,
    credentials: "include",
    headers,
  });

  if (!response.ok) {
    const responseText = await response.text();
    const message = responseText.trim();
    throw new ApiError(
      response.status,
      message || `Request failed with status ${response.status}`
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const data: unknown = await response.json();
  return data as T;
};

export const postJSON = <T>(path: string, body: unknown): Promise<T> =>
  apiRequest<T>(path, {
    method: "POST",
    body: JSON.stringify(body),
  });
