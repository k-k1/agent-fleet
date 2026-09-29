// "Switch agents" (ADR 0100 decision 2): the studio is unbound first — the Agent refuses to hand a
// studio over from a live session — and the old session is HALTED, never stopped. /stop forgets
// the session, so the conversation the member had with the old agent would leave the list for
// good (measured on the dev deployment: the old session answered 404 afterwards).
import { describe, expect, it, vi } from "vitest";

const calls: string[] = [];
let putStatus = 200;
let created: Record<string, unknown> = { name: "snew001" };
let current: { title: string; updated_at: string } | null = null;
vi.mock("../../core/api/client.ts", () => ({
  raw: async (path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return new Response("{}");
  },
  apiJSON: async (path: string, method: string, body: { studio?: string }) => {
    calls.push(`${method} ${path} studio=${body.studio}`);
    return created;
  },
  errText: () => "",
  errDetail: () => "",
}));
vi.mock("./api.ts", () => ({
  bindStudio: async (id: string, session: string) => {
    calls.push(`bind ${id} "${session}"`);
    return { id };
  },
  createStudio: async () => ({ id: "st1" }),
  studioPersona: async () => ({ prompt: "persona", lang: "ja" }),
  getStudio: async () => current,
  patchStudio: async (id: string, body: unknown, ifMatch: string) => {
    calls.push(`put ${id} ${ifMatch} ${JSON.stringify(body)}`);
    return { status: putStatus };
  },
}));

import { attachAgent } from "./attach.ts";

describe("switching the studio's agent", () => {
  it("unbinds, halts the old session and creates the new one bound", async () => {
    const r = await attachAgent({
      studioId: "st1",
      draft: () => ({}),
      replacing: "sold001",
      opts: { dir: "/home/u/repos/r", kind: "codex", driver: "managed", worktree: true } as never,
    });
    expect(r).toEqual({ studioId: "st1", session: "snew001" });
    expect(calls).toEqual(['bind st1 ""', "POST api/sessions/sold001/halt", "POST api/sessions studio=st1"]);
  });
});

describe("attaching to an existing studio", () => {
  it("writes the dialog's model and the default title before the persona is read", async () => {
    calls.length = 0;
    const r = await attachAgent({
      studioId: "st1",
      draft: () => ({}),
      existing: { title: "", updatedAt: "v1", draft: { provider: "comfy", model: "old" } },
      opts: { dir: "", kind: "claude", driver: "tui", imageProvider: "comfy", imageModel: "flux", place: "" } as never,
    });
    expect(r.session).toBe("snew001");
    expect(calls[0]).toBe('put st1 v1 {"author":"human","draft":{"model":"flux"},"title":"ホーム · Claude Code"}');
  });
  it("leaves a named studio's title and an unchanged model alone", async () => {
    calls.length = 0;
    await attachAgent({
      studioId: "st1",
      draft: () => ({}),
      existing: { title: "mine", updatedAt: "v1", draft: { provider: "comfy", model: "flux" } },
      opts: { dir: "", kind: "claude", driver: "tui", imageProvider: "comfy", imageModel: "flux", place: "" } as never,
    });
    expect(calls.some((c) => c.startsWith("put"))).toBe(false);
  });
  it("does not start the agent when only the provider changed and the write failed", async () => {
    calls.length = 0;
    putStatus = 500;
    const r = await attachAgent({
      studioId: "st1",
      draft: () => ({}),
      existing: { title: "mine", updatedAt: "v1", draft: { provider: "comfy-a", model: "sdxl" } },
      opts: { dir: "", kind: "claude", driver: "tui", imageProvider: "comfy-b", imageModel: "sdxl", place: "" } as never,
    });
    putStatus = 200;
    expect(r.session).toBeUndefined();
    expect(r.error).toBeTruthy();
    expect(calls.some((c) => c.startsWith("POST api/sessions"))).toBe(false);
  });
});

describe("a studio started in a new worktree", () => {
  const opts = { dir: "/home/u/repos/app@feat", kind: "codex", driver: "managed", worktree: true, place: "app@feat", imageModel: "flux" };

  it("takes the name of the worktree the Agent cut, not of the row it was started from", async () => {
    calls.length = 0;
    putStatus = 200;
    created = { name: "snew002", repo: "app@wip-s1abcde" };
    current = { title: "app@feat · Codex", updated_at: "v2" };
    const r = await attachAgent({ studioId: null, draft: () => ({}), opts: opts as never });
    expect(r.session).toBe("snew002");
    expect(calls.at(-1)).toBe('put st1 v2 {"author":"human","title":"app@wip-s1abcde · Codex"}');
  });

  it("leaves a title the member typed in the meantime", async () => {
    calls.length = 0;
    created = { name: "snew003", repo: "app@wip-s1abcde" };
    current = { title: "my harbour", updated_at: "v3" };
    await attachAgent({ studioId: null, draft: () => ({}), opts: opts as never });
    expect(calls.some((c) => c.startsWith("put"))).toBe(false);
  });

  it("leaves a studio the member had already named", async () => {
    calls.length = 0;
    created = { name: "snew004", repo: "app@wip-s1abcde" };
    current = { title: "mine", updated_at: "v4" };
    await attachAgent({
      studioId: "st1",
      draft: () => ({}),
      existing: { title: "mine", updatedAt: "v1", draft: { model: "flux" } },
      opts: opts as never,
    });
    expect(calls.some((c) => c.startsWith("put"))).toBe(false);
  });
});
