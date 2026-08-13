// transcript/model — the pure transcript pipeline, with no React and no I/O.
//
// Raw turns (GET …/messages) become renderable blocks in three steps:
//
//   coalesceUserActions  recover `!` shell runs and `/` commands from the system-tagged
//                        user turns claude logs them as, before isNoise drops them
//   groupTurns           fold consecutive same-role turns into blocks, dropping noise
//   foldParts            coalesce runs of tool traces inside a block
//
// Both the mirror (owner) and the shared-session view (recipient) run exactly this
// pipeline, so the two reads of a conversation can never drift apart. Everything here is
// a pure function of its input — which is also why it is the part that carries tests
// (model.test.ts) rather than the components.

import type { FoldItem, Group, Part, Turn } from "./types.ts";
import { carryEnd, endOf } from "../turnTime.ts";

// System-injected user lines that aren't real prompts: slash-command echoes, the
// bash tool's stdin/stdout, task-notification frames, memory captures. We hide them
// so the chat reads as the actual conversation. Matched at the start of the text.
export const SYS_PREFIXES = [
  "<task-notification>",
  "<bash-input>",
  "<bash-stdout>",
  "<bash-stderr>",
  "<local-command",
  "<command-message>",
  "<command-name>",
  "<command-args>",
  "<user-memory-input>",
  "<system-reminder>",
  // Auto-logged when the user interrupts a tool (e.g. rejecting an ExitPlanMode plan) —
  // a system record, not something the user typed, so keep it out of the mirror.
  "[Request interrupted by user",
];

export function isNoise(t: Turn): boolean {
  if (t.role !== "user") return false;
  const s = (t.text || "").replace(/^\s+/, "");
  return SYS_PREFIXES.some((p) => s.startsWith(p));
}

// peerSenderOf reads the sending session's name back out of the envelope the Agent
// prepends to a peer message (session_peer.go peerEnvelope). Parsing display text is
// normally a smell, but the name genuinely lives nowhere else on this side: peer turns
// are ordinary injected user turns whose only extra is the "peer" source tag. Returns
// null for anything that doesn't match, and the badge then degrades to "別のセッション".
const PEER_ENVELOPE_RE = /^\[agent-fleet:peer from=([A-Za-z0-9][A-Za-z0-9_-]*)\]/;
export function peerSenderOf(text: string): string | null {
  return PEER_ENVELOPE_RE.exec(text)?.[1] ?? null;
}

// A `!`-run shell command is logged by Claude as a user turn `<bash-input>cmd</bash-input>`,
// its result as the next user turn `<bash-stdout>…</bash-stdout><bash-stderr>…</bash-stderr>`.
// These are hidden by isNoise; parseBashInput/parseBashOutput recover the command + result
// so coalesceUserActions can surface them as a terminal block instead of dropping them.
const BASH_IN_RE = /^\s*<bash-input>([\s\S]*?)<\/bash-input>\s*$/;
export function parseBashInput(t: Turn): string | null {
  if (t.role !== "user") return null;
  const m = (t.text || "").match(BASH_IN_RE);
  return m ? m[1].trim() : null;
}
export function parseBashOutput(t: Turn): { stdout: string; stderr: string } | null {
  if (t.role !== "user") return null;
  const s = (t.text || "").replace(/^\s+/, "");
  if (!s.startsWith("<bash-stdout>")) return null;
  const out = s.match(/<bash-stdout>([\s\S]*?)<\/bash-stdout>/);
  const err = s.match(/<bash-stderr>([\s\S]*?)<\/bash-stderr>/);
  return { stdout: out ? out[1] : "", stderr: err ? err[1] : "" };
}

// A `/`-run slash command or skill is logged as a user turn
// `<command-name>/foo</command-name><command-message>…</command-message><command-args>…</command-args>`.
// It's hidden by isNoise; parseCommand recovers the name + args so it can surface as a chip.
// Tag order varies: built-ins log <command-name> first, but skill invocations (2.1.215
// 実測・定時 /scout) log <command-message> FIRST. Requiring name-first made those turns
// unparseable → isNoise dropped them entirely → no user-turn boundary, so every fire's
// reply merged into one assistant block anchored at the last visible turn (footer stuck
// at the old date). Accept either tag at the start and take the name wherever it sits.
export function parseCommand(t: Turn): { name: string; args: string } | null {
  if (t.role !== "user") return null;
  const s = (t.text || "").replace(/^\s+/, "");
  if (!s.startsWith("<command-name>") && !s.startsWith("<command-message>")) return null;
  const name = s.match(/<command-name>([\s\S]*?)<\/command-name>/);
  if (!name || !name[1].trim()) return null;
  const args = s.match(/<command-args>([\s\S]*?)<\/command-args>/);
  return { name: name[1].trim(), args: args ? args[1].trim() : "" };
}

// coalesceUserActions surfaces user actions that Claude logs as system-tagged user turns and
// isNoise would otherwise drop: a `!` shell command (`<bash-input>` + the paired `<bash-stdout>`
// result turn) becomes a kind="bash" terminal block; a `/` slash command / skill invocation
// (`<command-name>`) becomes a kind="cmd" chip. groupTurns keeps each as its own standalone
// block. Untouched turns pass through; an orphan bash-output turn stays and is dropped by isNoise.
export function coalesceUserActions(turns: Turn[]): Turn[] {
  const out: Turn[] = [];
  for (let i = 0; i < turns.length; i++) {
    const shell = parseBashInput(turns[i]);
    if (shell !== null) {
      const paired = i + 1 < turns.length ? parseBashOutput(turns[i + 1]) : null;
      if (paired) i++; // consume the result turn
      out.push({
        ...turns[i - (paired ? 1 : 0)],
        bash: true,
        text: "$ " + shell, // plain form used for copy
        parts: [{ kind: "bash", text: shell, output: paired?.stdout || "", stderr: paired?.stderr || "" }],
      });
      continue;
    }
    const slash = parseCommand(turns[i]);
    if (slash) {
      out.push({
        ...turns[i],
        cmd: true,
        text: slash.name + (slash.args ? " " + slash.args : ""), // plain form used for copy
        parts: [{ kind: "cmd", text: slash.name, info: slash.args }],
      });
      continue;
    }
    out.push(turns[i]);
  }
  return out;
}

// partsOf returns a turn's ordered parts, synthesizing a single text part for turns
// from an older Agent that predates the parts field (backward compatible).
export function partsOf(t: Turn): Part[] {
  if (Array.isArray(t.parts) && t.parts.length) return t.parts;
  return t.text ? [{ kind: "text", text: t.text }] : [];
}

// groupTurns folds consecutive same-role turns into one block (concatenating their
// ordered parts, and their text for copy) and drops noise. A block breaks on a role
// OR sidechain change so a subagent's turns stay separate from the main thread. It
// keeps the FIRST turn's idx/timestamp/branch/cwd, and for tokens sums output while
// taking the last event's input/cache as the context size.
// Timestamps are kept at BOTH ends: ts (first) orders the block, endTs (last) is what
// the footer shows. claude/codex write one turn as many rows — thinking, each tool call,
// then the answer — so the first row is the moment the model started, minutes or a day
// before the text being read. The footer must not claim that as the reply's time.
export function groupTurns(turns: Turn[]): Group[] {
  const out: Group[] = [];
  for (const t of turns) {
    // A child agent's raw prompt, reasoning, chatter and tool log are implementation
    // detail. The parent Agent/Task/spawn_agent call is rendered as one delegation card
    // instead, keeping the main conversation readable without hiding that delegation
    // happened.
    if (isNoise(t) || t.sidechain) continue;
    const parts = partsOf(t);
    if (!parts.length) continue;
    const last = out[out.length - 1];
    // A compaction summary, a `!` bash-command block, and a `/` command chip are each
    // standalone — never merged into an adjacent turn (nor a normal turn into them).
    if (
      last &&
      last.role === t.role &&
      last.sidechain === !!t.sidechain &&
      !last.compact &&
      !t.compact &&
      !last.bash &&
      !t.bash &&
      !last.cmd &&
      !t.cmd
    ) {
      last.parts.push(...parts);
      carryEnd(last, t); // the block's end follows the last row folded in
      if (t.pending) last.pending = true;
      if (t.queued) last.queued = true;
      if (t.source) last.source = t.source; // operator origin survives a same-role merge
      if (t.text) last.text += (last.text ? "\n\n" : "") + t.text;
      if (!last.model && t.model) last.model = t.model;
      if (!last.effort && t.effort) last.effort = t.effort;
      if (t.ctxWindow) last.ctxWindow = t.ctxWindow;
      last.outTok += t.outTok || 0;
      if (t.inTok || t.cacheRead || t.cacheCreate) {
        last.inTok = t.inTok || 0;
        last.cacheRead = t.cacheRead || 0;
        last.cacheCreate = t.cacheCreate || 0;
      }
    } else {
      out.push({
        role: t.role,
        sidechain: !!t.sidechain,
        compact: !!t.compact,
        bash: !!t.bash,
        cmd: !!t.cmd,
        parts: [...parts],
        text: t.text || "",
        model: t.model || "",
        effort: t.effort || "",
        ctxWindow: t.ctxWindow || 0,
        branch: t.branch || "",
        cwd: t.cwd || "",
        inTok: t.inTok || 0,
        outTok: t.outTok || 0,
        cacheRead: t.cacheRead || 0,
        cacheCreate: t.cacheCreate || 0,
        ts: t.ts,
        endTs: endOf(t) || undefined,
        idx: t.idx,
        anchorId: t.anchorId,
        pending: !!t.pending,
        queued: !!t.queued,
        source: t.source,
      });
    }
  }
  return out;
}

// latestContext returns the newest assistant turn's prompt breakdown (reused-cache,
// newly-cached, fresh) — the current context fill — or null if no usage is recorded
// yet (e.g. an Agent that predates the usage field, before a Stop/Start).
export function latestContext(groups: Group[]) {
  for (let i = groups.length - 1; i >= 0; i--) {
    const g = groups[i];
    if (g.role === "user") continue;
    if (g.inTok + g.cacheRead + g.cacheCreate > 0) {
      return { read: g.cacheRead, create: g.cacheCreate, fresh: g.inTok, model: g.model, window: g.ctxWindow };
    }
  }
  return null;
}

// spendOf is a turn's newly-consumed tokens (uncached input + cache creation + output).
export function spendOf(g: Group): number {
  return g.inTok + g.cacheCreate + g.outTok;
}

// ctxSizeOf is a turn's total prompt size (reused-cache + newly-cached + fresh input) —
// i.e. how much context that turn carried. 0 for turns with no recorded usage.
export function ctxSizeOf(g: Group): number {
  return g.cacheRead + g.cacheCreate + g.inTok;
}

// ctxSizeBefore / ctxSizeAfter bracket a compaction: the prompt size of the nearest
// assistant turn with usage before the compact block (the context that had piled up and
// triggered compaction) and after it (the compacted context). Return 0 when no such turn
// exists yet — before the post-compaction turn lands, or a resume with no prior turn —
// so CompactBlock can hide the effect line until both numbers are real.
export function ctxSizeBefore(groups: Group[], i: number): number {
  for (let j = i - 1; j >= 0; j--) {
    if (groups[j].role === "assistant" && ctxSizeOf(groups[j]) > 0) return ctxSizeOf(groups[j]);
  }
  return 0;
}
export function ctxSizeAfter(groups: Group[], i: number): number {
  for (let j = i + 1; j < groups.length; j++) {
    if (groups[j].role === "assistant" && ctxSizeOf(groups[j]) > 0) return ctxSizeOf(groups[j]);
  }
  return 0;
}

// foldParts walks a block's ordered parts and coalesces each maximal run of
// consecutive tool traces into one { kind:"toolrun", tools:[{p,i}] } item; every
// other part passes through as { kind:"part", p, i }. A run of length 1 still
// becomes a toolrun (ToolRun renders it inline), so callers only branch two ways.
export function foldParts(parts: Part[]): FoldItem[] {
  const items: FoldItem[] = [];
  let run: { kind: "toolrun"; tools: { p: Part; i: number }[] } | null = null;
  parts.forEach((p, i) => {
    if (p.kind === "tool") {
      if (!run) {
        run = { kind: "toolrun", tools: [] };
        items.push(run);
      }
      run.tools.push({ p, i });
    } else {
      run = null;
      items.push({ kind: "part", p, i });
    }
  });
  return items;
}
