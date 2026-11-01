// usageAll.dom.test.tsx mocks api(), which cannot catch a wrong guess about the shape api()
// resolves. This one runs the real api() over a stubbed fetch: an old Agent's mux answers a
// plain-text 404 for /usage/all, and the chips must still reach their own endpoints.
import { describe, it, expect, vi } from "vitest";

describe("usage hub over the real api()", () => {
  it("falls back to the per-kind endpoint on a plain-text 404, not on a plain-text 409", async () => {
    let allStatus = 404;
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const u = String(input);
        urls.push(u);
        if (u.includes("/usage/all"))
          return Promise.resolve(new Response(allStatus === 404 ? "404 page not found\n" : "conflict", { status: allStatus, headers: { "content-type": "text/plain" } }));
        return Promise.resolve(new Response(JSON.stringify({ ok: true, authed: true }), { status: 200, headers: { "content-type": "application/json" } }));
      }),
    );
    // client.ts wraps window.fetch at import time, so the stub has to be in place first.
    const { subscribeUsage } = await import("./usageAll.ts");
    const got: unknown[] = [];
    const off = subscribeUsage("real", "claude", "api/claude/usage", (r) => got.push(r));
    try {
      await vi.waitFor(() => expect(got).toHaveLength(1));
      expect(urls.some((u) => u.endsWith("/api/claude/usage"))).toBe(true);

      urls.length = 0;
      allStatus = 409;
      const { refreshUsage } = await import("./usageAll.ts");
      await refreshUsage("real", "claude");
      expect(urls.filter((u) => u.includes("/usage/all"))).toHaveLength(1);
      expect(urls.some((u) => u.includes("/claude/usage"))).toBe(false);
    } finally {
      off();
      vi.unstubAllGlobals();
    }
  }, 30000); // the first import pulls in client.ts and its dependencies
});
