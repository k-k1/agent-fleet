// Keeps xterm.js from answering terminal queries in output that belongs to a socket the
// pane has already replaced.
//
// xterm.js answers a query (device attributes, cursor/status reports, color and setting
// queries) by firing onData, which the pane sends to the PTY as input. term.reset() does
// not drop bytes still queued in xterm's write buffer, so output from the previous socket
// can be parsed after the new one is open, and its answers then reach the new tmux client.
// tmux swallows only the reply it asked for and hands any other to the pane, where a shell
// prints it as typed text ("0;276;0c" for DA2, #3337).

import type { IDisposable, Terminal } from "@xterm/xterm";

export interface ReplyGate {
  /** True while bytes written before the latest fence are still being parsed. */
  muted: boolean;
  gen: number;
}

export function newReplyGate(): ReplyGate {
  return { muted: false, gen: 0 };
}

// fenceReplies mutes replies until everything already written to the terminal has been
// parsed. Call it when the pane switches to a new socket, before that socket's first
// write: xterm parses writes in order, so the empty write's callback marks the point
// where the new socket's output begins. A later fence supersedes an earlier one.
export function fenceReplies(term: Terminal, gate: ReplyGate): void {
  const gen = ++gate.gen;
  gate.muted = true;
  term.write("", () => {
    if (gate.gen === gen) gate.muted = false;
  });
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
  // Color queries ("?" in place of a color). Setting a color is not a query and still applies.
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
