// features/viewer/readerText — text shaping for the read-aloud view (docs/log/24). Pure logic;
// the only dependency is plainify from ttsText. Preserves the source's newlines and leading
// spaces while interpreting Narou-style ruby (「なろう形式のルビ」) into display segments and
// building the spoken text (a ruby annotation is read in place of its base characters).
// The karaoke highlight unit is "one sentence within a line, cut again at the line end".
import { plainify, startsBlock, startsTame, type CodeReadOpts } from "../chat/ttsText.ts";

// RubySeg is one display chunk: with `ruby` it renders as <ruby>base<rt>ruby</rt></ruby>,
// without it as bare base. `base` keeps spaces and newlines verbatim so the source renders
// faithfully.
export interface RubySeg {
  base: string;
  ruby?: string;
}

// ReadUnit is one karaoke unit. segs = the display side (source spaces/newlines/ruby kept),
// spoken = the narration text (ruby reading substituted, plainified, trimmed). A unit with
// spoken === "" is displayed but not read.
// preBeat marks the first sentence of a new block (list, heading, quote): the marker glyphs are
// not spoken, so the structural break is expressed as a pause instead. tameBeat marks the first
// sentence of a line starting with a suspension run (――, ……); it takes a longer pre-beat than
// preBeat to reproduce the "pause, then speak" effect (see startsTame). lineHead marks the first
// spoken unit of a new source line, which readPreGaps uses to tell a paragraph break from a hard
// wrap.
export interface ReadUnit {
  segs: RubySeg[];
  spoken: string;
  preBeat?: boolean;
  tameBeat?: boolean;
  lineHead?: boolean;
}

const RUBY_OPEN = "《";
const RUBY_CLOSE = "》";
const RUBY_MARK = "｜"; // Fullwidth only: ASCII | would collide with Markdown tables.

// Kanji test, used to find the base of an implicit ruby. CJK unified + ext A + compat + 々.
function isKanji(ch: string): boolean {
  return /[々㐀-䶿一-鿿豈-﫿]/.test(ch);
}

// parseRuby splits one line into display segments along Narou-style ruby notation.
//  - `｜base《ruby》`: from ｜ up to 《 is the base, the 《》 content is the ruby.
//  - `kanji《ruby》` (｜ omitted): the run of kanji right before 《 is the base, the rest is plain.
//  - An unmatched 《》, or one with no kanji before it, is treated as plain characters.
export function parseRuby(line: string): RubySeg[] {
  const segs: RubySeg[] = [];
  const rs = Array.from(line); // by code point
  let buf = "";
  const flushPlain = () => {
    if (buf) {
      segs.push({ base: buf });
      buf = "";
    }
  };
  let i = 0;
  while (i < rs.length) {
    const ch = rs[i];
    if (ch === RUBY_MARK) {
      const open = rs.indexOf(RUBY_OPEN, i + 1);
      const close = open !== -1 ? rs.indexOf(RUBY_CLOSE, open + 1) : -1;
      if (open !== -1 && close !== -1) {
        flushPlain();
        segs.push({ base: rs.slice(i + 1, open).join(""), ruby: rs.slice(open + 1, close).join("") });
        i = close + 1;
        continue;
      }
      // No matching 《》: keep ｜ as a plain character.
      buf += ch;
      i++;
      continue;
    }
    if (ch === RUBY_OPEN) {
      const close = rs.indexOf(RUBY_CLOSE, i + 1);
      if (close !== -1) {
        const b = Array.from(buf);
        let k = b.length;
        while (k > 0 && isKanji(b[k - 1])) k--;
        if (k < b.length) {
          buf = "";
          const prefix = b.slice(0, k).join("");
          if (prefix) segs.push({ base: prefix });
          segs.push({ base: b.slice(k).join(""), ruby: rs.slice(i + 1, close).join("") });
          i = close + 1;
          continue;
        }
        // No kanji before it: treat 《》 as plain characters.
      }
      buf += ch;
      i++;
      continue;
    }
    buf += ch;
    i++;
  }
  flushPlain();
  return segs;
}

const SENTENCE_ENDERS = "。．！？!?";

// Ruby glyphs used for emphasis dots. A ruby made only of these is emphasis, not a reading, so
// narration reads the base instead (｜イ《・》｜カ《・》 is spoken as "イカ"); the display keeps
// the dots.
const EMPHASIS_MARKS = new Set(["・", "･", "•", "·", "﹅", "﹆", "●", "○", "◎", "、"]);
function isEmphasisRuby(ruby: string | undefined): boolean {
  if (!ruby) return false;
  for (const ch of ruby) if (!EMPHASIS_MARKS.has(ch)) return false;
  return true;
}

// Whether the text holds at least one speakable character (kana, kanji, alphanumeric). Without
// one it is a symbols-only line — a ＊ / ◇ / --- scene divider — which is displayed but skipped.
function hasSpeakable(text: string): boolean {
  return /[0-9A-Za-zぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/.test(text);
}

// Spoken text of one segment: an emphasis ruby reads its base, a normal ruby reads the ruby, and
// a plain segment reads as-is.
function spokenOf(s: RubySeg): string {
  if (isEmphasisRuby(s.ruby)) return s.base;
  return s.ruby !== undefined ? s.ruby : s.base;
}

// buildReadUnits turns the body into ReadUnits cut at sentence boundaries within a line and again
// at each line end. Source newlines, leading spaces and ruby all survive on the display side
// (segs). Markdown code fences are displayed but not spoken. Passing `code` makes inline code
// (`…`) read in abbreviated form (forwarded to plainify; the display is unchanged).
// ruby=false (when the UI locale is not ja) disables Narou-style ruby parsing and treats 《》｜ as
// plain characters — the locale gate for a Japanese-only feature (docs/log/28 §2.4).
export function buildReadUnits(content: string, isMarkdown: boolean, code?: CodeReadOpts, ruby = true): ReadUnit[] {
  const units: ReadUnit[] = [];
  const lines = content.split("\n");
  let inFence = false;

  for (let li = 0; li < lines.length; li++) {
    const line = lines[li].replace(/\r$/, ""); // \r\n
    const hasNL = li < lines.length - 1; // every line but the last ended with a newline

    const fenceMarker = isMarkdown && line.trimStart().startsWith("```");
    if (fenceMarker) inFence = !inFence;
    const skipSpoken = isMarkdown && (inFence || fenceMarker); // fence lines are not spoken
    // Block-head line (list, heading, quote): give the first spoken unit born from it a pre-beat.
    let linePre = isMarkdown && !skipSpoken && startsBlock(line);
    // Line starting with a suspension run (――, ……): prose styling rather than Markdown syntax,
    // so isMarkdown does not gate it.
    let lineTame = !skipSpoken && startsTame(line);
    // Mark the first spoken unit of this line as a new line head (not for the very first line).
    let lineHeadPending = li > 0;

    let cur: RubySeg[] = [];
    const flush = (lineEnd: boolean) => {
      if (lineEnd && hasNL) {
        // The newline stays in the display. When the line ended exactly on a sentence boundary
        // (cur empty) hang it off the previous unit rather than creating an empty one; only a
        // leading blank line becomes a newline unit of its own.
        if (cur.length) cur.push({ base: "\n" });
        else if (units.length) {
          units[units.length - 1].segs.push({ base: "\n" });
          return;
        } else cur.push({ base: "\n" });
      }
      if (!cur.length) return;
      const disp = cur;
      cur = [];
      const raw = skipSpoken ? "" : disp.map(spokenOf).join("");
      let spoken = raw ? plainify(raw, code).trim() : "";
      if (spoken && !hasSpeakable(spoken)) spoken = ""; // symbols only (＊/◇/--- dividers)
      const preBeat = !!spoken && linePre; // only on the line's first spoken unit
      if (preBeat) linePre = false;
      const tameBeat = !!spoken && lineTame;
      if (tameBeat) lineTame = false;
      const lineHead = !!spoken && lineHeadPending;
      if (lineHead) lineHeadPending = false;
      units.push({ segs: disp, spoken, preBeat, tameBeat, lineHead });
    };

    for (const seg of ruby ? parseRuby(line) : [{ base: line }]) {
      if (seg.ruby !== undefined) {
        cur.push(seg); // never split a ruby
        continue;
      }
      // Plain text is cut into sentences at the sentence enders, which stay with the sentence.
      let buf = "";
      for (const c of seg.base) {
        buf += c;
        if (SENTENCE_ENDERS.includes(c)) {
          cur.push({ base: buf });
          buf = "";
          flush(false); // sentence boundary inside the line
        }
      }
      if (buf) cur.push({ base: buf });
    }
    flush(true); // line end
  }
  return units;
}

// Does the text end on a sentence ender? Closing brackets and quotes may follow it.
const SENT_ENDED = /[。．！？!?][」』）)】]*$/;

// readPreGaps returns the pre-beat in seconds for each spoken unit (non-empty `spoken` only, in
// buildReadUnits order). Precedence: a suspension line (tameBeat) wins and gets tameBeat seconds;
// a marker line (preBeat) and a new line that follows a finished sentence (a paragraph or line
// break) get blockBeat; a sentence boundary inside a line gets the shorter sentBeat; a newline in
// mid-sentence (hard-wrapped prose) gets 0, because a pause there makes the sentence sound broken.
// ReaderView passes the result to startNarration as preGaps.
export function readPreGaps(units: ReadUnit[], blockBeat: number, sentBeat: number, tameBeat: number): number[] {
  const spoken = units.filter((u) => u.spoken);
  return spoken.map((u, i) => {
    if (i === 0) return 0; // a pre-beat on the first unit is just start-up latency
    if (u.tameBeat) return tameBeat;
    if (u.preBeat) return blockBeat;
    const prevEnded = SENT_ENDED.test(spoken[i - 1].spoken);
    if (u.lineHead) return prevEnded ? blockBeat : 0;
    return prevEnded ? sentBeat : 0;
  });
}
