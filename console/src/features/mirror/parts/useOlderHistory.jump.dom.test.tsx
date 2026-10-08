// A palette hit older than the loaded window (#1663): useOlderHistory pages back until the turn is
// held, then hands the mark to the scroller — and only then. jsdom has no layout, so the scroller
// is a spy; what is proved here is the sequencing, the request sizes and the fallbacks.
import { describe, it, expect, afterEach, vi, beforeEach } from "vitest";
import { act, useState, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../../core/api/client.ts")>()),
  api: (...a: unknown[]) => apiMock(...a),
}));

import { useOlderHistory } from "./useTranscriptPoll.ts";
import { clearMarks, requestJump } from "../scrollMark.ts";

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

let jumped: unknown[] = [];
let toasts: string[] = [];
let inputSeq = { current: 0 };
let turnsAtJump: number[] = [];
let stride = 1; // idx per cursor position: 1 = claude, >1 = a store-backed agent's sparse idx
let moved = false; // what scroll.placeMoved reports (the reader dragged the scrollbar)
let older: (() => Promise<void>) | null = null;

function Harness({ session, loaded = true, first = 1000 }: { session: string; loaded?: boolean; first?: number }) {
  const [turns, setTurns] = useState<{ idx: number }[]>([{ idx: first * stride }]);
  const [hasMore, setHasMore] = useState(first > 0);
  const [, setLoadingOlder] = useState(false);
  const firstLineRef = useRef(first);
  const loadingOlderRef = useRef(false);
  const topSentinelRef = useRef(null);
  const bodyRef = useRef(null);
  const st = { turns, setTurns, firstLineRef, hasMore, setHasMore, setLoadingOlder, loadingOlderRef, topSentinelRef, loaded, stateSession: session };
  const scroll = {
    bodyRef,
    inputSeqRef: inputSeq,
    placeSnapshot: () => ({ atBottom: false, mark: null }),
    placeMoved: () => moved,
    capturePrependAnchor: () => {},
    applyPrependAdjust: () => {},
    jumpTo: (m: unknown) => { jumped.push(m); turnsAtJump.push(turns[0].idx); return true; },
  };
  older = useOlderHistory({ session, st: st as never, scroll: scroll as never, toast: (m) => toasts.push(m) });
  return <div data-first={turns[0].idx} />;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
function mount(props: Parameters<typeof Harness>[0]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness {...props} />));
}

beforeEach(() => {
  apiMock.mockReset();
  jumped = []; toasts = []; turnsAtJump = []; stride = 1; moved = false; older = null;
  inputSeq = { current: 0 };
  // The page before `before`: `limit` positions, the oldest at before-limit.
  apiMock.mockImplementation(async (url: string) => {
    const before = Number(/before=(\d+)/.exec(url)![1]);
    const limit = Number(/limit=(\d+)/.exec(url)![1]);
    const lo = Math.max(0, before - limit);
    return { messages: [{ idx: lo * stride }], firstLine: lo, hasMore: lo > 0 };
  });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  clearMarks();
});

describe("useOlderHistory explicit jump", () => {
  it("pages back to a hit older than the window, then jumps once the turns are mounted", async () => {
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    await flush();
    expect(apiMock).toHaveBeenCalledTimes(1);
    expect(apiMock.mock.calls[0][0]).toContain("before=1000&limit=750");
    expect(jumped).toHaveLength(1);
    expect(turnsAtJump[0]).toBeLessThanOrEqual(250); // the jump saw the paged-in turns
    expect(toasts).toEqual([]);
  });

  it("reaches a hit by the oldest idx held when idx is sparse (cursor counts positions)", async () => {
    stride = 10; // cursor 1000 = idx 10000: a hit at idx 3000 is far outside, although 3000 >= 1000
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 3000, offset: 0, near: true }));
    await flush();
    expect(apiMock).toHaveBeenCalled();
    expect(jumped).toHaveLength(1);
    expect(turnsAtJump[0]).toBeLessThanOrEqual(3000);
  });

  it("serves the newest of two jumps; the superseded one never lands", async () => {
    const waits: ((v: unknown) => void)[] = [];
    apiMock.mockImplementation((url: string) => new Promise((r) => {
      const before = Number(/before=(\d+)/.exec(url)![1]);
      const limit = Number(/limit=(\d+)/.exec(url)![1]);
      const lo = Math.max(0, before - limit);
      waits.push(() => r({ messages: [{ idx: lo }], firstLine: lo, hasMore: lo > 0 }));
    }));
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true })); // A
    act(() => requestJump("s", { atBottom: false, idx: 100, offset: 0, near: true })); // B, while A loads
    await act(async () => waits.shift()!(null));
    await flush();
    while (waits.length) { await act(async () => waits.shift()!(null)); await flush(); }
    expect(jumped.map((m) => (m as { idx: number }).idx)).toEqual([100]);
  });

  it("serves a jump that arrived while the Load earlier button's page was in flight", async () => {
    let release: (v: unknown) => void = () => {};
    apiMock.mockImplementationOnce(() => new Promise((r) => { release = r; }));
    mount({ session: "s" });
    act(() => { void older!(); });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    expect(jumped).toHaveLength(0);
    await act(async () => release({ messages: [{ idx: 600 }], firstLine: 600, hasMore: true }));
    await flush();
    expect(jumped).toHaveLength(1);
  });

  it("drops a jump when the session changes while its page loads", async () => {
    let release: (v: unknown) => void = () => {};
    apiMock.mockImplementationOnce(() => new Promise((r) => { release = r; }));
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    act(() => root!.render(<Harness session="t" />));
    await act(async () => release({ messages: [{ idx: 200 }], firstLine: 200, hasMore: true }));
    await flush();
    expect(jumped).toHaveLength(0);
    expect(toasts).toEqual([]);
  });

  it("drops the jump when the reader moved without any input event (scrollbar drag)", async () => {
    let release: (v: unknown) => void = () => {};
    apiMock.mockImplementationOnce(() => new Promise((r) => { release = r; }));
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    moved = true;
    await act(async () => release({ messages: [{ idx: 250 }], firstLine: 250, hasMore: true }));
    await flush();
    expect(jumped).toHaveLength(0);
  });

  it("serves the new session's jump although the old session's fetch is still in flight", async () => {
    let release: (v: unknown) => void = () => {};
    apiMock.mockImplementationOnce(() => new Promise((r) => { release = r; }));
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    act(() => root!.render(<Harness session="t" />));
    act(() => requestJump("t", { atBottom: false, idx: 300, offset: 0, near: true }));
    await flush();
    await act(async () => release({ messages: [{ idx: 200 }], firstLine: 200, hasMore: true }));
    await flush();
    expect(apiMock.mock.calls.some((c) => String(c[0]).includes("sessions/t/"))).toBe(true);
    expect(jumped).toHaveLength(1);
  });

  it("applies a jump whose hit the button's page brought in", async () => {
    let release: (v: unknown) => void = () => {};
    apiMock.mockImplementationOnce(() => new Promise((r) => { release = r; }));
    mount({ session: "s" });
    act(() => { void older!(); });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    await act(async () => release({ messages: [{ idx: 0 }], firstLine: 0, hasMore: false }));
    await flush();
    expect(jumped).toHaveLength(1);
  });

  it("does nothing for a hit inside the window", async () => {
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 1200, offset: 0, near: true }));
    await flush();
    expect(apiMock).not.toHaveBeenCalled();
    expect(jumped).toHaveLength(1); // harmless repeat of what the scroller's own listener did
  });

  it("waits for the first window when the session is opened by the jump", async () => {
    requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true });
    mount({ session: "s", loaded: false });
    await flush();
    expect(apiMock).not.toHaveBeenCalled();
    act(() => root!.render(<Harness session="s" loaded />));
    await flush();
    expect(apiMock).toHaveBeenCalledTimes(1);
    expect(jumped).toHaveLength(1);
  });

  it("refuses a far-away hit with a notice and no request", async () => {
    mount({ session: "s", first: 20000 });
    act(() => requestJump("s", { atBottom: false, idx: 100, offset: 0, near: true }));
    await flush();
    expect(apiMock).not.toHaveBeenCalled();
    expect(toasts).toHaveLength(1);
    expect(jumped).toHaveLength(0);
  });

  it("gives up with a notice when a page fails", async () => {
    apiMock.mockResolvedValue({ error: "boom" });
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    await flush();
    expect(apiMock).toHaveBeenCalledTimes(1);
    expect(toasts).toHaveLength(1);
    expect(jumped).toHaveLength(0);
  });

  it("drops the jump when the reader takes over while the page loads", async () => {
    let release: (v: unknown) => void = () => {};
    apiMock.mockImplementation(() => new Promise((r) => { release = r; }));
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 300, offset: 0, near: true }));
    inputSeq.current++; // a wheel / touch / key
    await act(async () => release({ messages: [{ idx: 200 }], firstLine: 200, hasMore: true }));
    await flush();
    expect(jumped).toHaveLength(0);
  });

  it("ignores another session's hit", async () => {
    mount({ session: "s" });
    act(() => requestJump("other", { atBottom: false, idx: 300, offset: 0, near: true }));
    await flush();
    expect(apiMock).not.toHaveBeenCalled();
  });
});
