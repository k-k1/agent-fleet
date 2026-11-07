// The pane must say why a live PTY socket ended. The Agent closes with code 1000 and a reason:
// "session stopped" (halted) or "pty closed" (shell exit / detach). Neither is a drop, so
// neither may flag the pane for reconnect; both must print a notice instead of freezing silently.
import { describe, it, expect, afterEach, vi } from "vitest";
import { Terminal } from "@xterm/xterm";
import { ensureTerm, attach, disposeTerm } from "./term.ts";
import { t as tr } from "../lib/i18n/index.ts";

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
  constructor() {
    sockets.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
}

function closeWith(reason: string, code = 1000): string[] {
  const writes: string[] = [];
  const spy = vi.spyOn(Terminal.prototype, "write").mockImplementation((d: string | Uint8Array) => {
    if (typeof d === "string") writes.push(d);
  });
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("WebSocket", FakeWS);
  const el = document.createElement("div");
  document.body.appendChild(el);
  ensureTerm("pc1", el);
  attach("pc1", "sfake");
  writes.length = 0;
  sockets[0].onclose?.({ code, reason });
  spy.mockRestore();
  return writes;
}

describe("PTY close notice", () => {
  afterEach(() => {
    disposeTerm("pc1");
    sockets = [];
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("prints the stopped notice for 'session stopped'", () => {
    expect(closeWith("session stopped").join("")).toContain(tr("onb.term_session_stopped"));
  });

  it("prints an ended notice for 'pty closed' instead of freezing", () => {
    const w = closeWith("pty closed").join("");
    expect(w).toContain(tr("onb.term_pty_closed"));
    expect(w).not.toContain(tr("onb.term_session_stopped"));
    expect(w).not.toContain(tr("onb.term_disconnected"));
  });

  it("stays silent for a finished history replay (no reason)", () => {
    expect(closeWith("")).toEqual([]);
  });
});
