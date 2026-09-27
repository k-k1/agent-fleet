// "Switch agents" (ADR 0100 decision 2): the studio is unbound first — the Agent refuses to hand a
// studio over from a live session — and the old session is HALTED, never stopped. /stop forgets
// the session, so the conversation the member had with the old agent would leave the list for
// good (measured on the dev deployment: the old session answered 404 afterwards).
import { describe, expect, it, vi } from "vitest";

const calls: string[] = [];
vi.mock("../../core/api/client.ts", () => ({
  raw: async (path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return new Response("{}");
  },
  apiJSON: async (path: string, method: string, body: { studio?: string }) => {
    calls.push(`${method} ${path} studio=${body.studio}`);
    return { name: "snew001" };
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
  getStudio: async () => null,
  patchStudio: async (id: string, body: unknown, ifMatch: string) => {
    calls.push(`put ${id} ${ifMatch} ${JSON.stringify(body)}`);
    return { status: 200 };
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
});
