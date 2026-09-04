// --- User reading dictionary (literal spelling -> reading substitution) ----------
// Applies the ttsUserDict setting (one "spelling=reading" per line) to the text prepared for
// reading, by plain string substitution. It works for English, Japanese and symbols alike, and
// runs ahead of enkana (English -> katakana), so the user's reading wins whether enkana is on or
// off. Same idea as VOICEVOX's own user dictionary.

// parseUserDict turns the setting string into an array of [spelling, reading]. Blank lines and
// lines starting with # are comments; the separator is = in either ASCII or full width; lines
// with an empty spelling are dropped. Sorted by descending spelling length so that longer
// spellings are applied first.
export function parseUserDict(raw: string): [string, string][] {
  if (!raw) return [];
  const pairs: [string, string][] = [];
  for (const line of raw.split(/\r?\n/)) {
    const t = line.trim();
    if (!t || t.startsWith("#")) continue;
    const eq = t.search(/[=＝]/);
    if (eq <= 0) continue; // no separator, or an empty spelling
    const from = t.slice(0, eq).trim();
    const to = t.slice(eq + 1).trim();
    if (!from) continue; // an empty reading is allowed: it skips that word
    pairs.push([from, to]);
  }
  pairs.sort((a, b) => b[0].length - a[0].length); // apply longest first, so a partial match cannot shadow it
  return pairs;
}

// applyUserDict applies the dictionary to the text as literal substitution: every occurrence,
// longest spelling first. It uses split/join, so no regexp escaping is needed. dict is expected
// to be the output of parseUserDict.
export function applyUserDict(text: string, dict: [string, string][]): string {
  let out = text;
  for (const [from, to] of dict) {
    if (from) out = out.split(from).join(to);
  }
  return out;
}

// mergeDicts combines the user dictionary with the tenant-wide one. For the same spelling the
// user's entry wins, including an override with an empty reading that skips the word. The result
// is re-sorted by descending spelling length to preserve the "longest spelling first"
// precondition of applyUserDict and abbrevCode.
export function mergeDicts(user: [string, string][], tenant: [string, string][]): [string, string][] {
  if (!tenant.length) return user;
  const seen = new Set(user.map(([from]) => from));
  const out = [...user, ...tenant.filter(([from]) => !seen.has(from))];
  out.sort((a, b) => b[0].length - a[0].length);
  return out;
}

// --- Cutting start latency by emitting the first sentence early ------------------
// Waiting for a long first sentence to reach its full stop delays the start of speech. For the
// first utterance only, do not wait for the full stop: cut at a light break such as a comma
// (once there is enough text), or at a fixed length if no break arrives. From the second
// sentence on, tts.ts keeps cutting at full-stop granularity, so nothing is chopped too finely.
const FIRST_MIN = 10; // minimum length for the early cut, below which nothing is cut
const FIRST_MAX = 28; // for the first cut only, force a cut at this length even without a break
const EARLY_BREAK = /[、，,；;）」』】]/; // light breaks for the early cut (commas, closing brackets)

// firstChunkCut returns the cut position (1-origin end index) for emitting the first utterance
// early, or -1 for no cut. It cuts at a comma or similar at or after FIRST_MIN, and otherwise
// forces a cut at FIRST_MAX.
export function firstChunkCut(buf: string): number {
  const m = buf.match(EARLY_BREAK);
  if (m && m.index! + 1 >= FIRST_MIN) return m.index! + 1;
  if (buf.length >= FIRST_MAX) return FIRST_MAX;
  return -1;
}
