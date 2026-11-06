// The share-target service worker (public/sw.js) must refuse a share POST that a page on another
// site submitted, and keep accepting the OS share sheet (no referrer, no origin).
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

type Listener = (e: { request: unknown; respondWith: (p: Promise<unknown>) => void }) => void;

function loadSw() {
  const src = readFileSync(resolve(__dirname, "../public/sw.js"), "utf8");
  const listeners: Record<string, Listener> = {};
  const stash: string[] = [];
  const self = {
    location: { origin: "https://console.test" },
    registration: { scope: "https://console.test/" },
    skipWaiting() {},
    clients: { claim() {} },
    addEventListener: (n: string, f: Listener) => (listeners[n] = f),
  };
  class FakeResponse {
    constructor(
      public body: unknown,
      public init?: unknown,
    ) {}
    static redirect(url: string, status: number) {
      return { redirect: url, status };
    }
  }
  const caches = { open: async () => ({ put: async (k: string) => void stash.push(k) }) };
  new Function("self", "Response", "caches", "URL", src)(self, FakeResponse, caches, URL);
  return { listeners, stash };
}

async function share(headers: Record<string, string>, referrer = "") {
  const { listeners, stash } = loadSw();
  let answered: Promise<unknown> | undefined;
  const request = {
    method: "POST",
    url: "https://console.test/share-target",
    referrer,
    headers: { get: (k: string) => headers[k] ?? null },
    formData: async () => ({ get: () => "", getAll: () => [] }),
  };
  listeners.fetch({ request, respondWith: (p) => (answered = p) });
  return { res: (await answered) as { redirect: string }, stash };
}

describe("share-target service worker", () => {
  it("accepts the OS share sheet", async () => {
    const { res, stash } = await share({ "Sec-Fetch-Site": "none" });
    expect(res.redirect).toMatch(/\?share=/);
    expect(stash.length).toBeGreaterThan(0);
  });
  it("accepts a same-origin share", async () => {
    const { res } = await share({ Origin: "https://console.test" }, "https://console.test/");
    expect(res.redirect).toMatch(/\?share=/);
  });
  it.each([
    ["cross-site metadata", { "Sec-Fetch-Site": "cross-site" }, ""],
    ["same-site metadata", { "Sec-Fetch-Site": "same-site" }, ""],
    ["foreign origin", { Origin: "https://evil.example.org" }, ""],
    ["foreign referrer", {}, "https://p-3000.console.test/"],
  ])("refuses %s and stashes nothing", async (_n, h, ref) => {
    const { res, stash } = await share(h as Record<string, string>, ref);
    expect(res.redirect).toBe("https://console.test/");
    expect(stash).toHaveLength(0);
  });
});
