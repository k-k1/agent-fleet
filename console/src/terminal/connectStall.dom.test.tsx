// The connect-stall watchdog (#4432). A socket still CONNECTING past its window is torn down
// and re-opened, which clears the session-boot hang. The window must grow while connects keep
// being replaced while still connecting: on a ~1 s round-trip link a healthy connect takes over 3 s, and a fixed
// 3.5 s window cut every one of them off just before it completed.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { Terminal } from "@xterm/xterm";
import { ensureTerm, attach, ensureAttached, reconnect, disposeTerm, connStallWindow } from "./term.ts";

let sockets: FakeWS[] = [];
class FakeWS {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = 0;
  binaryType = "";
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    sockets.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  fail() {
    this.readyState = 3;
    this.onclose?.({ code: 1006, reason: "" });
  }
}

const last = () => sockets[sockets.length - 1];

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Terminal.prototype, "write").mockImplementation(() => {});
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("WebSocket", FakeWS);
  const el = document.createElement("div");
  document.body.appendChild(el);
  ensureTerm("cs1", el);
});

afterEach(() => {
  disposeTerm("cs1");
  sockets = [];
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("connStallWindow", () => {
  it("starts at 3 s, doubles, and stops at 30 s", () => {
    expect([0, 1, 2, 3, 4, 5, 50].map(connStallWindow)).toEqual([3000, 6000, 12000, 24000, 30000, 30000, 30000]);
  });
});

describe("the connect-stall watchdog", () => {
  it("waits longer each time a connect never opens", () => {
    attach("cs1", "sa");
    expect(sockets).toHaveLength(1);

    vi.advanceTimersByTime(3500);
    expect(sockets).toHaveLength(2); // the first retry is as quick as before

    vi.advanceTimersByTime(6000);
    expect(sockets).toHaveLength(2); // a slow connect gets more time on the next try
    vi.advanceTimersByTime(500);
    expect(sockets).toHaveLength(3);

    vi.advanceTimersByTime(12_000);
    expect(sockets).toHaveLength(3);
    vi.advanceTimersByTime(500);
    expect(sockets).toHaveLength(4);
  });

  it("lets a slow connect finish instead of cutting it off at 3.5 s", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500); // the first try was cut off
    const slow = last();
    vi.advanceTimersByTime(4500); // 4.5 s into the second, past the old fixed window
    expect(last()).toBe(slow);
    slow.open();
    vi.advanceTimersByTime(10_000); // past where its watchdog would have fired (the heartbeat is another rule)
    expect(last()).toBe(slow);
  });

  it("starts over at the short window once a socket opened", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500);
    last().open();
    attach("cs1", "sa", true); // a drop after it opened, re-attached
    const n = sockets.length;
    vi.advanceTimersByTime(3500);
    expect(sockets).toHaveLength(n + 1);
  });

  it("starts over at the short window for another session", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500); // the second try to sa is under way, with a 6 s window
    attach("cs1", "sb");
    const n = sockets.length;
    vi.advanceTimersByTime(3500);
    expect(sockets).toHaveLength(n + 1);
  });

  it("does not let the retry ladder cut a connect off inside its window", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500);
    const second = last();
    vi.advanceTimersByTime(4000); // inside the 6 s window
    ensureAttached("cs1", "sa");
    expect(last()).toBe(second);
    vi.advanceTimersByTime(2100); // past it (the watchdog itself fires at 6.5 s)
    ensureAttached("cs1", "sa");
    expect(last()).not.toBe(second);
  });

  it("starts over at the short window after a connect that failed by itself", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500); // one cut-off connect: the next window would be 6 s
    last().fail(); // refused: says nothing about the window
    reconnect("cs1");
    const n = sockets.length;
    vi.advanceTimersByTime(3500);
    expect(sockets).toHaveLength(n + 1);
  });

  it("lets reconnect leave a connect inside its window alone", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500);
    const second = last();
    vi.advanceTimersByTime(4000);
    reconnect("cs1");
    expect(last()).toBe(second);
    vi.advanceTimersByTime(2100);
    reconnect("cs1");
    expect(last()).not.toBe(second);
  });

  it("fires the current socket's watchdog once, at its own window", () => {
    attach("cs1", "sa");
    vi.advanceTimersByTime(3500);
    vi.advanceTimersByTime(4000);
    ensureAttached("cs1", "sa"); // inside the window: nothing
    const n = sockets.length;
    vi.advanceTimersByTime(2499); // only the current socket's watchdog may fire, at 6.5 s
    expect(sockets).toHaveLength(n);
    vi.advanceTimersByTime(2);
    expect(sockets).toHaveLength(n + 1);
  });

  it("stops growing at 30 s", () => {
    attach("cs1", "sa");
    for (const w of [3000, 6000, 12000, 24000, 30000]) vi.advanceTimersByTime(w + 500);
    const n = sockets.length;
    vi.advanceTimersByTime(30_499);
    expect(sockets).toHaveLength(n);
    vi.advanceTimersByTime(2);
    expect(sockets).toHaveLength(n + 1);
  });

  it("ignores a late open of a socket it already replaced", () => {
    attach("cs1", "sa");
    const first = last();
    const onopen = first.onopen; // queued before the replacement
    vi.advanceTimersByTime(3500);
    const second = last();
    first.readyState = 1;
    onopen?.();
    vi.advanceTimersByTime(6000); // the second's watchdog still fires at 6.5 s
    vi.advanceTimersByTime(500);
    expect(last()).not.toBe(second);
  });
});
