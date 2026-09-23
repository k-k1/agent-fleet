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

vi.mock("./api.ts", () => ({
  getStudio: async () => current,
  patchStudio: async (_id: string, body: StudioPatch, ifMatch: string) => {
    calls.push({ body, ifMatch });
    const next = putAnswers.shift();
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

import { useStudio, type StudioState } from "./useStudio.ts";

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
