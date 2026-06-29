import { afterEach, describe, expect, it, vi } from "vitest";
import { apiGet, ApiError } from "./http";

afterEach(() => vi.restoreAllMocks());

describe("http client", () => {
  it("unwraps ok data envelopes", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true, data: [1] }), { status: 200 })));
    await expect(apiGet<number[]>("/api/test")).resolves.toEqual([1]);
  });

  it("accepts raw arrays", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify([{ id: "n1" }]), { status: 200 })));
    await expect(apiGet<Array<{ id: string }>>("/api/notifications")).resolves.toEqual([{ id: "n1" }]);
  });

  it("throws on direct error response", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "invalid event" }), { status: 400 })));
    await expect(apiGet("/api/test")).rejects.toMatchObject({ name: "ApiError", message: "invalid event" });
  });

  it("includes safe error details in thrown messages", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: "send notification failed",
      detail: "connect smtp server: connection refused",
    }), { status: 502 })));

    await expect(apiGet("/api/test")).rejects.toMatchObject({
      name: "ApiError",
      message: "send notification failed: connect smtp server: connection refused",
    });
  });

  it("sends same-origin credentials", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true, data: {} }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await apiGet("/api/auth/check");
    expect(fetchMock).toHaveBeenCalledWith("/api/auth/check", expect.objectContaining({ credentials: "same-origin" }));
  });
});
