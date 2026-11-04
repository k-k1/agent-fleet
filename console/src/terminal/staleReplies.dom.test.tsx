// Regression guard for #3337: answers to terminal queries in a replaced socket's output must
// not reach the new socket's PTY, where tmux hands them to the shell as typed text.
import { describe, it, expect, afterEach, vi } from "vitest";
import { Terminal } from "@xterm/xterm";
import { ensureTerm, attach, disposeTerm } from "./term.ts";
import {
  fenceReplies,
  installReplyGate,
  newReplyGate,
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
  // Built from the global Uint8Array: a TextEncoder buffer comes from another realm under
  // jsdom and fails term.ts's `instanceof ArrayBuffer`, so it would never be written.
  output(s: string) {
    this.onmessage?.({
      data: Uint8Array.from(s, (c) => c.charCodeAt(0)).buffer,
    });
  }
  inputs() {
    return this.sent
      .map((s) => JSON.parse(s))
      .filter((m) => m.type === "input")
      .map((m) => m.data as string);
  }
}

// Resolves once everything written to term so far has been parsed.
const parsed = (term: Terminal) => new Promise<void>((r) => term.write("", r));

describe("replies to a replaced socket's output", () => {
  afterEach(() => {
    disposeTerm("q1");
    FakeWS.all = [];
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("are not sent to the new socket, while its own queries still are", async () => {
    // jsdom has no layout; the rAF work in term.ts only repaints.
    vi.stubGlobal("requestAnimationFrame", () => 0);
    vi.stubGlobal("cancelAnimationFrame", () => {});
    vi.stubGlobal("WebSocket", FakeWS);
    const el = document.createElement("div");
    document.body.appendChild(el);
    const term = ensureTerm("q1", el);

    attach("q1", "sfake");
    const old = FakeWS.all[0];
    old.open();
    // Queued, not yet parsed: xterm parses writes on a later tick.
    old.output("history\x1b[>c\x1b[6n");
    attach("q1", "sfake");
    const cur = FakeWS.all[1];
    cur.open();
    await parsed(term);
    expect(cur.inputs()).toEqual([]);
    expect(old.inputs()).toEqual([]);

    cur.output("\x1b[>c");
    await parsed(term);
    expect(cur.inputs()).toEqual([DA2_REPLY]);
  });
});

describe("reply gate", () => {
  function setup() {
    const term = new Terminal({ allowProposedApi: true });
    const gate = newReplyGate();
    installReplyGate(term, gate);
    const sent: string[] = [];
    term.onData((d) => sent.push(d));
    return { term, gate, sent };
  }

  it("answers queries when no fence is pending", async () => {
    const { term, sent } = setup();
    // OSC color queries are left out: an unopened terminal has no theme to answer them from.
    term.write("\x1b[>c\x1b[c\x1b[6n");
    await parsed(term);
    expect(sent).toHaveLength(3);
    expect(sent[0]).toBe(DA2_REPLY);
  });

  it("swallows every query written before the fence and none after it", async () => {
    const { term, gate, sent } = setup();
    term.write(
      "\x1b[>c\x1b[c\x1b[5n\x1b[6n\x1b[?6n\x1b[?2004$p\x1b[4$p\x1b[18t\x1b]4;1;?\x07\x1b]11;?\x07\x1bP$qm\x1b\\",
    );
    fenceReplies(term, gate);
    term.write("\x1b[>c");
    await parsed(term);
    expect(sent).toEqual([DA2_REPLY]);
    expect(gate.muted).toBe(false);
  });

  it("stays muted until the latest fence when fences overlap", async () => {
    const { term, gate, sent } = setup();
    fenceReplies(term, gate);
    term.write("\x1b[>c");
    fenceReplies(term, gate);
    term.write("\x1b[>c");
    await parsed(term);
    expect(sent).toEqual([DA2_REPLY]);
  });
});
