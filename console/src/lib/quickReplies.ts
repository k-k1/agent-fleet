// Reply suggestions (quick replies) — learning and ranking for the short-answer chips shown just
// above the composer.
//
// Layer A: learns the frequency and recency of the short messages the user has sent and offers the
//          top ones, so everyday replies line up on their own.
// Layer B-1: reorders by the content of the agent's most recent answer (a zero-token heuristic).
//
// Storage is settings.quickReplies, the same shape as ssmHostUsage, so the server mirror syncs it
// across devices. Keys are normalized, width-folded and lowercased, collapsing "OK"/"ok" and the
// fullwidth and halfwidth forms of digits and letters into one entry; the display text keeps the
// spelling of the most recent send.
// A suggestion the user deletes is pushed onto settings.quickRepliesHidden (an array of keys) and
// hidden permanently, seeds included. Sending the same text again is taken as intent to use it once
// more, and lifts the hiding.
//
// Pinning (settings.quickRepliesPinned) overrides the ranking. Learned frequency and the B-1
// context boost are only guesses at what is useful right now, and the boost (80-120) is an order of
// magnitude larger than realistic use counts, so a candidate matching a context word always takes
// the left-hand slots while a longer sentence the user really relies on is pushed past limit and
// disappears despite being in use. A pin is the only way to fix a suggestion in place, so it loses
// to nothing — not the hidden list, the length cap, limit or eviction — and is always emitted
// first, in pin order.

export type QuickReplyUse = { text: string; count: number; at: number };
export type QuickReplyMap = Record<string, QuickReplyUse>;

// Maximum length of a suggestion; anything longer is a question or a prompt, not a quick reply.
// Enforced both when learning (isQuickReplyCandidate) and when displaying (rankQuickReplies), so
// lowering the threshold retroactively hides longer entries that were already learned. 20 is the
// length at which a chip still does not take a whole row.
const MAX_LEN = 20;
// Maximum number of stored entries; past this the weakest (ascending count, then at) are evicted.
// The hidden list shares the cap, which always leaves room to hide learned entries one at a time.
// Display is capped by limit, so this only governs how much vocabulary is remembered, i.e. how few
// candidates die in eviction. Everything stored rides along in the settings payload to the server
// mirror, but one entry is a short string plus two numbers, so even 100 is a few KB.
const MAX_ENTRIES = 100;
// Maximum number of pins. Pins are always shown, so without a cap they fill the chip row. Past it
// the oldest pin is dropped, favouring the more recent intent.
const MAX_PINNED = 12;

// Collapse whitespace and trim. Used for both display and matching.
function normalize(text: string): string {
  return text.trim().replace(/\s+/g, " ");
}

// Absorb fullwidth/halfwidth differences (matching key only). One toggle of the IME makes the
// fullwidth and halfwidth forms of the same reply two different entries, duplicating the chip and
// splitting its count, so the key alone is pushed through NFKC. NFKC is the standard normalization
// for compatibility characters: it folds fullwidth alphanumerics to halfwidth and halfwidth kana to
// fullwidth, composed voiced marks included — which an implementation that shifts code points by
// hand gets wrong for voiced halfwidth kana. Case is still folded by keyOf's lowercasing. Never
// applied to the display text: the user's own spelling is what gets shown.
function foldWidth(text: string): string {
  return text.normalize("NFKC");
}

// The matching key for learning, hiding and pinning: normalize, fold width, lowercase.
// Changing this normalization leaves stored entries under their old keys, so always match against
// keyOf(stored display text) rather than the stored key, and let record/forget/rank fold the old
// and new keys into one entry.
function keyOf(text: string): string {
  return foldWidth(normalize(text)).toLowerCase();
}

// Public version of the same folding, for the Tab-completion ring (lib/suggestCycle) and the
// duplicate check between generated suggestions and learned chips (MirrorView / ChatView). If a
// caller matches on a different basis, what the chip row shows drifts from what can be selected
// and what counts as a duplicate.
export function quickReplyKey(text: string): string {
  return keyOf(text);
}

// Whether this sent text should be learned: one line, short, no attachments, and not a slash
// command or a path.
export function isQuickReplyCandidate(text: string, hasAttachments: boolean): boolean {
  if (hasAttachments) return false;
  const t = text.trim();
  if (!t) return false;
  if (t.length > MAX_LEN) return false;
  if (/[\r\n]/.test(t)) return false; // multi-line text is not a reply template
  if (t.startsWith("/")) return false; // slash command or absolute path
  return true;
}

// Record a sent text into the frequency map and return a new map (pure). Evicts past the cap.
export function recordQuickReply(map: QuickReplyMap, text: string, now: number): QuickReplyMap {
  const norm = normalize(text);
  const k = keyOf(norm);
  // Carry over the counts of every existing entry that folds to this key — itself, plus anything
  // learned under a width-variant key before the key normalization changed — and drop the old keys.
  // The display spelling is the one most recently sent.
  const next: QuickReplyMap = { ...map };
  let count = 0;
  for (const [k2, e] of Object.entries(map)) {
    if (k2 !== k && keyOf(e.text) !== k) continue;
    count += e.count;
    delete next[k2];
  }
  next[k] = { text: norm, count: count + 1, at: now };
  const keys = Object.keys(next);
  if (keys.length > MAX_ENTRIES) {
    // Evict the weakest first (by use count, then recency). The key just written is new, so it stays.
    keys
      .sort((a, b) => next[a].count - next[b].count || next[a].at - next[b].at)
      .slice(0, keys.length - MAX_ENTRIES)
      .forEach((dead) => delete next[dead]);
  }
  return next;
}

// Delete one learned entry (pure). Removing it from the display alone lets the next send revive it,
// so callers pair this with hideQuickReply. Seeds are not in the learned map, so for them only the
// hidden list has any effect.
export function forgetQuickReply(map: QuickReplyMap, text: string): QuickReplyMap {
  const k = keyOf(text);
  // Also delete the same sentence still held under an old width-variant key, so deleting one chip
  // does not leave an identical-looking one behind.
  const dead = Object.keys(map).filter((k2) => k2 === k || keyOf(map[k2].text) === k);
  if (!dead.length) return map;
  const next = { ...map };
  dead.forEach((d) => delete next[d]);
  return next;
}

// Display texts of learned entries sent exactly once (pure; for the bulk delete in settings).
// Learning grows silently on every send, so one-off phrasings come to dominate and the list becomes
// unreadable. This is the target list for dropping the throwaways while keeping the everyday ones.
//
// - Counts are folded by the key derived from the spelling, not by the stored key: the same
//   sentence split across two width-variant keys totals 2 and is therefore not a one-off. Same as
//   how the list's rows are built.
// - A pin is an explicit choice, so it is excluded whatever its count; a pin loses to nothing.
// Callers delete with forgetQuickReply and must not add to the hidden list: this is cleaning up
// learning noise, not "never show this again", and sending the text again should learn it afresh.
// It also avoids wiping out a seed when a text spelled like one happened to be learned once.
export function oneTimeQuickReplies(map: QuickReplyMap, pinned?: string[]): string[] {
  const byKey = new Map<string, { text: string; count: number; at: number }>();
  for (const e of Object.values(map)) {
    const k = keyOf(e.text);
    const prev = byKey.get(k);
    byKey.set(
      k,
      prev
        ? { text: e.at >= prev.at ? e.text : prev.text, count: prev.count + e.count, at: Math.max(prev.at, e.at) }
        : { ...e },
    );
  }
  const pins = new Set((pinned ?? []).map((p) => keyOf(p)));
  return [...byKey.entries()].filter(([k, e]) => e.count <= 1 && !pins.has(k)).map(([, e]) => e.text);
}

// Push a key onto the hidden list. Same cap as learned entries: bounded, oldest dropped first.
export function hideQuickReply(hidden: string[], text: string): string[] {
  const k = keyOf(text);
  // Compare keyOf to keyOf, so an old key stored in the other width is treated as the same entry.
  if (!k || hidden.some((h) => keyOf(h) === k)) return hidden;
  const next = [...hidden, k];
  return next.length > MAX_ENTRIES ? next.slice(next.length - MAX_ENTRIES) : next;
}

// Lift the hiding. Called when the user sends that text again, which is their intent to reuse it.
// Returns the same array reference when nothing changed, so callers can save only real diffs.
export function unhideQuickReply(hidden: string[], text: string): string[] {
  const k = keyOf(text);
  if (!hidden.some((h) => keyOf(h) === k)) return hidden;
  return hidden.filter((h) => keyOf(h) !== k);
}

// Pinning (always shown). Unlike hiding, this stores the display spelling rather than the key, so
// the text can be restored from the pin alone even after the learned entry was evicted or when it
// was never a seed; a pin never disappearing is the whole requirement. The order is the order they
// were pinned, i.e. the user's own order, and the ranking never moves them.
export function pinQuickReply(pinned: string[], text: string): string[] {
  const norm = normalize(text);
  const k = keyOf(norm);
  if (!k || pinned.some((p) => keyOf(p) === k)) return pinned;
  const next = [...pinned, norm];
  return next.length > MAX_PINNED ? next.slice(next.length - MAX_PINNED) : next;
}

// Unpin. Returns the same array reference when nothing changed.
export function unpinQuickReply(pinned: string[], text: string): string[] {
  const k = keyOf(text);
  if (!pinned.some((p) => keyOf(p) === k)) return pinned;
  return pinned.filter((p) => keyOf(p) !== k);
}

// Whether this text is pinned (case and whitespace differences ignored).
export function isQuickReplyPinned(pinned: string[] | undefined, text: string): boolean {
  const k = keyOf(text);
  return (pinned ?? []).some((p) => keyOf(p) === k);
}

// i18n-exempt-start: what follows is dictionary data - the seed phrases and the words matched
// against - not UI copy to translate. The locale key selects the ja/en *content*, the same way raw
// fontStack values or VOICEVOX voice names work.
// Initial seeds, so a few everyday replies are offered before anything has been learned. Their
// count is 0, so real usage overtakes them immediately.
const SEEDS: Record<string, string[]> = {
  ja: ["OK", "進めて", "続けて", "commit して", "やめて"],
  en: ["OK", "Go ahead", "Continue", "Commit it", "Stop"],
};

// Short affirmative/negative answers, boosted right after an answer that ends in a question mark.
// Matched in lowercase.
const AFFIRM = new Set(["ok", "はい", "yes", "y", "進めて", "続けて", "go ahead", "continue", "sure"]);
const NEGATE = new Set(["no", "いいえ", "n", "やめて", "待って", "stop", "cancel", "キャンセル"]);

// Boost from the most recent answer (lastReply) — B-1. lastReply is not assumed to arrive
// lowercased; that is handled here.
//
// The boost takes the MAXIMUM, not the sum. Summing lets a greedy sentence that happens to contain
// several keywords (commit plus proceed, say) collect +180 and structurally beat a plain
// single-word candidate (+100 / +80) forever, so it sticks to the front in every context. Matching
// one context signal is enough; the number of matches is not a measure of relevance.
// lr is the already-keyOf'd recent answer (width and case folded). Answers are long, so the caller
// folds once rather than re-folding for every candidate.
function contextBoost(entryText: string, lastReply: string, lr: string): number {
  if (!lastReply) return 0;
  // Fold the candidate the same way, so one learned from fullwidth typing still matches.
  const et = keyOf(entryText);
  let boost = 0;
  // A question (ends in ? or its fullwidth form) boosts the short affirmative/negative answers.
  if (/[?？]\s*$/.test(lastReply)) {
    if (AFFIRM.has(et) || NEGATE.has(et)) boost = Math.max(boost, 120);
  }
  // An answer about committing boosts the commit-related suggestions.
  if ((lr.includes("commit") || lr.includes("コミット")) && (et.includes("commit") || et.includes("コミット")))
    boost = Math.max(boost, 100);
  // An answer about proceeding or continuing boosts the continuation suggestions.
  if (/続け|進め|proceed|continue/.test(lr) && /続け|進め|proceed|continue|ok/.test(et)) boost = Math.max(boost, 80);
  return boost;
}
// i18n-exempt-end

export type RankArgs = {
  draft: string; // current composer input, used as a prefix filter
  lastReply: string; // final text of the agent's most recent answer (B-1)
  locale: string; // "ja" | "en" (which seed language to use)
  hidden?: string[]; // keys the user deleted from the menu (settings.quickRepliesHidden)
  pinned?: string[]; // pinned = always shown first (settings.quickRepliesPinned, in pin order)
  limit?: number; // cap on the candidates the learned ranking returns (default 6). Pins sit outside
  // this cap, so however many are pinned they never squeeze the learned slots: the total can reach
  // the number of pins + limit.
};

// Compute and order the candidates, as an array of display texts: pins first in pin order, then the
// ranking.
export function rankQuickReplies(map: QuickReplyMap, args: RankArgs): string[] {
  const { draft, lastReply, locale, hidden, pinned, limit = 6 } = args;
  const seeds = SEEDS[locale] ?? SEEDS.ja;
  // Do not trust the stored hidden values as they are; run keyOf over them again so keys pushed in
  // their original width before the normalization changed still take effect. keyOf is idempotent,
  // so newer keys pass straight through.
  const hide = new Set((hidden ?? []).map((h) => keyOf(h)));
  const pins = (pinned ?? []).map((p) => normalize(p)).filter((p) => p);
  const pinKeys = new Set(pins.map((p) => keyOf(p)));
  // Merge the learned entries with the seeds not yet learned; on a key collision the seed is
  // dropped. MAX_LEN is applied here too, so lowering the threshold retroactively hides long
  // entries already learned. A hidden key is not revived by the seed either: hiding applies whether
  // or not the text was ever learned.
  const byKey = new Map<string, { text: string; count: number; at: number }>();
  for (const e of Object.values(map)) {
    if (normalize(e.text).length > MAX_LEN) continue;
    const k = keyOf(e.text);
    if (hide.has(k)) continue;
    if (pinKeys.has(k)) continue; // pins are emitted separately and first; do not list them twice
    const prev = byKey.get(k);
    // Fold the same sentence split across width-variant keys into one entry: counts summed,
    // spelling from the newer. record folds too, but this path fixes the display without
    // rewriting what is stored.
    byKey.set(
      k,
      prev
        ? { text: e.at >= prev.at ? e.text : prev.text, count: prev.count + e.count, at: Math.max(prev.at, e.at) }
        : { ...e },
    );
  }
  for (const s of seeds) {
    const k = keyOf(s);
    if (hide.has(k) || pinKeys.has(k)) continue;
    if (!byKey.has(k)) byKey.set(k, { text: normalize(s), count: 0, at: 0 });
  }

  // The prefix match also runs through keyOf, so "commit" is still offered when the prefix is typed
  // in the other width, and vice versa.
  const draftNorm = keyOf(draft);
  const lastReplyKey = keyOf(lastReply);
  const scored = [...byKey.values()]
    // While a draft is being typed, filter by prefix and drop a candidate equal to the draft
    // itself, which would offer nothing.
    .filter((e) => {
      const et = keyOf(e.text);
      if (draftNorm && !et.startsWith(draftNorm)) return false;
      if (draftNorm && et === draftNorm) return false;
      return true;
    })
    .map((e) => ({ text: e.text, score: e.count + contextBoost(e.text, lastReply, lastReplyKey), at: e.at }))
    // Score descending, ties broken by recency, then by shorter first: a short chip is easier to
    // hit, and it keeps the order from falling back to the unexplainable order of an Object's keys.
    .sort((a, b) => b.score - a.score || b.at - a.at || a.text.length - b.text.length);

  // Pins obey only the prefix match while typing (autocomplete), never the length cap, hiding or
  // the score.
  const head = pins.filter((p) => {
    const pt = keyOf(p);
    return !draftNorm || (pt.startsWith(draftNorm) && pt !== draftNorm);
  });
  // Pins sit outside the cap, so any number of them leaves the learned limit intact; the learned
  // side contributes the top-ranked entries up to limit.
  return [...head, ...scored.slice(0, limit).map((e) => e.text)];
}
