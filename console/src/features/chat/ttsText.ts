// features/chat/ttsText - text shaping and splitting for read-aloud (pure logic, no dependencies).
// Strips Markdown syntax, code blocks, links and URLs down to plain text TTS can take. Used from
// tts.ts. It touches no browser API, so node's vitest can test it directly.

// The implementation is split across three files under parts/ plus this one; dependencies run one
// way:
//   ttsUserDict (applying the user/tenant dictionary, cutting entries out early)
//   ttsAbbrev (abbreviated reading of inline code, bare hashes and paths)
//   ttsReadings (the built-in reading-correction table and its application, particle micro-pauses,
//     detecting block heads and held beats)
//   here (spoken text for a pending question, emotion guess, sentence splitting, plainify)
// Callers still import from "features/chat/ttsText.ts", as they did before the split.
import { abbrevCode, abbrevPath, isBareHash, BARE_HASH_RE, PATH_RE, type CodeReadOpts } from "./parts/ttsAbbrev.ts";

export * from "./parts/ttsUserDict.ts";
export * from "./parts/ttsAbbrev.ts";
export * from "./parts/ttsReadings.ts";

// --- Spoken text for a pending question (AskUserQuestion) ------------------------
// When the mirror starts waiting for confirmation, the question and its options are assembled into
// one text for speech. An option is read from its description (the tooltip body) in preference to
// the on-screen label, which is often abbreviated. question/option can be Markdown fragments, so
// they go through plainify.
export interface SpokenQuestion {
  question?: string;
  multiSelect?: boolean;
  options?: { label: string; description?: string }[];
}

export function pendingSpeech(qs: SpokenQuestion[]): string {
  const parts: string[] = ["確認です。"];
  const period = (t: string) => (/[。．！？!?]$/.test(t) ? t : t + "。");
  qs.forEach((q, qi) => {
    if (qs.length > 1) parts.push(`質問${qi + 1}。`);
    const question = q.question ? plainify(q.question).trim() : "";
    if (question) parts.push(period(question));
    const opts = q.options || [];
    if (opts.length) {
      parts.push(`選択肢は${opts.length}つ。`);
      opts.forEach((o, oi) => {
        const alt = plainify(o.description || o.label).trim();
        if (alt) parts.push(`${oi + 1}、${period(alt)}`);
      });
    }
    if (q.multiSelect) parts.push("複数選択できます。");
  });
  return parts.join("");
}

// --- Guessing a sentence's emotion, to pick an emotion style ----------------------
// A sentence with error/failure words reads as "angry" (the sharper styles), one with
// success/completion words as "happy" (the sweeter ones); neither gives null (normal). The test is
// keyword matching over one already-plainified sentence, and angry wins - a failure report often
// carries success words, rarely the other way round.
const ANGRY_WORDS = ["エラー", "失敗", "例外", "できませんでした", "落ちました", "error", "fail"];
const HAPPY_WORDS = ["成功", "完了", "できました", "通りました", "問題ありません", "green", "passed", "✅", "🎉"];

export function emotionOf(text: string): "happy" | "angry" | null {
  const t = text.toLowerCase();
  for (const w of ANGRY_WORDS) if (t.includes(w)) return "angry";
  for (const w of HAPPY_WORDS) if (t.includes(w)) return "happy";
  return null;
}

// --- Sentence splitting for rendered text (the mirror's karaoke reading) ----------
// Splits text taken from textContent (Markdown syntax is already gone) into sentences. The full
// stop stays with the sentence before it, newlines and runs of whitespace collapse to one space,
// and a fragment holding no kana, kanji or alphanumeric at all (rules, symbols only) is dropped.
const SENT_END = "。．！？!?";
const SPEAKABLE = /[0-9A-Za-zぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/;

// --- Splitting a long sentence for synthesis ----------------------------------------
// When a "sentence" cut at a full stop is too long, split it further for synthesis at weaker breaks
// (comma, middle dot, slash, dash, closing bracket). A single long synthesis call turns a CPU
// engine's synthesis time straight into silent waiting - the read-ahead runs out of breath - so
// start latency and keeping the pipeline fed win. The break stays with the preceding piece; with no
// break before max, the split is forced by length. Callers (submit / turnTts in tts.ts, ReaderView)
// play the pieces back-to-back with the gaps closed and still highlight by the original sentence.
const SENT_SPLIT_BREAK = "、，・；：／—–」』）】";
const SPLIT_HEAD_MIN = 8; // never cut inside the first 8 characters, to avoid shredding

export function splitLongSentence(s: string, max = 60): string[] {
  const out: string[] = [];
  let rest = s;
  while (rest.length > max) {
    let cut = -1;
    for (let i = max; i >= SPLIT_HEAD_MIN; i--) {
      if (SENT_SPLIT_BREAK.includes(rest[i])) {
        cut = i + 1;
        break;
      }
    }
    if (cut < 0) cut = max; // no break found -> force the split by length
    out.push(rest.slice(0, cut));
    rest = rest.slice(cut);
  }
  if (rest) out.push(rest);
  return out;
}

export function splitSentences(text: string): string[] {
  const out: string[] = [];
  let buf = "";
  const flush = () => {
    const t = buf.replace(/\s+/g, " ").trim();
    buf = "";
    if (t && SPEAKABLE.test(t)) out.push(t);
  };
  for (const c of text) {
    buf += c;
    if (SENT_END.includes(c)) flush();
  }
  flush();
  return out;
}

// plainifyStreaming - plainifies one sentence's worth of text for reading. A ```fence``` is tracked
// across calls through the carried state (inFence), so its whole body is dropped.
export function plainifyStreaming(
  s: string,
  fence: { get: () => boolean; set: (v: boolean) => void },
  code?: CodeReadOpts,
): string {
  const out: string[] = [];
  let rest = s;
  while (rest.length) {
    const i = rest.indexOf("```");
    if (i < 0) {
      out.push(fence.get() ? "" : rest);
      rest = "";
      break;
    }
    if (!fence.get()) out.push(rest.slice(0, i));
    fence.set(!fence.get());
    rest = rest.slice(i + 3);
  }
  return plainify(out.join(""), code);
}

// plainify - strips Markdown syntax, links, URLs and symbols down to text for reading. It assumes
// plainifyStreaming has already removed the fences. Passing code makes inline code read abbreviated
// (abbrevCode); omitted, the body is read as-is.
export function plainify(s: string, code?: CodeReadOpts): string {
  return (
    s
      // inline code `x` -> x (with abbreviation on, abbrevCode gives head + filler)
      .replace(/`([^`]*)`/g, (_, p: string) => (code?.abbrev ? abbrevCode(p, code.dict) : p))
      // image ![alt](url) -> dropped
      .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
      // link [text](url) -> text
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      // bare URLs are not read
      .replace(/https?:\/\/\S+/g, "")
      // bare paths (no backticks: /a/b/c.ts, ./src/foo) fold to head + tail
      .replace(PATH_RE, (m) => (code?.abbrev ? abbrevPath(m) : m))
      // a bare hash (no backticks: f437e17) gets the same abbreviated reading as code
      .replace(BARE_HASH_RE, (m) => (code?.abbrev && isBareHash(m) ? abbrevCode(m, code.dict) : m))
      // heading / quote / list markers at line start
      .replace(/^\s{0,3}(#{1,6}\s+|>\s+|[-*+]\s+|\d+\.\s+)/gm, "")
      // a held beat at line start is a marker, not read; the pause comes from TAME_BEAT in preGaps
      .replace(/^\s*(?:[—–―]+|\.{3,}|…+)(?=[^\s—–―.…])/gm, "")
      // emphasis and strikethrough
      .replace(/(\*\*|__|~~|\*|_)(.*?)\1/g, "$2")
      // horizontal rule
      .replace(/^\s*([-*_]\s*){3,}$/gm, "")
      // collapse redundant whitespace
      .replace(/[ \t]+/g, " ")
      .trim()
  );
}
