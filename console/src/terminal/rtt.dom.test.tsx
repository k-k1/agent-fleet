// Regression guard for the terminal round-trip time (RTT) measurement.
//
// This number is the only observation the product can offer against a "typing is slow" report,
// so if it breaks silently the means of investigation disappears with it. Three points are
// pinned:
//   1. a pong is an RTT sample as well as the heartbeat's liveness signal;
//   2. only one ping is in flight at a time - the Agent's pong is a constant frame and cannot
//      be correlated, so a second ping makes it impossible to say which round trip was
//      measured;
//   3. re-attaching the socket discards the window: numbers from the previous route say
//      nothing about the current one.
import { describe, it, expect, afterEach, vi } from "vitest";
import { ensureTerm, attach, disposeTerm, terminalRtt, probeTerminalRtt } from "./term.ts";

interface FakeSocket {
  readyState: number;
  binaryType: string;
  sent: string[];
  onopen: (() => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number; reason: string }) => void) | null;
  onerror: (() => void) | null;
  close(): void;
  send(d: string): void;
}

let sockets: FakeSocket[] = [];
// autoPong: answer a ping with a pong on the next microtask, for the sequential probe.
let autoPong = false;

class FakeWS implements FakeSocket {
  // Carry the same constants as the real class: term.ts compares readyState against
  // WebSocket.OPEN and friends, so a fake without them looks permanently disconnected and
  // no ping is ever sent.
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = 0;
  binaryType = "";
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() {
    sockets.push(this);
  }
  send(d: string) {
    this.sent.push(d);
    if (autoPong && d.includes('"ping"')) queueMicrotask(() => this.pong());
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  pong() {
    this.onmessage?.({ data: '{"type":"pong"}' });
  }
  pings() {
    return this.sent.filter((s) => s.includes('"ping"'));
  }
}

// jsdom has no layout, so the viewport sync xterm runs inside rAF throws while reading
// RenderService dimensions. Only the socket side matters here, so rAF is swallowed; the rAF
// in term.ts only rebuilds the rendering and has nothing to do with RTT.
function stubRaf() {
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
}

function mount(paneId: string) {
  const el = document.createElement("div");
  document.body.appendChild(el);
  ensureTerm(paneId, el);
  return el;
}

describe("terminal round-trip measurement", () => {
  afterEach(() => {
    for (const id of ["r1"]) disposeTerm(id);
    sockets = [];
    autoPong = false;
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("turns the heartbeat pong into a sample, and only for an outstanding ping", () => {
    stubRaf();
    vi.stubGlobal("WebSocket", FakeWS);
    const el = mount("r1");
    attach("r1", "sfake");
    const ws = sockets[0] as unknown as FakeWS;

    // Not open yet, so no measurement has started.
    expect(terminalRtt("r1")).toBeNull();

    // One ping right after open, so a number exists from the moment of attaching.
    ws.open();
    expect(ws.pings()).toHaveLength(1);
    expect(terminalRtt("r1")).toBeNull(); // before the reply

    ws.pong();
    const first = terminalRtt("r1");
    expect(first).not.toBeNull();
    expect(first!.n).toBe(1);
    expect(first!.last).toBeGreaterThanOrEqual(0);

    // A pong with nothing in flight is not a sample. Counting it would dilute the RTT toward
    // zero with pongs that never made a round trip, erasing the latency from the number.
    ws.pong();
    ws.pong();
    expect(terminalRtt("r1")!.n).toBe(1);

    el.remove();
  });

  it("re-attaching drops the previous socket's window", async () => {
    stubRaf();
    vi.stubGlobal("WebSocket", FakeWS);
    const el = mount("r1");
    attach("r1", "sfake");
    const first = sockets[0] as unknown as FakeWS;
    first.open();
    first.pong();
    expect(terminalRtt("r1")!.n).toBe(1);

    // The route changed (re-attach), so the previous window says nothing about this connection.
    attach("r1", "sfake");
    expect(terminalRtt("r1")).toBeNull();
    expect(sockets).toHaveLength(2);

    el.remove();
  });

  it("probes a burst sequentially over the live socket", async () => {
    stubRaf();
    vi.stubGlobal("WebSocket", FakeWS);
    autoPong = true;
    const el = mount("r1");
    attach("r1", "sfake");
    const ws = sockets[0] as unknown as FakeWS;
    ws.open();
    // Settle the ping sent at open first. Starting the probe with one still in flight fills
    // the first sample with that ping's round trip - correct behaviour, but it would stop this
    // from testing the ping count.
    await new Promise((r) => setTimeout(r, 0));

    const before = ws.pings().length;
    const stats = await probeTerminalRtt("r1", 4, 0);
    expect(stats).not.toBeNull();
    expect(stats!.n).toBe(4);
    // Sequential: exactly as many new pings as were requested, never fired in parallel.
    expect(ws.pings().length - before).toBe(4);
    expect(stats!.max).toBeGreaterThanOrEqual(stats!.med);

    el.remove();
  });

  it("returns null when the pane has no live socket", async () => {
    stubRaf();
    vi.stubGlobal("WebSocket", FakeWS);
    const el = mount("r1");
    expect(await probeTerminalRtt("r1", 2, 0)).toBeNull();
    el.remove();
  });
});
