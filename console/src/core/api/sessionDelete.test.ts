// sessionDelete's request (ADR 0101). The Agent moves a deleted session to the trash; the query
// keeps reclaim=1 because an Agent older than ADR 0101 forgot the session WITHOUT the trash when
// a DELETE came without it. stop=1 only when the caller asks to stop a running session first.
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
const fetchSpy = vi.fn(async () => new Response("{}"));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});

describe("sessionDelete", () => {
  it("sends DELETE with reclaim=1, and stop=1 only when asked", async () => {
    await client.sessionDelete("s 1");
    await client.sessionDelete("s2", { stop: true });
    const calls = fetchSpy.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([u, o]) => `${o.method} ${String(u).replace("http://localhost/", "")}`)).toEqual([
      "DELETE api/sessions/s%201?reclaim=1",
      "DELETE api/sessions/s2?reclaim=1&stop=1",
    ]);
  });
});
