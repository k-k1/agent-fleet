// Regression guard for #3337: answers to terminal queries in replayed history, or in a
// replaced socket's output, must not reach the live PTY, where tmux hands them to the shell
// as typed text. Only the live output's own queries get answers.
import { describe, it, expect, afterEach, vi } from "vitest";
import { Terminal } from "@xterm/xterm";
import { ensureTerm, attach, disposeTerm } from "./term.ts";
import {
  installReplyGate,
  newReplyGate,
  startReplay,
  startSocket,
  writeOutput,
} from "./staleReplies.ts";

const DA2_REPLY = "\x1b[>0;276;0c";

class FakeWS {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static all: FakeWS[] = [];
  readyState = 0;
  binaryType = "";
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() {
    FakeWS.all.push(this);
  }
  send(d: string) {
    this.sent.push(d);
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  output(s: string) {
    this.onmessage?.({ data: bytes(s).buffer });
  }
  replay(history: string) {
    this.onmessage?.({
      data: JSON.stringify({
        type: "replay",
        bytes: bytes(history).byteLength,
      }),
    });
    this.output(history);
  }
  inputs() {
    return this.sent
      .map((s) => JSON.parse(s))
      .filter((m) => m.type === "input")
      .map((m) => m.data as string);
  }
}

// Real UTF-8, copied into the global Uint8Array: TextEncoder's own buffer comes from another
// realm under jsdom and fails term.ts's `instanceof ArrayBuffer`, so it would never be written.
const bytes = (s: string) => Uint8Array.from(new TextEncoder().encode(s));

// Resolves once everything written to term so far has been parsed.
const parsed = (term: Terminal) => new Promise<void>((r) => term.write("", r));

describe("terminal query answers on attach", () => {
  afterEach(() => {
    disposeTerm("q1");
    FakeWS.all = [];
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  function mount() {
    // jsdom has no layout; the rAF work in term.ts only repaints.
    vi.stubGlobal("requestAnimationFrame", () => 0);
    vi.stubGlobal("cancelAnimationFrame", () => {});
    vi.stubGlobal("WebSocket", FakeWS);
    const el = document.createElement("div");
    document.body.appendChild(el);
    return ensureTerm("q1", el);
  }

  it("are not given for the replayed history, only for live output", async () => {
    const term = mount();
    attach("q1", "sfake");
    const ws = FakeWS.all[0];
    ws.open();
    ws.replay("$ vim\x1b[>c\x1b[6n\x1b[c\x1b[?2004$p");
    ws.output("\x1b[>c"); // tmux's own query on attach
    await parsed(term);
    expect(ws.inputs()).toEqual([DA2_REPLY]);
  });

  it("are not given for a sequence the history leaves open", async () => {
    const term = mount();
    attach("q1", "sfake");
    const ws = FakeWS.all[0];
    ws.open();
    ws.replay("$ \x1bP$qm"); // cut off: the next ESC would end the DECRQSS and answer it
    ws.output("\x1b[>c");
    await parsed(term);
    expect(ws.inputs()).toEqual([DA2_REPLY]);
  });

  it("count the replay across several frames", async () => {
    const term = mount();
    attach("q1", "sfake");
    const ws = FakeWS.all[0];
    ws.open();
    const history = "one\x1b[>c two\x1b[6n";
    ws.onmessage?.({
      data: JSON.stringify({
        type: "replay",
        bytes: bytes(history).byteLength,
      }),
    });
    ws.output(history.slice(0, 6));
    ws.output(history.slice(6));
    ws.output("\x1b[6n");
    await parsed(term);
    expect(ws.inputs()).toHaveLength(1);
    expect(ws.inputs()[0]).toMatch(/^\x1b\[\d+;\d+R$/);
  });

  it("are not given for a replaced socket's queued output", async () => {
    const term = mount();
    attach("q1", "sfake");
    const old = FakeWS.all[0];
    old.open();
    // Queued, not yet parsed: xterm parses writes on a later tick. It ends inside a DCS
    // that the new socket's first ESC would otherwise complete.
    old.output("history\x1b[>c\x1b[6n\x1bP$qm");
    attach("q1", "sfake");
    const cur = FakeWS.all[1];
    cur.open();
    cur.output("\x1b[>c");
    await parsed(term);
    expect(old.inputs()).toEqual([]);
    expect(cur.inputs()).toEqual([DA2_REPLY]);
  });

  it("are given as before when the Agent sends no replay marker", async () => {
    const term = mount();
    attach("q1", "sfake");
    const ws = FakeWS.all[0];
    ws.open();
    await parsed(term);
    ws.output("\x1b[>c");
    await parsed(term);
    expect(ws.inputs()).toEqual([DA2_REPLY]);
  });
});

describe("reply gate", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  // An opened terminal: an unopened one has no theme and never answers color queries, so a
  // color-query check on it would pass with the gate switched off.
  function setup() {
    vi.stubGlobal("requestAnimationFrame", () => 0);
    vi.stubGlobal("cancelAnimationFrame", () => {});
    const el = document.createElement("div");
    document.body.appendChild(el);
    const term = new Terminal({ allowProposedApi: true });
    term.open(el);
    const gate = newReplyGate();
    installReplyGate(term, gate);
    const sent: string[] = [];
    term.onData((d) => sent.push(d));
    return { term, gate, sent };
  }

  // Each query xterm.js answers, including the spellings a byte matcher misses.
  const QUERIES: [string, string][] = [
    ["DA2", "\x1b[>c"],
    ["DA2 with 00", "\x1b[>00c"],
    ["DA1", "\x1b[c"],
    ["DSR", "\x1b[5n"],
    ["CPR with 06", "\x1b[06n"],
    ["CPR with LF inside", "\x1b[6\nn"],
    ["DECDSR", "\x1b[?6n"],
    ["DECRQM DEC", "\x1b[?2004$p"],
    ["DECRQM ANSI", "\x1b[4$p"],
    ["OSC 4 query", "\x1b]4;1;?\x07"],
    ["OSC 011 query", "\x1b]011;?\x07"],
    ["OSC 10 query, ST", "\x1b]10;?\x1b\\"],
    ["DECRQSS", "\x1bP$qm\x1b\\"],
    ["DECRQSS with 0", "\x1bP0$qm\x1b\\"],
    ["C1 CSI DA2", "\u009b>c"],
  ];

  it.each(QUERIES)(
    "%s is answered when not muted, and not in a replay",
    async (_, q) => {
      const { term, gate, sent } = setup();
      writeOutput(term, gate, bytes(q));
      await parsed(term);
      expect(sent.length).toBeGreaterThan(0); // positive control: xterm does answer it

      sent.length = 0;
      startReplay(gate, bytes(q).byteLength);
      writeOutput(term, gate, bytes(q));
      writeOutput(term, gate, bytes("\x1b[>c"));
      await parsed(term);
      expect(sent).toEqual([DA2_REPLY]);
      expect(gate.muted).toBe(false);
    },
  );

  it("stays muted until the latest fence when socket switches overlap", async () => {
    const { term, gate, sent } = setup();
    startSocket(term, gate);
    term.write("\x1b[>c");
    startSocket(term, gate);
    term.write("\x1b[>c");
    await parsed(term);
    expect(sent).toEqual([DA2_REPLY]);
  });

  it.each([
    ["zero", 0],
    ["NaN", NaN],
    ["negative", -5],
    ["fraction", 2.5],
    ["infinite (JSON 1e400)", JSON.parse('{"n":1e400}').n],
    ["beyond the history cap", 64 << 20],
    ["a string", "12"],
    ["missing", undefined],
  ])("ignores a replay marker whose count is %s", async (_, n) => {
    const { term, gate, sent } = setup();
    startReplay(gate, n);
    expect(gate.muted).toBe(false);
    writeOutput(term, gate, bytes("\x1b[>c"));
    await parsed(term);
    expect(sent).toEqual([DA2_REPLY]);
  });

  it("recovers on the next socket when a replay is cut off", async () => {
    const { term, gate, sent } = setup();
    startReplay(gate, 1000);
    writeOutput(term, gate, bytes("partial\x1b[>c"));
    startSocket(term, gate);
    writeOutput(term, gate, bytes("\x1b[>c"));
    await parsed(term);
    expect(sent).toEqual([DA2_REPLY]);
  });
});
