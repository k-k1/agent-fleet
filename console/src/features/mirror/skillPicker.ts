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

// filterSkills: order by prefix match > name substring > description substring. Case-insensitive.
// An empty query returns everything (in the API's order, i.e. name ascending).
export function filterSkills(skills: SessionSkill[], query: string): SessionSkill[] {
  const q = query.trim().toLowerCase();
  if (!q) return skills;
  const rank = (s: SessionSkill): number => {
    const nm = s.name.toLowerCase();
    if (nm.startsWith(q)) return 0;
    if (nm.includes(q)) return 1;
    if ((s.description || "").toLowerCase().includes(q)) return 2;
    return -1;
  };
  return skills
    .map((s) => ({ s, r: rank(s) }))
    .filter((x) => x.r >= 0)
    .sort((a, b) => a.r - b.r)
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
export function originKind(origin: string | undefined): "claude" | "codex" | null {
  if (origin === ".claude") return "claude";
  if (origin === ".codex") return "codex";
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
