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

function Harness({ session, loaded = true, first = 1000 }: { session: string; loaded?: boolean; first?: number }) {
  const [turns, setTurns] = useState<{ idx: number }[]>([{ idx: first }]);
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
    capturePrependAnchor: () => {},
    applyPrependAdjust: () => {},
    jumpTo: (m: unknown) => { jumped.push(m); turnsAtJump.push(turns[0].idx); return true; },
  };
  useOlderHistory({ session, st: st as never, scroll: scroll as never, toast: (m) => toasts.push(m) });
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
  jumped = []; toasts = []; turnsAtJump = [];
  inputSeq = { current: 0 };
  // The page before `before`: `limit` positions, the oldest at before-limit.
  apiMock.mockImplementation(async (url: string) => {
    const before = Number(/before=(\d+)/.exec(url)![1]);
    const limit = Number(/limit=(\d+)/.exec(url)![1]);
    const lo = Math.max(0, before - limit);
    return { messages: [{ idx: lo }], firstLine: lo, hasMore: lo > 0 };
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

  it("does nothing for a hit inside the window", async () => {
    mount({ session: "s" });
    act(() => requestJump("s", { atBottom: false, idx: 1200, offset: 0, near: true }));
    await flush();
    expect(apiMock).not.toHaveBeenCalled();
    expect(jumped).toHaveLength(0);
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
