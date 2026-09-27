// "Open image generation" always lands on a studio (ADR 0100 decision 10, revised): the one
// this browser opened last unless the Agent says it is gone, else a new one; a picture's
// recovered fields make a new studio of their own. A double press must not leave two studios.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const calls: string[] = [];
const toasts: string[] = [];
let studios = new Set<string>();
let createFails = false;
let getThrows = false;
let seq = 0;

vi.mock("../../ui/toast.ts", () => ({
  toast: (m: string) => {
    toasts.push(m);
    return true;
  },
}));
vi.mock("./api.ts", () => ({
  getStudio: async (id: string) => {
    calls.push(`get ${id}`);
    if (getThrows) throw new Error("offline");
    return studios.has(id) ? { id } : { error: { code: "not_found" } };
  },
  createStudio: async (body: { draft: { prompt?: string } }) => {
    calls.push(`create prompt=${body.draft.prompt ?? ""}`);
    await new Promise((r) => setTimeout(r, 5));
    if (createFails) return { error: { code: "unavailable" } };
    const id = `st${++seq}`;
    studios.add(id);
    return { id };
  },
}));

const { useLayoutStore } = await import("../../layout/store.ts");
const { openImagegen, lastStudio, rememberStudio } = await import("./open.ts");
const { emptyDraft } = await import("./draft.ts");

const opened: string[] = [];
beforeEach(() => {
  calls.length = 0;
  toasts.length = 0;
  opened.length = 0;
  studios = new Set(["old"]);
  createFails = false;
  getThrows = false;
  seq = 0;
  localStorage.clear();
  useLayoutStore.setState({
    openTarget: (t: { content: { kind: string; studioId?: string | null } }) => opened.push(`here ${t.content.studioId}`),
    openTargetInNew: (t: { content: { kind: string; studioId?: string | null } }) => opened.push(`new ${t.content.studioId}`),
  } as never);
});
afterEach(() => localStorage.clear());

describe("openImagegen", () => {
  it("覚えているスタジオがあればそれを開き、作らない", async () => {
    rememberStudio("old");
    expect(await openImagegen()).toEqual({ studioId: "old" });
    expect(calls).toEqual(["get old"]);
    expect(opened).toEqual(["here old"]);
  });

  it("覚えていなければ新しいスタジオを作って開き、次回のために覚える", async () => {
    expect(await openImagegen()).toEqual({ studioId: "st1" });
    expect(calls).toEqual(["create prompt="]);
    expect(opened).toEqual(["here st1"]);
    expect(lastStudio()).toBe("st1");
  });

  it("Agent が「無い」と言ったら忘れて新しく作る", async () => {
    rememberStudio("gone");
    expect(await openImagegen()).toEqual({ studioId: "st1" });
    expect(calls).toEqual(["get gone", "create prompt="]);
    expect(lastStudio()).toBe("st1");
  });

  it("届かないだけなら覚えているスタジオを開く（2 つ目を作らない）", async () => {
    rememberStudio("old");
    getThrows = true;
    expect(await openImagegen()).toEqual({ studioId: "old" });
    expect(calls).toEqual(["get old"]);
  });

  it("draft 付きはその draft で新しいスタジオを作る（覚えているスタジオは触らない）", async () => {
    rememberStudio("old");
    expect(await openImagegen({ draft: { ...emptyDraft(), prompt: "harbour at dusk" } })).toEqual({ studioId: "st1" });
    expect(calls).toEqual(["create prompt=harbour at dusk"]);
    expect(opened).toEqual(["here st1"]);
  });

  it("fresh + newPane は隣のペインに空のスタジオ", async () => {
    rememberStudio("old");
    await openImagegen({ fresh: true, newPane: true });
    expect(calls).toEqual(["create prompt="]);
    expect(opened).toEqual(["new st1"]);
  });

  it("作れなければトーストで知らせ、ペインは開かない", async () => {
    createFails = true;
    const r = await openImagegen();
    expect(r.studioId).toBeUndefined();
    expect(r.error).toBeTruthy();
    expect(toasts).toHaveLength(1);
    expect(opened).toEqual([]);
    expect(lastStudio()).toBeNull();
  });

  it("二度押しでもスタジオは 1 つ", async () => {
    const [a, b] = await Promise.all([openImagegen({ fresh: true }), openImagegen({ fresh: true })]);
    expect(a).toEqual({ studioId: "st1" });
    expect(b).toEqual({ studioId: "st1" });
    expect(calls).toEqual(["create prompt="]);
    // Settled, the next press is a new request of its own.
    await openImagegen({ fresh: true });
    expect(calls).toEqual(["create prompt=", "create prompt="]);
  });

  it("スタジオ id 指定はそのまま開く（Agent に問い合わせない）", async () => {
    expect(await openImagegen({ studioId: "x", newPane: true })).toEqual({ studioId: "x" });
    expect(calls).toEqual([]);
    expect(opened).toEqual(["new x"]);
  });
});
