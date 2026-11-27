// Switching sessions must drop the previous session's state at once (the retry band, a carried
// question card, …), not when the new session's first transcript response arrives (that one can be
// slow, or fail).
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";

vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as object),
  api: vi.fn(() => new Promise(() => {})), // the new session's response never arrives
}));

import { useTranscriptPoll } from "./useTranscriptPoll.ts";
import { api } from "../../../core/api/client.ts";
import type { Turn } from "../transcript/types.ts";

function fakeState() {
  const fns: Record<string, ReturnType<typeof vi.fn>> = {};
  const refs: Record<string, { current: unknown }> = {};
  const st = new Proxy({} as Record<string, unknown>, {
    get(_t, k: string) {
      if (/Ref$/.test(k)) return (refs[k] ??= { current: k === "pendingSendsRef" ? [] : k === "queueOpsRef" ? new Set() : "" });
      return (fns[k] ??= vi.fn());
    },
  });
  return { st, fns, refs };
}

function Probe({ session, st }: { session: string; st: unknown }) {
  const scroll = new Proxy({}, { get: () => vi.fn() });
  const tts = new Proxy({}, { get: () => vi.fn() });
  useTranscriptPoll({
    session,
    st: st as never,
    scroll: scroll as never,
    tts: tts as never,
    marksReloadRef: { current: () => {} },
  });
  return null;
}

describe("useTranscriptPoll reset", () => {
  it("clears the retry band when the session changes", () => {
    const { st, fns } = fakeState();
    const host = document.createElement("div");
    const root = createRoot(host);
    act(() => root.render(<Probe session="A" st={st} />));
    fns.setRetry.mockClear();
    act(() => root.render(<Probe session="B" st={st} />));
    expect(fns.setRetry).toHaveBeenCalledWith(null);
    act(() => root.unmount());
  });

  it("drops the previous session's carried, approval and pending-text state when the session changes", () => {
    const { st, fns } = fakeState();
    const host = document.createElement("div");
    const root = createRoot(host);
    act(() => root.render(<Probe session="A" st={st} />));
    // Each empty value is the one the poll itself writes when the field is absent.
    const empties: Record<string, unknown> = {
      setCarried: null,
      setPendingApproval: null,
      setPendingText: "",
      setRestoredAt: [],
      setAgentCtx: null,
      setCompactProg: null,
      setBgBusyReason: "",
    };
    for (const k of Object.keys(empties)) fns[k].mockClear();
    act(() => root.render(<Probe session="B" st={st} />));
    for (const [k, v] of Object.entries(empties)) expect(fns[k], k).toHaveBeenCalledWith(v);
    act(() => root.unmount());
  });
});

it("inserts an Antigravity failure in order and updates the same turn when its log arrives", async () => {
  const { st, fns, refs } = fakeState();
  const root = createRoot(document.createElement("div"));
  const first = { role: "user", idx: 0, text: "first" };
  const latest = { role: "assistant", idx: 11, text: "continued" };
  const failure = { role: "assistant", idx: 5, parts: [{ kind: "error", text: "executor failed" }] };
  const updated = { ...failure, parts: [{ kind: "text", text: "partial answer" }, ...failure.parts] };
  vi.mocked(api)
    .mockResolvedValueOnce({ stepIndexed: true, cursor: 12, firstLine: 0, messages: [first, latest] })
    .mockResolvedValueOnce({ stepIndexed: true, cursor: 12, messages: [failure, latest] })
    .mockResolvedValueOnce({ stepIndexed: true, cursor: 12, messages: [updated, latest] });
  const turns = () => fns.setTurns.mock.calls.reduce<Turn[]>(
    (acc, [update]) => typeof update === "function" ? update(acc) : update,
    [],
  );
  try {
    await act(async () => root.render(<Probe session="agy" st={st} />));
    expect(turns()).toEqual([first, latest]);
    const tick = refs.tickRef.current as () => void;
    await act(async () => tick());
    expect(api).toHaveBeenLastCalledWith("api/sessions/agy/messages?since=12&from=0");
    expect(turns()).toEqual([first, failure, latest]);
    await act(async () => tick());
    expect(turns()).toEqual([first, updated, latest]);
  } finally {
    act(() => root.unmount());
  }
});

it("preserves the loaded Antigravity history range when an echo refreshes the tail", async () => {
  const { st, fns, refs } = fakeState();
  const root = createRoot(document.createElement("div"));
  const latest = { role: "assistant", idx: 11, text: "last reply" };
  const oldTail = { stepIndexed: true, cursor: 12, firstLine: 10, hasMore: true, messages: [latest] };
  vi.mocked(api)
    .mockResolvedValueOnce(oldTail)
    .mockResolvedValueOnce({ stepIndexed: true, cursor: 12, messages: [] })
    .mockResolvedValueOnce(oldTail)
    .mockResolvedValueOnce({ stepIndexed: true, cursor: 12, messages: [latest] });
  try {
    await act(async () => root.render(<Probe session="agy-resync" st={st} />));
    refs.firstLineRef.current = 0; // the reader paged to the beginning
    fns.setHasMore.mockClear();
    refs.pendingSendsRef.current = [{ id: "echo", at: Date.now() - 21000 }];
    const tick = refs.tickRef.current as () => void;
    await act(async () => tick());
    expect(refs.cursorRef.current).toBe(0);
    refs.pendingSendsRef.current = []; // the one-shot retry has been scheduled
    await act(async () => tick());
    expect(api).toHaveBeenLastCalledWith("api/sessions/agy-resync/messages?since=0&tail=1&limit=400");
    expect(refs.firstLineRef.current).toBe(0);
    expect(fns.setHasMore).not.toHaveBeenCalledWith(true);
    await act(async () => tick());
    expect(api).toHaveBeenLastCalledWith("api/sessions/agy-resync/messages?since=12&from=0");
  } finally {
    act(() => root.unmount());
  }
});
