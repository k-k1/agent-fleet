// features/mirror/turnTts — karaoke-style narration of a mirror turn's body (docs/log/24).
//
// Collects blocks (p / h1-h6 / li / paragraphs inside blockquote) in document order from the DOM
// MarkdownView rendered via innerHTML, splits their textContent into sentences and hands them to
// startNarration (features/chat/tts.ts). Speech unit = sentence, highlight unit = block; pre
// (code), table and mermaid are not read. Splitting the source Markdown string instead would be
// fragile, because keeping marked's tokens aligned with the render result is hard — textContent
// has already dropped the notation, and links keep only their display text. A turn arrives once
// complete (polling), so extracting once at narration start is stable.

import {
  startNarration,
  BLOCK_BEAT,
  SENT_BEAT,
  type TtsEndReason,
  type TtsOptions,
  type TtsStopReason,
} from "../chat/tts.ts";
import { splitSentences, splitLongSentence, abbrevCode, type CodeReadOpts } from "../chat/ttsText.ts";
import { effectiveDict } from "../chat/ttsDict.ts";
import { getSettings } from "../../lib/settings.ts";

// Leaf blocks that get read. ul/ol are read per li (a nested list is its own block), blockquote
// descends into its paragraphs; pre / table / hr / mermaid (div) are skipped.
const LEAF = new Set(["P", "H1", "H2", "H3", "H4", "H5", "H6"]);

// collectBlocks returns, in document order, the block elements of a turn body (.mirror-turn-body)
// that form the speech/highlight units. Only text parts count (a .markdown directly under the
// body = a MarkdownView root); tool output, thinking (.markdown inside details), plan and
// question are not picked up.
export function collectBlocks(body: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  body.querySelectorAll<HTMLElement>(":scope > .markdown").forEach((md) => walk(md, out));
  return out;
}

function walk(container: HTMLElement, out: HTMLElement[]): void {
  for (const el of Array.from(container.children) as HTMLElement[]) {
    const tag = el.tagName;
    if (LEAF.has(tag)) out.push(el);
    else if (tag === "UL" || tag === "OL") walkList(el, out);
    else if (tag === "BLOCKQUOTE") walk(el, out);
  }
}

function walkList(list: HTMLElement, out: HTMLElement[]): void {
  for (const li of Array.from(list.children) as HTMLElement[]) {
    if (li.tagName !== "LI") continue;
    out.push(li);
    for (const sub of Array.from(li.children) as HTMLElement[]) {
      if (sub.tagName === "UL" || sub.tagName === "OL") walkList(sub, out);
    }
  }
}

// finalAnswerStart returns the block index where the final answer's body begins (indices follow
// collectBlocks' block order); 0 when there is no tool. Mirror auto-narration uses it to skip the
// narration of the work leading up to the answer and read only the final answer (same split as in
// chat, docs/log/19).
// Text parts sit as .markdown directly under the body, tool runs as direct children with an
// mt-tool* class. Only work tools appearing *before* the first final-answer body are skipped:
// tools that come after it (writing a memo and other clean-up) are part of the final answer — in
// a completed turn workSplit folds the work trace into a disclosure, so the tools left as direct
// children are only the clean-up. Skipping past them would read just the trailing sentence.
export function finalAnswerStart(body: HTMLElement): number {
  let count = 0; // speech blocks counted so far
  let boundary = 0; // block count at which the final answer's body starts
  let sawAnswer = false; // whether a final-answer body block has been seen
  for (const el of Array.from(body.children) as HTMLElement[]) {
    if (el.classList.contains("markdown")) {
      const blocks: HTMLElement[] = [];
      walk(el, blocks);
      if (blocks.length) sawAnswer = true;
      count += blocks.length;
    } else if (!sawAnswer && Array.from(el.classList).some((c) => c.startsWith("mt-tool"))) {
      boundary = count; // a tool before the final-answer body is work trace, so skip it
    }
  }
  return boundary;
}

// blockText is a block's own spoken text. For an li it returns only its own text, excluding
// nested lists (read as their own blocks), code blocks, tables and mermaid (div). Inline elements
// are descended into recursively, and <code> (from backticks) gets the abbreviated reading
// (abbrevCode) — the rendered DOM no longer contains the backticks, so this is the only place
// that decision can be made, the equivalent of plainify.
const EXCLUDE = new Set(["UL", "OL", "PRE", "TABLE", "DIV"]);
function blockText(el: HTMLElement, code?: CodeReadOpts): string {
  let t = "";
  el.childNodes.forEach((n) => {
    if (n.nodeType === Node.ELEMENT_NODE) {
      const e = n as HTMLElement;
      if (EXCLUDE.has(e.tagName)) return;
      if (e.tagName === "CODE") {
        const s = e.textContent ?? "";
        t += code?.abbrev ? abbrevCode(s, code.dict) : s;
        return;
      }
      t += blockText(e, code);
      return;
    }
    t += n.textContent ?? "";
  });
  return t;
}

// blockIndexAt returns the index of the first readable block at or after the node a selection
// starts from: the containing block if the node is inside one, otherwise the next block when the
// selection starts between blocks (in tool output, say). -1 when there is none.
export function blockIndexAt(blocks: HTMLElement[], node: Node): number {
  const el = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
  if (!el) return -1;
  const within = blocks.findIndex((b) => b.contains(el));
  if (within >= 0) return within;
  return blocks.findIndex((b) => !!(node.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING));
}

// turnSpokenText returns the text to be read from fromBlock on — the input for summarised
// narration and for length checks. Raw text, with no abbreviation or dictionary applied; code and
// tables were already excluded when the blocks were collected.
export function turnSpokenText(body: HTMLElement, fromBlock = 0): string {
  return collectBlocks(body)
    .slice(fromBlock)
    .map((b) => blockText(b).replace(/\s+/g, " ").trim())
    .filter(Boolean)
    .join("\n");
}

// --- Registering the narrating pane (auto-narration across panes, docs/log/24) ---------------
// When the same session is open in several panes, only the pane that registered first performs
// auto-narration and confirmation narration, so nothing is read twice. If that pane closes, the
// next registered pane takes over automatically. useSessionNotifications uses hasTurnReader to
// avoid layering a short announcement on top of a session whose body is already being narrated.
const readers = new Map<string, symbol[]>();

// claimTurnReader registers a pane (token) as a candidate narrator for the session and returns
// the unregister function, ready to be used as a useEffect cleanup.
export function claimTurnReader(session: string, token: symbol): () => void {
  const arr = readers.get(session) ?? [];
  arr.push(token);
  readers.set(session, arr);
  return () => {
    const cur = readers.get(session);
    if (!cur) return;
    const i = cur.indexOf(token);
    if (i >= 0) cur.splice(i, 1);
    if (!cur.length) readers.delete(session);
  };
}

// isTurnReader reports whether token is the session's narrator (the first to register).
export function isTurnReader(session: string, token: symbol): boolean {
  return (readers.get(session) ?? [])[0] === token;
}

// hasTurnReader reports whether a mirror pane able to narrate this session is open anywhere.
export function hasTurnReader(session: string): boolean {
  return (readers.get(session)?.length ?? 0) > 0;
}

export interface TurnReadHandle {
  pause(): void;
  resume(): void;
  stop(reason?: TtsStopReason): void;
}

const ACTIVE = "tts-active";

// readTurn narrates body from block fromBlock on, karaoke-highlighting and scrolling to the block
// the currently playing sentence belongs to. onEnd is called exactly once, with a reason, whether
// narration finished naturally, was stopped explicitly or was replaced by another playback.
// Returns null when there is no sentence to read, in which case onEnd is never called.
export function readTurn(
  body: HTMLElement,
  source: string,
  fromBlock: number,
  onEnd: (reason: TtsEndReason) => void,
  voice?: Partial<TtsOptions>, // per-session voice overrides (sessionVoiceOpts) and the like
  sessionName = "", // originating session name, for the left rail's "playing" icon
): TurnReadHandle | null {
  const code: CodeReadOpts = { abbrev: getSettings().ttsAbbrevCode, dict: effectiveDict() };
  const blocks = collectBlocks(body);
  const texts: string[] = [];
  const blockOf: number[] = [];
  const sentHead: boolean[] = []; // is this the first piece of a sentence (false = continuation of a long one)
  blocks.forEach((b, bi) => {
    if (bi < fromBlock) return;
    for (const s of splitSentences(blockText(b, code))) {
      // Split one long sentence further for synthesis, so waiting on the synthesiser does not
      // leave silence. Highlighting stays per block, so this is invisible.
      splitLongSentence(s).forEach((piece, j) => {
        texts.push(piece);
        blockOf.push(bi);
        sentHead.push(j === 0);
      });
    }
  });
  if (!texts.length) return null;

  let lit: HTMLElement | null = null;
  const light = (el: HTMLElement | null) => {
    if (lit === el) return;
    lit?.classList.remove(ACTIVE);
    lit = el;
    if (el) {
      el.classList.add(ACTIVE);
      el.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  };
  // Put a beat before the first sentence of a new block (paragraph, list item, heading): marker
  // characters are not spoken, so the structural break is conveyed by the pause instead. Sentence
  // boundaries within one block get the shorter SENT_BEAT, and continuation pieces of a split
  // long sentence get no pause at all (only whatever silence the audio itself carries).
  const preGaps = blockOf.map((b, i) => {
    if (i === 0 || !sentHead[i]) return 0;
    return b !== blockOf[i - 1] ? BLOCK_BEAT : SENT_BEAT;
  });
  const h = startNarration(
    texts,
    source,
    (i, endReason) => {
      if (i == null) {
        light(null);
        onEnd(endReason ?? "done");
      } else light(blocks[blockOf[i]]);
    },
    voice,
    preGaps,
    sessionName,
  );
  return { pause: h.pause, resume: h.resume, stop: h.stop };
}
