export class ApiError extends Error {
  status: number;
  body: unknown;

  constructor(message: string, status: number, body: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: "same-origin"
  });
  const body = await parseBody(response);

  if (!response.ok) {
    throw new ApiError(errorMessage(body, response.statusText), response.status, body);
  }
  if (isObject(body) && body.ok === false) {
    throw new ApiError(errorMessage(body, "request failed"), response.status, body);
  }
  if (isObject(body) && "error" in body && typeof body.error === "string") {
    throw new ApiError(errorMessage(body, "request failed"), response.status, body);
  }
  if (isObject(body) && body.ok === true && "data" in body) {
    return body.data as T;
  }
  return body as T;
}

export const apiGet = <T>(path: string) => request<T>(path);
export const apiPost = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: "POST", body: body === undefined ? undefined : JSON.stringify(body) });
export const apiPut = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: "PUT", body: body === undefined ? undefined : JSON.stringify(body) });
export const apiDelete = (path: string) => request<void>(path, { method: "DELETE" });

async function parseBody(response: Response): Promise<unknown> {
  const text = await response.text();
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function errorMessage(body: unknown, fallback: string): string {
  if (isObject(body) && typeof body.error === "string") {
    if (typeof body.detail === "string" && body.detail.trim()) {
      return `${body.error}: ${body.detail}`;
    }
    return body.error;
  }
  if (typeof body === "string" && body.trim()) return body;
  return fallback;
}
