// Keeps xterm.js from answering terminal queries that no live program asked.
//
// xterm.js answers a query (device attributes, cursor/status reports, color and setting
// queries) by firing onData, which the pane sends to the PTY as input. tmux keeps only the
// reply it asked for itself and hands any other to the pane, where a shell prints it as
// typed text ("0;276;0c" for DA2, #3337). Two kinds of output carry queries nobody is
// waiting for:
//   - the recorded history the Agent replays ahead of the live attach (announced by a
//     {"type":"replay","bytes":N} text frame, workspace/agent/terminal.go writeReplay);
//   - bytes from a replaced socket, which term.reset() leaves queued in xterm's write
//     buffer, so they are parsed after the new socket is open.
// While either is being parsed the gate is muted, and parser handlers swallow the queries
// instead of letting xterm answer them.

import type { IDisposable, Terminal } from "@xterm/xterm";

export interface ReplyGate {
  /** True while output nobody is waiting on is being parsed. */
  muted: boolean;
  gen: number;
  /** Replay bytes announced by the Agent and not yet received. */
  replayLeft: number;
}

export function newReplyGate(): ReplyGate {
  return { muted: false, gen: 0, replayLeft: 0 };
}

// CAN aborts any sequence the parser is still inside (CSI, OSC, DCS) without executing it,
// and does nothing in the ground state. Ending each muted stretch with it means a sequence
// left open there cannot be completed by the next live bytes and answered after unmuting.
const CANCEL = "\x18";

// fenceReplies mutes replies until everything already written to the terminal has been
// parsed. xterm parses writes in order, so the fence's callback marks where the next
// output begins. A later mute or fence supersedes an earlier one.
export function fenceReplies(term: Terminal, gate: ReplyGate): void {
  const gen = ++gate.gen;
  gate.muted = true;
  term.write(CANCEL, () => {
    if (gate.gen === gen) gate.muted = false;
  });
}

// startSocket is called for a new socket, after term.reset(): the old socket's queued
// output stays muted.
export function startSocket(term: Terminal, gate: ReplyGate): void {
  gate.replayLeft = 0;
  fenceReplies(term, gate);
}

// startReplay is called on the Agent's replay marker: the next `bytes` bytes are replay.
export function startReplay(gate: ReplyGate, bytes: number): void {
  if (!(bytes > 0)) return;
  gate.gen++;
  gate.muted = true;
  gate.replayLeft = bytes;
}

// writeOutput writes socket output, and fences the replay once its last byte is written.
export function writeOutput(
  term: Terminal,
  gate: ReplyGate,
  data: Uint8Array,
): void {
  term.write(data);
  if (gate.replayLeft > 0) {
    gate.replayLeft -= data.byteLength;
    if (gate.replayLeft <= 0) {
      gate.replayLeft = 0;
      fenceReplies(term, gate);
    }
  }
}

// XTWINOPS operations that ask for a report; the others change the window.
const WINOPS_REPORTS = new Set([11, 13, 14, 15, 16, 18, 19, 20, 21]);

// installReplyGate registers parser handlers that swallow the queries xterm.js answers
// while the gate is muted, and otherwise defer to xterm's own handlers (returning false
// passes the sequence on to the built-in handler).
export function installReplyGate(
  term: Terminal,
  gate: ReplyGate,
): IDisposable[] {
  const p = term.parser;
  const whenMuted = () => gate.muted;
  const disposables = [
    p.registerCsiHandler({ final: "c" }, whenMuted), // DA1
    p.registerCsiHandler({ prefix: ">", final: "c" }, whenMuted), // DA2
    p.registerCsiHandler({ final: "n" }, whenMuted), // DSR / CPR
    p.registerCsiHandler({ prefix: "?", final: "n" }, whenMuted), // DECDSR
    p.registerCsiHandler({ intermediates: "$", final: "p" }, whenMuted), // DECRQM, ANSI modes
    p.registerCsiHandler(
      { prefix: "?", intermediates: "$", final: "p" },
      whenMuted,
    ), // DECRQM, DEC modes
    p.registerCsiHandler(
      { final: "t" },
      (params) => gate.muted && WINOPS_REPORTS.has(Number(params[0])),
    ),
    p.registerDcsHandler({ intermediates: "$", final: "q" }, whenMuted), // DECRQSS
  ];
  // Color queries ("?" in place of a color). A sequence that only sets colors still applies;
  // one that both sets and queries is swallowed whole, as xterm cannot apply half of it.
  for (const id of [4, 10, 11, 12]) {
    disposables.push(
      p.registerOscHandler(
        id,
        (data) => gate.muted && data.split(";").includes("?"),
      ),
    );
  }
  return disposables;
}
