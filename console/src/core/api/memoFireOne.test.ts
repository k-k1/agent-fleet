// memoQueueFireOne's request (#4919). The named press is a route of its own: a Control Plane that
// predates it answers 404 and starts nothing, where a body on the plain route would be ignored and
// the oldest memos started. The real client function is called here, so a change that posts the
// named press to the plain route is caught at the fetch boundary.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

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
const answers: Response[] = [];
const fetchSpy = vi.fn(async (_url: unknown, _init?: RequestInit) => answers.shift() ?? new Response("{}"));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});
beforeEach(() => {
  fetchSpy.mockClear();
  answers.length = 0;
});

const urls = () => fetchSpy.mock.calls.map(([u]) => String(u).replace("http://localhost/", ""));

describe("memoQueueFireOne", () => {
  it("posts to the named route, with both ids escaped, and never to the plain one", async () => {
    await client.memoQueueFireOne("c 1", "m/1");
    expect(urls()).toEqual(["api/memo-queues/c%201/fire/m%2F1"]);
    expect(fetchSpy.mock.calls[0]?.[1]?.method).toBe("POST");
  });
  it("does not fall back to the plain route when the Control Plane answers 404", async () => {
    answers.push(new Response(JSON.stringify({ error: { code: "not_found", message: "no" } }), { status: 404 }));
    const res = await client.memoQueueFireOne("c1", "m1");
    expect(urls()).toEqual(["api/memo-queues/c1/fire/m1"]);
    expect(res?.error?.code).toBe("not_found");
  });
  it("leaves the category's own press on the plain route", async () => {
    await client.memoQueueFire("c1");
    expect(urls()).toEqual(["api/memo-queues/c1/fire"]);
  });
});
