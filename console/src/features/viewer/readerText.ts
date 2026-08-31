// features/viewer/readerText — 朗読ビュー（docs/log/24）用のテキスト整形（純ロジック、依存は
// ttsText の plainify のみ）。**原文の改行・行頭スペースを保持**しつつ、**なろう形式のルビ**を
// 解釈して表示用セグメントに割り、読み上げ用テキスト（ルビは読みを採用）を作る。
// カラオケ・ハイライトの単位＝「行内は文（句点区切り）、行末で区切り」。node の vitest でテスト可。
import { plainify, startsBlock, startsTame, type CodeReadOpts } from "../chat/ttsText.ts";

// RubySeg は表示の 1 かたまり。ruby があれば <ruby>base<rt>ruby</rt></ruby>、無ければ素の base。
// base には空白・改行をそのまま含める（原文忠実表示のため）。
export interface RubySeg {
  base: string;
  ruby?: string;
}

// ReadUnit はカラオケ 1 単位。segs=表示（原文の空白/改行/ルビを保持）、spoken=読み上げ
// テキスト（ルビは読みを採用・plainify 済み・trim）。spoken==="" の単位は表示のみ（読まない）。
// preBeat=リスト・見出し・引用など「新しいブロックの頭」の最初の文（読む前に一拍おく合図。
// マーカー記号は読まないぶん、構造の切れ目を間で表す）。tameBeat=行頭が溜め（――・……等）
// の最初の文（preBeat より長い前拍で「一拍おいてから話す」演出を再現。startsTame 参照）。
// lineHead=原文の新しい行の最初の読み上げ単位（readPreGaps が段落の切れ目とハードラップを
// 見分けるのに使う）。
export interface ReadUnit {
  segs: RubySeg[];
  spoken: string;
  preBeat?: boolean;
  tameBeat?: boolean;
  lineHead?: boolean;
}

const RUBY_OPEN = "《";
const RUBY_CLOSE = "》";
const RUBY_MARK = "｜"; // 全角のみ（半角 | は Markdown 表と衝突するため使わない）

// 漢字（自動ルビの base 判定用）。CJK 統合漢字＋拡張A＋互換＋々。
function isKanji(ch: string): boolean {
  return /[々㐀-䶿一-鿿豈-﫿]/.test(ch);
}

// parseRuby は 1 行をなろう形式ルビで表示セグメントへ割る。
//  - `｜親文字《ルビ》`：｜ 以降 《 までを base、《》内を ruby。
//  - `漢字《ルビ》`（｜省略）：《 直前の漢字連続を base、その手前は素の文字列。
//  - 対応する《》が無い / 直前に漢字が無い《》は素の文字として扱う。
export function parseRuby(line: string): RubySeg[] {
  const segs: RubySeg[] = [];
  const rs = Array.from(line); // コードポイント単位
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
      // 対応する《》なし → ｜ は素の文字として残す
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
        // 直前に漢字が無い → 素の《》として扱う
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

// 傍点（圏点）用のルビ記号。ルビがこれらだけなら「読み」ではなく強調なので、読み上げは
// 親文字（base）を読む（例 ｜イ《・》｜カ《・》→「イカ」）。表示はルビ（点）のまま。
const EMPHASIS_MARKS = new Set(["・", "･", "•", "·", "﹅", "﹆", "●", "○", "◎", "、"]);
function isEmphasisRuby(ruby: string | undefined): boolean {
  if (!ruby) return false;
  for (const ch of ruby) if (!EMPHASIS_MARKS.has(ch)) return false;
  return true;
}

// 読み上げ対象になる文字（かな/漢字/英数字）を 1 つでも含むか。含まなければ記号だけの行＝
// 視点切り替えの ＊ / ◇ / --- 等の区切りなので読まずに飛ばす（表示はする）。
function hasSpeakable(text: string): boolean {
  return /[0-9A-Za-zぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/.test(text);
}

// 1 セグメントの読み上げ文字列。傍点ルビは親文字、通常ルビは読み、素の文字はそのまま。
function spokenOf(s: RubySeg): string {
  if (isEmphasisRuby(s.ruby)) return s.base;
  return s.ruby !== undefined ? s.ruby : s.base;
}

// buildReadUnits は本文を「行内は文、行末で区切り」の ReadUnit 列へ。原文の改行・行頭スペース・
// ルビはすべて表示側（segs）に保持する。Markdown のコードフェンス内は表示するが読み上げない。
// code を渡すとインラインコード（`…`）を省略読みにする（plainify に伝搬。表示は原文のまま）。
// ruby=false（UI ロケールが非 ja のとき）は なろう形式ルビの解釈を無効化し、《》｜ を素の文字として
// 扱う（日本語専用機能のロケールゲート・docs/log/28 §2.4）。
export function buildReadUnits(content: string, isMarkdown: boolean, code?: CodeReadOpts, ruby = true): ReadUnit[] {
  const units: ReadUnit[] = [];
  const lines = content.split("\n");
  let inFence = false;

  for (let li = 0; li < lines.length; li++) {
    const line = lines[li].replace(/\r$/, ""); // \r\n 対応
    const hasNL = li < lines.length - 1; // 最終行以外は末尾に改行があった

    const fenceMarker = isMarkdown && line.trimStart().startsWith("```");
    if (fenceMarker) inFence = !inFence;
    const skipSpoken = isMarkdown && (inFence || fenceMarker); // フェンス行は読まない
    // ブロック頭の行（リスト・見出し・引用）: この行から生まれる最初の読み上げ単位に前拍を付ける。
    let linePre = isMarkdown && !skipSpoken && startsBlock(line);
    // 溜め（――・……等）で始まる行: markdown 記法ではなく地の文の演出なので isMarkdown は問わない。
    let lineTame = !skipSpoken && startsTame(line);
    // この行の最初の読み上げ単位に「新しい行の頭」の印を付ける（先頭行は除く）。
    let lineHeadPending = li > 0;

    let cur: RubySeg[] = [];
    const flush = (lineEnd: boolean) => {
      if (lineEnd && hasNL) {
        // 改行は表示に保持。文の切れ目で行が終わった（cur 空）ときは直前の単位に
        // ぶら下げ、空単位を作らない。先頭が空行のときだけ独立した改行単位になる。
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
      if (spoken && !hasSpeakable(spoken)) spoken = ""; // 記号だけ（＊/◇/--- 等の区切り）は読まない
      const preBeat = !!spoken && linePre; // 行の最初の読み上げ単位にだけ付ける
      if (preBeat) linePre = false;
      const tameBeat = !!spoken && lineTame;
      if (tameBeat) lineTame = false;
      const lineHead = !!spoken && lineHeadPending;
      if (lineHead) lineHeadPending = false;
      units.push({ segs: disp, spoken, preBeat, tameBeat, lineHead });
    };

    for (const seg of ruby ? parseRuby(line) : [{ base: line }]) {
      if (seg.ruby !== undefined) {
        cur.push(seg); // ルビは分割しない
        continue;
      }
      // 素の文字列は句点で文に割る（句点は前の文に含める）。
      let buf = "";
      for (const c of seg.base) {
        buf += c;
        if (SENTENCE_ENDERS.includes(c)) {
          cur.push({ base: buf });
          buf = "";
          flush(false); // 行内の文境界
        }
      }
      if (buf) cur.push({ base: buf });
    }
    flush(true); // 行末
  }
  return units;
}

// 文の終わり（句点類。閉じ括弧・閉じ鉤括弧が続いてもよい）で終わっているか。
const SENT_ENDED = /[。．！？!?][」』）)】]*$/;

// readPreGaps は読み上げ単位（spoken 非空のみ、buildReadUnits の出力順）ごとの前拍（秒）を
// 返す。段階: 溜め行（tameBeat）が最優先で tameBeat 秒、マーカー行（preBeat）と
// 「文が終わったあとの新しい行」（段落・行の切れ目）は blockBeat、行内の文境界（。区切り）は
// 短い sentBeat、文の途中の改行（ハードラップされた散文）は 0（間を入れると文が途切れて
// 聞こえる）。ReaderView が startNarration の preGaps に渡す。
export function readPreGaps(units: ReadUnit[], blockBeat: number, sentBeat: number, tameBeat: number): number[] {
  const spoken = units.filter((u) => u.spoken);
  return spoken.map((u, i) => {
    if (i === 0) return 0; // 先頭の前拍は開始遅延になるだけ
    if (u.tameBeat) return tameBeat;
    if (u.preBeat) return blockBeat;
    const prevEnded = SENT_ENDED.test(spoken[i - 1].spoken);
    if (u.lineHead) return prevEnded ? blockBeat : 0;
    return prevEnded ? sentBeat : 0;
  });
}
