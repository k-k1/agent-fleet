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
