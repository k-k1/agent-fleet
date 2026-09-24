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
// Answers per URL; the default is 200.
const answers: Response[] = [];
const fetchSpy = vi.fn(async () => answers.shift() ?? new Response("{}"));
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

  it("with stop, an older Agent's 409 session_running is answered by halting and trying once more", async () => {
    fetchSpy.mockClear();
    const running = () => new Response(JSON.stringify({ error: { code: "session_running", message: "" } }), { status: 409 });
    answers.push(running());
    const res = await client.sessionDelete("s3", { stop: true });
    expect(res.ok).toBe(true);
    const calls = fetchSpy.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([u, o]) => `${o.method} ${String(u).replace("http://localhost/", "")}`)).toEqual([
      "DELETE api/sessions/s3?reclaim=1&stop=1",
      "POST api/sessions/s3/halt",
      "DELETE api/sessions/s3?reclaim=1&stop=1",
    ]);
  });

  it("does not halt a session that was resumed mid-delete (session_resumed), nor without stop", async () => {
    fetchSpy.mockClear();
    answers.push(new Response(JSON.stringify({ error: { code: "session_resumed", message: "" } }), { status: 409 }));
    expect((await client.sessionDelete("s4", { stop: true })).status).toBe(409);
    answers.push(new Response(JSON.stringify({ error: { code: "session_running", message: "" } }), { status: 409 }));
    expect((await client.sessionDelete("s5")).status).toBe(409);
    expect(fetchSpy.mock.calls.length).toBe(2);
  });
});
