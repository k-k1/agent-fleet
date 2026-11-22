import { afterEach, describe, expect, it, vi } from "vitest";
import { probePushKey } from "./pushApi.ts";

const reply = (status: number, body: unknown) => vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(body), { status }));
afterEach(() => vi.restoreAllMocks());

describe("probePushKey", () => {
  it("returns the key", async () => {
    reply(200, { publicKey: "k" });
    expect(await probePushKey()).toBe("k");
  });
  it("reads the CP's own push_unavailable as not configured", async () => {
    reply(503, { error: { code: "push_unavailable", message: "x" } });
    expect(await probePushKey()).toBeNull();
  });
  it("treats a bare gateway 503 as transient, not as not configured", async () => {
    reply(503, "Service unavailable");
    await expect(probePushKey()).rejects.toThrow();
  });
  it("treats a network failure as transient", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("offline"));
    await expect(probePushKey()).rejects.toThrow();
  });
});
