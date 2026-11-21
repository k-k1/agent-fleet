// memoQueueDeliveries' request (#4678). closed_since always travels with include=open: a Control
// Plane that does not know closed_since answers the plain newest fifty, and the open runs older
// than that must not drop out of the read.
import { beforeAll, describe, expect, it, vi } from "vitest";

// client.ts binds window.fetch and reads document.baseURI at import time, so stub first.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("sessionStorage", {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
});
vi.stubGlobal("document", { baseURI: "http://localhost/" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });
// Answers per URL; the default is 200.
const answers: Response[] = [];
const fetchSpy = vi.fn(async () => answers.shift() ?? new Response("{}"));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});

describe("memoQueueDeliveries", () => {
  it("asks for the open runs alone, with closed_since, or for the plain list", async () => {
    await client.memoQueueDeliveries("c 1");
    await client.memoQueueDeliveries("c1", true);
    await client.memoQueueDeliveries("c1", true, "2026-11-01T00:45:00.000000Z");
    const calls = fetchSpy.mock.calls as unknown as [string][];
    expect(calls.map(([u]) => String(u).replace("http://localhost/", ""))).toEqual([
      "api/memo-queues/c%201/deliveries",
      "api/memo-queues/c1/deliveries?include=open",
      "api/memo-queues/c1/deliveries?include=open&closed_since=2026-11-01T00%3A45%3A00.000000Z",
    ]);
  });
});
