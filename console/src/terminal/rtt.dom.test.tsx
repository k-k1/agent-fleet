// 端末の往復時間（RTT）計測の回帰。
//
// この数字は「打鍵が遅い」という報告に対して製品側が出せる唯一の観測量なので、静かに
// 壊れると調査手段そのものが消える。押さえるのは 3 点:
//   ① pong は heartbeat の生存判定に加えて RTT サンプルにもなる
//   ② ping は 1 本だけ in-flight（Agent の pong は定数フレームで相関できないため、
//      2 本目を出すと「どちらの往復か」が言えなくなる）
//   ③ ソケットを張り替えたら窓は捨てる（前の経路の数字は今の経路を語らない）
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
// autoPong: ping を受けたら次のマイクロタスクで pong を返す（probe の逐次計測用）。
let autoPong = false;

class FakeWS implements FakeSocket {
  // 実物と同じ定数を持たせる: term.ts は readyState を WebSocket.OPEN 等と比較するので、
  // 定数の無い偽物だと「常に未接続」に見えて ping が 1 本も出ない。
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

// jsdom にはレイアウトが無く、xterm が rAF 内で走らせる viewport 同期は
// RenderService の dimensions を読んで落ちる。ここで見たいのはソケット側だけなので
// rAF は握り潰す（term.ts 側の rAF も描画の作り直しだけで、RTT には関係しない）。
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

    // まだ open していないので測定は始まっていない。
    expect(terminalRtt("r1")).toBeNull();

    // open 直後に 1 本 ping を出す（アタッチした瞬間から数字が出るように）。
    ws.open();
    expect(ws.pings()).toHaveLength(1);
    expect(terminalRtt("r1")).toBeNull(); // 応答前

    ws.pong();
    const first = terminalRtt("r1");
    expect(first).not.toBeNull();
    expect(first!.n).toBe(1);
    expect(first!.last).toBeGreaterThanOrEqual(0);

    // ★ in-flight が無い状態の pong はサンプルにしない。ここを数えると「往復して
    // いない pong」で RTT が 0 に薄まり、遅延が数字の上から消える。
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

    // 経路が変わる（張り替え）＝前の窓は今の接続について何も語らない。
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
    // open 直後の 1 本を先に決着させる。in-flight が残ったまま probe を始めると、
    // 最初のサンプルはその ping の往復で埋まる（＝正しい振る舞いだが、本数の
    // 検証にならない）。
    await new Promise((r) => setTimeout(r, 0));

    const before = ws.pings().length;
    const stats = await probeTerminalRtt("r1", 4, 0);
    expect(stats).not.toBeNull();
    expect(stats!.n).toBe(4);
    // 逐次＝要求した本数ぶんだけ ping が増える（並行に撃たない）。
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
