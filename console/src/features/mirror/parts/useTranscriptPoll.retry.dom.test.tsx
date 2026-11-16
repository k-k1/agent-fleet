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

function fakeState() {
  const fns: Record<string, ReturnType<typeof vi.fn>> = {};
  const refs: Record<string, { current: unknown }> = {};
  const st = new Proxy({} as Record<string, unknown>, {
    get(_t, k: string) {
      if (/Ref$/.test(k)) return (refs[k] ??= { current: k === "pendingSendsRef" ? [] : k === "queueOpsRef" ? new Set() : "" });
      return (fns[k] ??= vi.fn());
    },
  });
  return { st, fns };
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
