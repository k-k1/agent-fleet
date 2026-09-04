// --- Abbreviated reading of inline code ------------------------------------------
// Reading a fragment like `e79853e` out in full conveys nothing, so read only its head and
// replace the rest with a filler word. The filler is chosen deterministically from the token
// content: the same token always gets the same ending, which keeps the synthesis cache warm
// and makes a replay sound identical, while varying between tokens keeps it lively.
export const CODE_FILLERS = [
  "なんとか",
  "ふがふが",
  "むにゅむにゅ",
  "ごにょごにょ",
  "なんちゃら",
  "ほにゃらら",
  "もにょもにょ",
  "うにゃうにゃ",
  "なんとかかんとか",
  "もごもご",
];

// Abbreviation context handed to plainify / abbrevCode. dict is the user's reading
// dictionary: a token the dictionary matches is never abbreviated (the dictionary wins), and
// the substitution itself happens later in applyUserDict.
export interface CodeReadOpts {
  abbrev: boolean;
  dict: [string, string][];
}

const JA_CHAR = /[ぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/;

// codeWords extracts alphabetic words (2+ characters) using camelCase boundaries and
// separators. A token that yields no word (hash, version number, ...) counts as "nothing
// readable here".
function codeWords(token: string): string[] {
  return (
    token
      .replace(/([a-z0-9])([A-Z])/g, "$1 $2") // fooBar → foo Bar
      .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2") // TTSEnabled → TTS Enabled
      .match(/[A-Za-z]{2,}/g) ?? []
  );
}

// --- Detecting a bare hash --------------------------------------------------------
// Raw hashes not wrapped in backticks (f437e17 and friends) are abbreviated too. Since they
// sit in running prose, the match is narrowed hard to tokens that can only be a hex hash:
//  - lowercase hex only, 7+ characters (git's short-hash minimum)
//  - contains both a digit and a letter (letters only would swallow English words like
//    facade / deadbeef; digits only would mangle long numbers such as token counts and
//    timestamps, which should be read as-is)
//  - a UUID (8-4-4-4-12) is identifiable by structure alone, so it needs no digit/letter mix
// Applied in plainify's prose step, gated by the same ttsAbbrevCode setting as code
// fragments, and read through abbrevCode's hash branch (first 2 characters plus a filler,
// dictionary first).
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

export function isBareHash(t: string): boolean {
  if (UUID_RE.test(t)) return true;
  return /^[0-9a-f]{7,}$/.test(t) && /\d/.test(t) && /[a-f]/.test(t);
}

// Picks hash candidates out of prose: UUID first, then a hex run; isBareHash makes the final
// call. The trailing \b stops it from grabbing the head of an identifier that continues with
// letters after the hex, like deadbeef12ghost.
export const BARE_HASH_RE = /\b(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{7,})\b/g;

// abbrevCode decides how one piece of inline code is read aloud.
//  - short (<6 chars), a command/phrase containing whitespace, containing Japanese, or
//    matched by the user dictionary -> unchanged
//  - hash or UUID (isBareHash) -> first 2 characters plus a filler
//  - a single pure word (vitest, ...) -> unchanged
//  - two words (ttsEnabled, ...) -> first word plus a filler
//  - three or more words (ttsAutoReadMirror, paths, ...) -> first word, filler, last word
//  - no words at all (version numbers, ...) -> first 2 characters plus a filler
export function abbrevCode(token: string, dict: [string, string][] = []): string {
  const t = token.trim();
  if (t.length < 6) return token;
  if (/\s/.test(t)) return token;
  if (JA_CHAR.test(t)) return token;
  if (dict.some(([from]) => from && t.includes(from))) return token; // dictionary wins
  let sum = 0;
  for (const c of t) sum += c.codePointAt(0)!;
  const filler = CODE_FILLERS[sum % CODE_FILLERS.length];
  // Do not run word extraction on a hash: a long SHA's accidental letter runs look like words.
  if (isBareHash(t)) return `${t.slice(0, 2)} ${filler}`;
  const words = codeWords(t);
  if (words.length === 1 && words[0] === t) return token; // a single pure word
  if (words.length >= 3) return `${words[0]} ${filler} ${words[words.length - 1]}`;
  if (words.length === 2) return `${words[0]} ${filler}`;
  return `${t.slice(0, 2)} ${filler}`;
}

// --- Abbreviated reading of paths -------------------------------------------------
// Reading absolute (/a/b/c), relative (./a/b) and deep (a/b/c.ext) paths in full recites
// every directory and is unbearably verbose. Fold them to the first segment, a filler that
// sounds like skipping the middle, and the last segment (the file name) — e.g.
// console/src/features/chat/tts.ts becomes 「console、なんとかかんとか、tts.ts」. Wrapping the
// filler in Japanese commas produces natural pauses, so it sounds like an elided hierarchy.
// Dates (2024/01/02) and numeric runs are not folded. Gated by the same ttsAbbrevCode setting
// as inline code's abbrevCode (reaches plainify as code?.abbrev). Fold only at 3+ segments;
// two or fewer are short enough to read verbatim.
export const PATH_RE = /(?:\.{0,2}\/)?(?:[\w.-]+\/){2,}[\w.-]+/g;

// Fillers that convey "I skipped the middle levels". Chosen deterministically from the path,
// so the same path always sounds the same.
export const PATH_FILLERS = ["なんとかかんとか", "ずらずらっと", "うんぬんかんぬん", "ごにょごにょ"];

export function abbrevPath(path: string): string {
  const segs = path.split("/").filter((s) => s && s !== "." && s !== ".."); // drop empty, ./, ../
  if (segs.length <= 2) return path; // short paths stay as they are
  if (segs.every((s) => /^\d+$/.test(s))) return path; // dates and numeric runs (2024/01/02) never fold
  let sum = 0;
  for (const c of path) sum += c.codePointAt(0)!;
  const filler = PATH_FILLERS[sum % PATH_FILLERS.length];
  return `${segs[0]}、${filler}、${segs[segs.length - 1]}`;
}
