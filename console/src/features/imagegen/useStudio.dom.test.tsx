// useStudio's save path against the Agent's answers (ADR 0100 decision 3, review 🟡C2/🟡C6): a
// 412 must not throw away what the member typed, a save that got no answer must be retried
// rather than left dirty, and the agent's outlines survive the pane being reopened.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { StudioPatch, StudioWire } from "./wire.ts";

const calls: { body: StudioPatch; ifMatch: string }[] = [];
let putAnswers: ((body: StudioPatch) => { status: number; studio?: StudioWire })[] = [];
let current: StudioWire;
// When set, patchStudio answers only after this resolves — a save still in flight.
let patchGate: Promise<void> | null = null;

vi.mock("./api.ts", () => ({
  getStudio: async () => current,
  patchStudio: async (_id: string, body: StudioPatch, ifMatch: string) => {
    calls.push({ body, ifMatch });
    const next = putAnswers.shift(); // taken at request time, so a gated save keeps its own answer
    if (patchGate) await patchGate;
    if (next) return next(body);
    // Apply the merge patch the way the Agent does (params one level deep), so the answer
    // carries what was saved.
    const d: Record<string, unknown> = { ...current.draft };
    for (const [k, v] of Object.entries(body.draft || {})) {
      if (v === null) delete d[k];
      else if (k === "params") {
        const p: Record<string, unknown> = { ...(current.draft.params || {}) };
        for (const [pk, pv] of Object.entries(v as Record<string, unknown>)) {
          if (pv === null) delete p[pk];
          else p[pk] = pv;
        }
        d.params = p;
      } else d[k] = v;
    }
    current = { ...current, draft: d, updated_at: current.updated_at + "+" };
    return { status: 200, studio: current };
  },
  pressStudio: async () => ({}),
  rewindStudio: async () => current,
  studioDraftLog: async () => ({ entries: [] }),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { forgetStudioState, useStudio, type StudioState } from "./useStudio.ts";

const ID = "0f8e2a4c-1b2d-4e5f-8a9b-0c1d2e3f4a5b";
let host: HTMLDivElement;
let root: Root;
let st: StudioState;

function Probe() {
  st = useStudio(ID, { running: true });
  return null;
}

const mount = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(<Probe />));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
};

const tick = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  calls.length = 0;
  putAnswers = [];
  patchGate = null;
  current = {
    id: ID,
    title: "t",
    draft: { prompt: "a", params: { cfg: 7 } },
    session: "s1",
    agent_trial: true,
    created_at: "c",
    updated_at: "v1",
    recent_log: [{ seq: 1, kind: "edit", at: "t", author: "human", changes: [{ field: "prompt" }] }],
  };
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("useStudio: 保存", () => {
  it("412: 新しいスタジオに人の鍵だけ載せ直し、新しい版で送り直す（打った文を失わない）", async () => {
    await mount();
    // The agent moved cfg while the member was typing the prompt.
    putAnswers.push(() => {
      current = { ...current, draft: { prompt: "a", params: { cfg: 5 } }, updated_at: "v2" };
      return { status: 412 };
    });
    await act(async () => st.patchForm({ prompt: "mine" }));
    await tick(600);
    expect(calls.map((c) => c.ifMatch)).toEqual(["v1", "v2"]);
    expect(calls[1].body.draft).toEqual({ prompt: "mine" });
    expect(st.form.prompt).toBe("mine");
    expect(st.form.cfg).toBe("5");
  });

  it("答えの無い保存は再送を予約する（汚れたまま放置しない）", async () => {
    await mount();
    putAnswers.push(() => ({ status: 0 }));
    await act(async () => st.patchForm({ prompt: "mine" }));
    await tick(600);
    expect(calls).toHaveLength(1);
    await tick(2100);
    expect(calls).toHaveLength(2);
    expect(calls[1].body.draft).toEqual({ prompt: "mine" });
  });

  it("params の摘みを消すと null で名指す（🔴C1）", async () => {
    current = { ...current, draft: { params: { steps: 30, cfg: 7, sampler: "euler" } } };
    await mount();
    await act(async () => st.patchForm({ sampler: "" }));
    await tick(600);
    expect(calls[0].body.draft).toEqual({ params: { steps: 30, cfg: 7, sampler: null } });
  });
});

describe("useStudio: teardown", () => {
  it("a save that fails after the pane unmounted schedules no retry and the edit survives a reopen", async () => {
    await mount();
    let release!: () => void;
    patchGate = new Promise<void>((r) => (release = r));
    putAnswers.push(() => ({ status: 0 }));
    await act(async () => st.patchForm({ prompt: "mine" }));
    await tick(600);
    expect(calls).toHaveLength(1);
    // The pane goes away with the save unanswered; the answer then arrives (no answer = status 0).
    await act(async () => root.unmount());
    release();
    await tick(0);
    // A retry armed now would fire after the environment is gone (`window` undefined).
    expect(vi.getTimerCount()).toBe(0);
    await tick(60000);
    expect(calls).toHaveLength(1);
    // Reopening the pane puts the unsaved edit back and sends it: nothing typed is lost.
    host.remove();
    await mount();
    await tick(600);
    expect(st.form.prompt).toBe("mine");
    expect(calls).toHaveLength(2);
    expect(calls[1].body.draft).toEqual({ prompt: "mine" });
  });
});

describe("useStudio: a parked edit", () => {
  it("restores only what the member changed, not fields the agent moved meanwhile", async () => {
    await mount();
    let release!: () => void;
    patchGate = new Promise<void>((r) => (release = r));
    putAnswers.push(() => ({ status: 0 }));
    await act(async () => st.patchForm({ prompt: "mine" }));
    await tick(600);
    await act(async () => root.unmount());
    release();
    await tick(0);
    patchGate = null;
    current = { ...current, draft: { ...current.draft, inputs: ["agent.png"] }, updated_at: "v2" };
    host.remove();
    await mount();
    await tick(600);
    expect(calls[1].body.draft).toEqual({ prompt: "mine" });
  });

  it("a pane reopened before the old save failed still gets the edit back", async () => {
    await mount();
    let release!: () => void;
    patchGate = new Promise<void>((r) => (release = r));
    putAnswers.push(() => ({ status: 0 }));
    await act(async () => st.patchForm({ prompt: "late" }));
    await tick(600);
    await act(async () => root.unmount());
    host.remove();
    await mount(); // its first read is done; the old save has not answered yet
    release();
    patchGate = null;
    await tick(600);
    expect(st.form.prompt).toBe("late");
    expect(calls[calls.length - 1].body.draft).toEqual({ prompt: "late" });
  });
});

describe("useStudio: a parked edit vs newer input", () => {
  for (const saved of [false, true]) {
    it(`an older failed save does not overwrite input typed after the reopen (new input ${saved ? "saved" : "still dirty"})`, async () => {
      await mount();
      let release!: () => void;
      patchGate = new Promise<void>((r) => (release = r));
      putAnswers.push(() => ({ status: 0 }));
      await act(async () => st.patchForm({ prompt: "old" }));
      await tick(600);
      await act(async () => root.unmount());
      host.remove();
      const gate = patchGate;
      patchGate = null;
      await mount();
      await act(async () => st.patchForm({ prompt: "new" }));
      if (saved) await tick(600);
      patchGate = gate;
      release();
      await tick(0);
      await tick(600);
      expect(st.form.prompt).toBe("new");
      expect(current.draft.prompt).toBe(saved ? "new" : current.draft.prompt);
      expect(calls.filter((c) => c.body.draft?.prompt === "old")).toHaveLength(1);
    });
  }
});

describe("useStudio: 縁取り（決定 6）", () => {
  it("エージェントの編集の縁取りは開き直しても残り、人が触ると消える", async () => {
    await mount();
    current = {
      ...current,
      updated_at: "v2",
      draft: { prompt: "b", params: { cfg: 7 } },
      recent_log: [...(current.recent_log || []), { seq: 2, kind: "edit", at: "t", author: "agent", changes: [{ field: "prompt" }] }],
    };
    await tick(2100); // the poll
    expect([...st.highlight]).toEqual(["prompt"]);
    await act(async () => root.unmount());
    root = createRoot(host);
    await act(async () => root.render(<Probe />));
    await tick(0);
    expect([...st.highlight]).toEqual(["prompt"]);
    await act(async () => st.patchForm({ prompt: "c" }));
    expect([...st.highlight]).toEqual([]);
  });
});

describe("useStudio: 押すと縁取りが消える", () => {
  it("試走・投入はエージェントの値を見て押したこと: 縁取りを全部消す", async () => {
    await mount();
    current = {
      ...current,
      updated_at: "v2",
      draft: { prompt: "b", params: { cfg: 5 } },
      recent_log: [
        ...(current.recent_log || []),
        { seq: 2, kind: "edit", at: "t", author: "agent", changes: [{ field: "prompt" }, { field: "params.cfg" }] },
      ],
    };
    await tick(2100);
    expect([...st.highlight].sort()).toEqual(["params.cfg", "prompt"]);
    await act(async () => void (await st.press("trial")));
    expect([...st.highlight]).toEqual([]);
  });

  it("摘みを 1 つ直すとその摘みの縁取りだけ消える", async () => {
    await mount();
    current = {
      ...current,
      updated_at: "v2",
      draft: { params: { cfg: 5, sampler: "euler" } },
      recent_log: [
        ...(current.recent_log || []),
        { seq: 2, kind: "edit", at: "t", author: "agent", changes: [{ field: "params.cfg" }, { field: "params.sampler" }] },
      ],
    };
    await tick(2100);
    await act(async () => st.patchForm({ cfg: "6" }));
    expect([...st.highlight]).toEqual(["params.sampler"]);
  });
});

describe("forgetStudioState", () => {
  it("削除したスタジオの縁取りと合図の位置だけを消す", async () => {
    await mount();
    localStorage.setItem(`af.imagegen-seen.${ID}`, "{}");
    localStorage.setItem(`af.imagegen-signal.${ID}.s1`, "3");
    localStorage.setItem("af.imagegen-seen.other", "{}");
    forgetStudioState(ID);
    expect(localStorage.getItem(`af.imagegen-seen.${ID}`)).toBeNull();
    expect(localStorage.getItem(`af.imagegen-signal.${ID}.s1`)).toBeNull();
    expect(localStorage.getItem("af.imagegen-seen.other")).toBe("{}");
  });
});
