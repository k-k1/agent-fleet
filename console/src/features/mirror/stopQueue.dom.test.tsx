// The Console's reading of the ADR 0105 wire, checked against the frozen fixture the Go side
// reads too (workspace/agent/testdata/stop-queue-wire.json): every /turn case through
// sessionTurn, and the messages keys through the parsers. Plus the pure rules of stopQueue.ts.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, it, expect, afterEach, vi } from "vitest";

import {
  isMemberOrigin,
  parseDiscards,
  parseQueueItems,
  sessionTurn,
  type Discard,
  type TurnOp,
} from "../../core/api/client.ts";
import {
  closeStep,
  discardView,
  emptyDiscardNotices,
  queueEntries,
  restorable,
  restoreStep,
  stopRowVisible,
  visibleDiscards,
} from "./stopQueue.ts";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Json = any;
// A dom-project file only because client.ts needs a browser at load. The fixture path is
// resolved from console/, where the tests always run (see twoStageStop.dom.test.tsx).
const fixture: Json = JSON.parse(
  readFileSync(resolve(process.cwd(), "../workspace/agent/testdata/stop-queue-wire.json"), "utf8"),
);

afterEach(() => vi.unstubAllGlobals());

// Runs one fixture case through sessionTurn: the request body the Console builds must be the
// fixture's request exactly, and the answer is the fixture's response.
async function runCase(c: Json) {
  let sent: Json = null;
  vi.stubGlobal("fetch", async (_url: string, init?: RequestInit) => {
    sent = JSON.parse(String(init?.body));
    return new Response(JSON.stringify(c.response), { status: c.status, headers: { "Content-Type": "application/json" } });
  });
  const req = c.request as { op: TurnOp; discard_queue?: boolean; id?: string };
  const res = await sessionTurn("fixture-session", req.op, undefined, undefined, {
    discardQueue: req.discard_queue,
    id: req.id,
  });
  return { sent, res };
}

describe("sessionTurn against the frozen /turn fixture", () => {
  const cases = Object.entries(fixture.turn as Record<string, Json>);
  it("covers every case", () => {
    expect(cases.length).toBeGreaterThanOrEqual(14);
  });
  for (const [name, c] of cases) {
    it(name, async () => {
      const { sent, res } = await runCase(c);
      expect(sent).toEqual(c.request);
      if (c.status !== 200) {
        expect(res.ok).toBe(false);
        expect(res.code).toBe(c.response.error.code);
        return;
      }
      expect(res.ok).toBe(true);
      const r = c.response;
      expect(res.stop).toBe(r.stop);
      if ("discard" in r) expect(res.discard ?? null).toEqual(r.discard);
      else expect(res.discard).toBeUndefined();
      if (r.removed) expect(res.removed).toEqual(r.removed);
      if ("dismissed" in r) expect(res.dismissed).toBe(r.dismissed);
    });
  }

  it("does not fall back to /input for the queue ops of an Agent without /turn", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", async (url: string) => {
      urls.push(url);
      return new Response("404 page not found", { status: 404 });
    });
    const res = await sessionTurn("s", "remove", undefined, undefined, { id: "cm_b" });
    expect(res.ok).toBe(false);
    expect(urls).toHaveLength(1);
  });
});

describe("messages keys", () => {
  const m = fixture.messages;
  it("reads queuedItems with ids, origins, states and attachments", () => {
    const items = parseQueueItems(m.working.queuedItems)!;
    expect(items).toEqual(m.working.queuedItems);
    expect(items.map((i) => i.state)).toEqual(["committed", "queued", "queued"]);
  });
  it("tells an absent queuedItems (older Agent, Terminal) from an empty one", () => {
    expect(parseQueueItems(undefined)).toBeNull();
    expect(parseQueueItems([])).toEqual([]);
  });
  it("reads discardedInputs", () => {
    expect(parseDiscards(m.working.discardedInputs)).toEqual(m.working.discardedInputs);
    expect(parseDiscards(m.idle_after_a_second_stop.discardedInputs)).toEqual(m.idle_after_a_second_stop.discardedInputs);
    expect(parseDiscards(undefined)).toEqual([]);
  });
  it("member input is member, discord and slack only", () => {
    const kinds = parseDiscards(m.idle_after_a_second_stop.discardedInputs)[0].items.map((i) => [
      i.origin?.kind,
      isMemberOrigin(i.origin),
    ]);
    expect(kinds).toEqual([
      ["member", true],
      ["schedule", false],
      ["operator", false],
      ["auto-resume", false],
      ["schedule-manual", false],
      ["slack", true],
    ]);
    expect(isMemberOrigin(undefined)).toBe(false);
    expect(isMemberOrigin({ kind: "" })).toBe(false);
  });
});

describe("queueEntries", () => {
  it("prefers queuedItems and falls back to queuedPrompts without ids", () => {
    const m = fixture.messages.working;
    expect(queueEntries(parseQueueItems(m.queuedItems), m.queuedPrompts).map((e) => e.item?.id)).toEqual([
      "cm_a",
      "af_c",
      "cm_b",
    ]);
    expect(queueEntries(null, m.queuedPrompts).every((e) => !e.item)).toBe(true);
  });
});

describe("restorable", () => {
  it("only the member's own still-queued input may go back into the input box", () => {
    const items = parseQueueItems(fixture.messages.working.queuedItems)!;
    // committed member / queued peer / queued discord
    expect(items.map(restorable)).toEqual([false, false, true]);
  });
});

describe("stopRowVisible", () => {
  const base = { busy: false, queued: false, question: false, approval: false };
  it("Managed: whenever it runs, holds anything, or waits on a question or approval", () => {
    expect(stopRowVisible({ ...base, managed: true })).toBe(false);
    for (const k of ["busy", "queued", "question", "approval"] as const) {
      expect(stopRowVisible({ ...base, managed: true, [k]: true }), k).toBe(true);
    }
  });
  it("Terminal (CLI): only while busy and not under a question", () => {
    expect(stopRowVisible({ ...base, managed: false, busy: true })).toBe(true);
    expect(stopRowVisible({ ...base, managed: false, busy: true, question: true })).toBe(false);
    expect(stopRowVisible({ ...base, managed: false, queued: true })).toBe(false);
  });
});

describe("discard notices", () => {
  const d = (): Discard => parseDiscards(fixture.messages.idle_after_a_second_stop.discardedInputs)[0];
  it("splits member input from other origins", () => {
    const v = discardView(d());
    expect(v.member.map((i) => i.id)).toEqual(["cm_d", "af_i"]);
    expect(v.others.map((i) => i.id)).toEqual(["af_e", "af_f", "af_g", "af_h"]);
  });
  it("restores one member entry per step, and dismisses and closes only after the last", () => {
    const s1 = restoreStep(emptyDiscardNotices, d());
    expect(s1.item?.id).toBe("cm_d");
    // One of two is back: the driver must keep the discard, or closing the tab now would lose
    // the other one on the server too.
    expect(s1.dismiss).toBe(false);
    expect(visibleDiscards([], s1.next).map((n) => [n.view.discard.id, n.restored])).toEqual([["dsc_0002", 1]]);
    const s2 = restoreStep(s1.next, d());
    expect(s2.item?.id).toBe("af_i");
    expect(s2.dismiss).toBe(true);
    expect(visibleDiscards([d()], s2.next)).toEqual([]);
    expect(restoreStep(s2.next, d()).item).toBeNull();
  });
  it("close always dismisses, and a stale poll cannot bring it back", () => {
    const c = closeStep(emptyDiscardNotices, "dsc_0002");
    expect(c.dismiss).toBe(true);
    expect(visibleDiscards([d()], c.next)).toEqual([]);
    // Half-way through a restore, closing is what gives the rest up.
    const r = restoreStep(emptyDiscardNotices, d());
    expect(closeStep(r.next, "dsc_0002").dismiss).toBe(true);
  });
  it("a discard with no member input has nothing to restore and is dismissed only by close", () => {
    const peerOnly: Discard = { ...d(), items: d().items.filter((i) => !isMemberOrigin(i.origin)) };
    const r = restoreStep(emptyDiscardNotices, peerOnly);
    expect(r.item).toBeNull();
    expect(r.dismiss).toBe(false);
    expect(closeStep(emptyDiscardNotices, peerOnly.id).dismiss).toBe(true);
  });
});
