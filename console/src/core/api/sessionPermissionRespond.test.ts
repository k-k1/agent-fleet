// sessionPermissionRespond (#3354): the permission card answers by meaning, and the Agent picks
// the row. Against an Agent without the route only layout-free keys may go out: Down-based
// sequences pressed "No" for always and wrapped onto "Yes" for deny on a two-row menu.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// client.ts binds window.fetch and reads document.baseURI at import time, so stub first.
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

const sent = () =>
  (fetchSpy.mock.calls as unknown as [string, RequestInit][]).map(
    ([u, o]) => `${o.method} ${String(u).replace("http://localhost/", "")} ${String(o.body ?? "")}`,
  );
const notFound = () => new Response("404 page not found\n", { status: 404 });

describe("sessionPermissionRespond", () => {
  it("posts the choice to /permission-respond", async () => {
    expect((await client.sessionPermissionRespond("s1", "always")).ok).toBe(true);
    expect(sent()).toEqual(['POST api/sessions/s1/permission-respond {"choice":"always"}']);
  });

  it("an older Agent: deny falls back to Escape and allow to Enter, never Down", async () => {
    answers.push(notFound());
    expect((await client.sessionPermissionRespond("s2", "deny")).ok).toBe(true);
    answers.push(notFound());
    expect((await client.sessionPermissionRespond("s2", "allow")).ok).toBe(true);
    expect(sent()).toEqual([
      'POST api/sessions/s2/permission-respond {"choice":"deny"}',
      'POST api/sessions/s2/input {"keys":["Escape"]}',
      'POST api/sessions/s2/permission-respond {"choice":"allow"}',
      'POST api/sessions/s2/input {"keys":["Enter"]}',
    ]);
  });

  it("an older Agent: always fails rather than guess a row", async () => {
    answers.push(notFound());
    expect((await client.sessionPermissionRespond("s3", "always")).ok).toBe(false);
    expect(sent()).toHaveLength(1);
  });

  it("a refusal is reported with its code, and nothing else is sent", async () => {
    answers.push(new Response(JSON.stringify({ error: { code: "choice_unavailable", message: "no row" } }), { status: 409 }));
    const res = await client.sessionPermissionRespond("s4", "always");
    expect(res).toMatchObject({ ok: false, code: "choice_unavailable" });
    expect(sent()).toHaveLength(1);
  });
});

describe("parsePermissionChoices", () => {
  it("keeps the known choices in card order and treats a missing field as not reported", () => {
    expect(client.parsePermissionChoices(["deny", "allow", "bogus"])).toEqual(["allow", "deny"]);
    expect(client.parsePermissionChoices(undefined)).toBeNull();
  });

  it("samePermChoices keeps the previous array when unchanged", () => {
    const a = client.parsePermissionChoices(["allow", "deny"]);
    expect(client.samePermChoices(a, ["allow", "deny"])).toBe(a);
    expect(client.samePermChoices(a, ["allow", "always", "deny"])).toEqual(["allow", "always", "deny"]);
  });
});
