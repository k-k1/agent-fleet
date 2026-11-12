// sessionEndWatch (#3947): the mirror's end-watch button. A watch the Agent no longer knows is
// reported as code "not_found" so the caller can treat it as already ended.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("document", { baseURI: "http://localhost/" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });
const answers: Response[] = [];
const fetchSpy = vi.fn(async () => answers.shift() ?? new Response("{}"));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});
beforeEach(() => {
  fetchSpy.mockClear();
  answers.length = 0;
});

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

describe("sessionEndWatch", () => {
  it("sends DELETE for the encoded session and watch id", async () => {
    answers.push(json(200, { ended: "w/1" }));
    expect((await client.sessionEndWatch("s 1", "w/1")).ok).toBe(true);
    const [u, o] = (fetchSpy.mock.calls as unknown as [string, RequestInit][])[0];
    expect(`${o.method} ${String(u).replace("http://localhost/", "")}`).toBe("DELETE api/sessions/s%201/watches/w%2F1");
  });

  it("passes the Agent's not_found code through", async () => {
    answers.push(json(404, { error: { code: "not_found", message: "this session has no watch w1" } }));
    const r = await client.sessionEndWatch("s1", "w1");
    expect(r.ok).toBe(false);
    expect(r.code).toBe("not_found");
  });
});
