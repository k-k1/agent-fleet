// Pure skill-picker logic (docs/log/50 / ADR0034). Touches neither the DOM nor the store:
// MirrorView delegates trigger detection, filtering and insertion here.
// The trigger character depends on the kind (claude/opencode/cursor use "/", codex uses a "$"
// mention - registry's skillTrigger). Invocation is treated as valid only at the head of the
// input, so the trigger counts as "completion" only while the caret is inside that first token.
// Once a space is typed and the arguments begin, it becomes a passive token with args=true: the
// list stays up as an argument hint but does not capture the keyboard (see MirrorView's skillArgs
// / skillNavActive).

import type { SessionSkill } from "../../core/api/client.ts";

export interface SlashToken {
  token: string; // the fragment being typed, trigger character removed ("" = right after the trigger)
  start: number; // always 0 (leading trigger) - the left edge of the insertion replacement
  end: number; // the right edge of the replacement (first whitespace, or end of text)
  bare?: boolean; // leading token with no trigger character (button-initiated filtering; see pickerTokenAt)
  args?: boolean; // caret is right of the leading token (= writing arguments) - for the passive display
}

// Full-width aliases: a Japanese IME types "/" and "$" as full-width characters (／ and ＄), so
// accept them as equivalent triggers. On confirmation the whole token is replaced by invoke (the
// correct half-width form), so a full-width character can never be sent by accident.
const TRIGGER_ALIASES: Record<string, string[]> = { "/": ["/", "／"], $: ["$", "＄"] };

// triggerHead: if text starts with the trigger (or a full-width alias), return the matched string.
function triggerHead(text: string, trigger: string): string | null {
  if (!trigger) return null;
  for (const a of TRIGGER_ALIASES[trigger] ?? [trigger]) {
    if (text.startsWith(a)) return a;
  }
  return null;
}

// hasTriggerHead: the public predicate used by MirrorView's "draft drifted from token" guard.
export function hasTriggerHead(text: string, trigger: string): boolean {
  return triggerHead(text, trigger) !== null;
}

// slashTokenAt: return the token to complete, given the draft and the caret position. Out of
// scope (head is not the trigger, caret left of the trigger) returns null. If the caret is right
// of the leading token (= arguments are being written), args=true is set: the token itself (the
// replacement range) is unchanged, so the caller can tell "completing" apart from "typing
// arguments" (hint only).
export function slashTokenAt(text: string, caret: number, trigger = "/"): SlashToken | null {
  const head = triggerHead(text, trigger);
  if (!head) return null;
  const ws = text.search(/[\s]/); // the token ends at the first whitespace (newlines included)
  const end = ws < 0 ? text.length : ws;
  if (caret < head.length) return null;
  const tok: SlashToken = { token: text.slice(head.length, end), start: 0, end };
  return caret > end ? { ...tok, args: true } : tok;
}

// pickerTokenAt: the token the picker actually filters with. With allowBare (i.e. while opened
// from the "/" button) the first token is accepted as the query even without a trigger character,
// so typing straight after opening with the button narrows the list. On confirmation
// applySkillToDraft replaces that same token with invoke under the same rule, so the query can
// never be left behind as an argument.
// Only in the bare (no trigger) case does the second word or later return null = everything: past
// the first word of a trigger-less draft the text is no longer "more of the filter", so it is not
// used as a query. With a trigger, slashTokenAt returns a passive token with args=true instead
// (the list stays alive to show the argument hint).
export function pickerTokenAt(text: string, caret: number, trigger = "/", allowBare = false): SlashToken | null {
  const tok = slashTokenAt(text, caret, trigger);
  if (tok || !allowBare) return tok;
  if (triggerHead(text, trigger)) return null; // with a trigger, slashTokenAt's verdict wins
  const ws = text.search(/[\s]/);
  const end = ws < 0 ? text.length : ws;
  if (caret > end) return null;
  return { token: text.slice(0, end), start: 0, end, bare: true };
}

// splitDoubleTrigger: a token that itself starts with the trigger (the user typed "//" or
// "$$") is the "show the bundled ones too" gesture (docs/log/50 §9). Strip that second trigger
// and return the remaining query with all=true; any other token is returned unchanged. The
// full-width aliases count here too (a Japanese IME types ／／).
export function splitDoubleTrigger(token: string, trigger: string): { query: string; all: boolean } {
  const head = triggerHead(token, trigger);
  if (!head) return { query: token, all: false };
  return { query: token.slice(head.length), all: true };
}

// isCli: a CLI-bundled/advertised entry (claude's shipped skills, codex's .system, cursor's
// builtin list) — the second tier of the picker.
export function isCli(s: SessionSkill): boolean {
  return s.source === "cli";
}

// collapseCli: the two-tier display (docs/log/50 §9). With showAll=false the CLI-bundled
// entries are folded away behind a "show N more" row, so the user's own skills come first —
// but only when there IS a first tier: a list made of cli entries alone (cursor's advertised
// list, a claude session with no skills of its own) is shown as it is, since hiding all of it
// behind one click would just be a hurdle. Returns the rows to show and the folded count.
export function collapseCli(items: SessionSkill[], showAll: boolean): { shown: SessionSkill[]; hidden: number } {
  if (showAll) return { shown: items, hidden: 0 };
  const own = items.filter((s) => !isCli(s));
  if (own.length === 0) return { shown: items, hidden: 0 };
  return { shown: own, hidden: items.length - own.length };
}

// Word separators in a skill name or a query: kebab, snake, dotted, namespaced ("plugin:skill")
// and path-like names all split the same way, so the user never has to know which one a name uses.
const WORD_SEPARATORS = /[-_.:/\s]+/;
// One word inside a separator-free run: an acronym ("HTML" in "HTMLParser"), a capitalised or
// lower-case word, a digit run, or a run of anything else (kana/kanji names stay one word each
// instead of being dropped).
const WORD_PIECE = /\p{Lu}+(?!\p{Ll})|\p{Lu}?\p{Ll}+|\p{N}+|[^\p{Lu}\p{Ll}\p{N}]+/gu;

// splitWords: lower-cased words of a name or a query, split on separators and on camelCase /
// PascalCase / digit boundaries. "issue-to-pr", "issue_to_pr" and "IssueToPr" all give
// ["issue", "to", "pr"].
export function splitWords(text: string): string[] {
  const out: string[] = [];
  for (const part of text.split(WORD_SEPARATORS)) {
    for (const w of part.match(WORD_PIECE) ?? []) out.push(w.toLowerCase());
  }
  return out;
}

// wordMatchCost: does the query read as word prefixes of the name, in order? Each query segment
// (the query split by splitWords, so a typed "-", "_" or capital letter is a word break) is
// consumed as the prefixes of one or more successive name words, and a segment always starts on a
// new word. Words may be skipped. So for "issue-to-pr": "iss-t", "iss_to", "issTo", "issto",
// "itp", "to-pr" and "isspr" all match, while "iss-r" does not (no word after "issue" starts with
// "r"). Returns the number of words skipped before or between the matched ones for the best split
// (0 = matched from the first word with nothing skipped; unused trailing words are free), or -1
// for no match. Because a capital starts a word, a stray capital mid-word ("IsST") splits the
// segment and misses; that is the price of honouring "issT" and "IssueToPr" as typed.
// A plain subsequence ("every character somewhere, in order") is deliberately not accepted: with
// short queries it lights up most of the list ("/iss" would hit every name with i…s…s), which is
// the noise this matcher exists to avoid.
export function wordMatchCost(name: string, query: string): number {
  return wordCost(splitWords(name), splitWords(query));
}

// wordCost: wordMatchCost on already split words, so filterSkills splits the query once per
// keystroke rather than once per skill.
function wordCost(words: string[], segs: string[]): number {
  if (segs.length === 0 || words.length === 0) return -1;
  const memo = new Map<string, number>();
  // best(si, ci, wi): least skipped words to match segs[si] from character ci on, using words
  // from wi on. Names are a handful of words, so the memoised search is trivially small.
  const best = (si: number, ci: number, wi: number): number => {
    if (si === segs.length) return 0;
    const key = si + ":" + ci + ":" + wi;
    const hit = memo.get(key);
    if (hit !== undefined) return hit;
    const seg = segs[si];
    let res = Infinity;
    for (let j = wi; j < words.length; j++) {
      const w = words[j];
      // Try every prefix length of this word that the segment shares from ci.
      for (let k = 1; k <= w.length && ci + k <= seg.length && w[k - 1] === seg[ci + k - 1]; k++) {
        const rest = ci + k === seg.length ? best(si + 1, 0, j + 1) : best(si, ci + k, j + 1);
        res = Math.min(res, j - wi + rest);
      }
    }
    memo.set(key, res);
    return res;
  };
  const cost = best(0, 0, 0);
  return Number.isFinite(cost) ? cost : -1;
}

// filterSkills: order by name prefix > word-prefix match (wordMatchCost) > name substring >
// description substring. Case-insensitive. Within one rank the user's own entries
// (project/user/foreign) come before the CLI-bundled ones, so `/sim` lists a project "simulate"
// ahead of the shipped "simplify"; inside the word-match rank fewer skipped words then win, so
// "/t-p" lists "to-pr-helper" (nothing skipped) ahead of "issue-to-pr" (one word skipped).
// An empty query returns everything in that tier order (the API's name order within a tier).
export function filterSkills(skills: SessionSkill[], query: string): SessionSkill[] {
  const q = query.trim().toLowerCase();
  // Split the query before lower-casing it: its capitals are word breaks ("issT").
  const segs = splitWords(query.trim());
  const tier = (s: SessionSkill): number => (isCli(s) ? 1 : 0);
  const rank = (s: SessionSkill): { r: number; c: number } => {
    if (!q) return { r: 0, c: 0 };
    const nm = s.name.toLowerCase();
    if (nm.startsWith(q)) return { r: 0, c: 0 };
    const c = wordCost(splitWords(s.name), segs);
    if (c >= 0) return { r: 1, c };
    if (nm.includes(q)) return { r: 2, c: 0 };
    if ((s.description || "").toLowerCase().includes(q)) return { r: 3, c: 0 };
    return { r: -1, c: 0 };
  };
  return skills
    .map((s) => ({ s, ...rank(s), t: tier(s) }))
    .filter((x) => x.r >= 0)
    // Tier before cost: a CLI-bundled skill never jumps an own one, however clean its word match.
    .sort((a, b) => a.r - b.r || a.t - b.t || a.c - b.c)
    .map((x) => x.s);
}

// exactSkills: the filter used while arguments are being typed (an args token). The leading
// command is already typed and settled, so keep only native items whose name matches exactly - the
// point is to keep that one item's argument hint/description visible, and listing partial matches
// alongside it would be noise. When nothing matches (just a sentence starting with "/", say) the
// result is empty = no list.
export function exactSkills(skills: SessionSkill[], query: string): SessionSkill[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  return skills.filter((s) => !!s.invoke && s.name.toLowerCase() === q);
}

// originKind: the origin convention dir of a foreign skill -> the kind shown in the UI. ".agents"
// is the cross-agent shared convention and belongs to no kind -> null (a neutral "shared" badge).
// ".muse" only ever names muse's user root (the Agent's appendUserForeignSkills).
export function originKind(origin: string | undefined): "claude" | "codex" | "muse" | null {
  if (origin === ".claude") return "claude";
  if (origin === ".codex") return "codex";
  if (origin === ".muse") return "muse";
  return null;
}

// applySkillToDraft: insert the chosen skill's invocation string (invoke - including the trailing
// space, e.g. "/name " or "$name ") into the draft and return the new draft and caret position.
// An invocation only means anything at the head, so the result is always built as "invoke + the
// existing body (kept as arguments)". The token being typed is replaced away (with allowBare a
// trigger-less leading token is also the text that was filtered with, so it is replaced too - read
// this together with pickerTokenAt). Choosing a different command while typing arguments (an args
// token) replaces only the leading command; the arguments already written stay to its right.
export function applySkillToDraft(
  draft: string,
  caret: number,
  invoke: string,
  trigger = "/",
  allowBare = false,
): { next: string; caret: number } {
  const tok = pickerTokenAt(draft, caret, trigger, allowBare);
  // If the token is alive, keep what is to its right (the arguments already written); otherwise
  // (button-initiated) keep a whole draft that does not start with the trigger as the arguments.
  const tail = tok
    ? draft.slice(tok.end).trimStart()
    : hasTriggerHead(draft, trigger)
      ? ""
      : draft.trimStart();
  return { next: invoke + tail, caret: invoke.length };
}
