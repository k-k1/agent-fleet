// features/chat/ttsText — 読み上げ用テキスト整形・分割（純ロジック、依存なし）。
// Markdown 記法・コードブロック・リンク・URL を落として TTS に渡せる素のテキストにする。
// tts.ts から使う。ブラウザ API に触れないので node の vitest で直接テストできる。

// 実体は parts/ の 3 枚＋このファイルに分かれている（依存は一方向）:
//   ttsUserDict（ユーザー/テナント辞書の適用・早出しの切り出し）
//   ttsAbbrev（インラインコード・裸のハッシュ・パスの省略読み）
//   ttsReadings（組み込みの読み補正の表と適用・助詞の小休止・ブロック頭/溜めの判定）
//   ここ（保留質問の読み上げ文・感情推定・文分割・plainify）
// 呼び出し側は分割前と同じく "features/chat/ttsText.ts" から import する。
import { abbrevCode, abbrevPath, isBareHash, BARE_HASH_RE, PATH_RE, type CodeReadOpts } from "./parts/ttsAbbrev.ts";

export * from "./parts/ttsUserDict.ts";
export * from "./parts/ttsAbbrev.ts";
export * from "./parts/ttsReadings.ts";

// --- 保留中の質問（AskUserQuestion）の読み上げ文 ---------------------------------
// ミラーが確認待ちになったとき、質問文と選択肢を音声用の 1 本のテキストに組む。選択肢は
// 画面の表示ラベル（短縮されがち）ではなく **説明文（ツールチップの中身）を優先**して読む。
// question/option は Markdown 断片のことがあるので plainify を通す。
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

// --- 文の感情推定（感情スタイル読み分け用） --------------------------------------
// 文にエラー・失敗系の語があれば "angry"（ツンツン系スタイル）、成功・完了系なら
// "happy"（あまあま系）、どちらも無ければ null（ノーマル）。読み上げ済みテキスト
// （プレーン化後の 1 文）に対するキーワード判定で、angry を優先する（失敗の報告に
// 成功語が混ざることはあっても逆は稀なため）。
const ANGRY_WORDS = ["エラー", "失敗", "例外", "できませんでした", "落ちました", "error", "fail"];
const HAPPY_WORDS = ["成功", "完了", "できました", "通りました", "問題ありません", "green", "passed", "✅", "🎉"];

export function emotionOf(text: string): "happy" | "angry" | null {
  const t = text.toLowerCase();
  for (const w of ANGRY_WORDS) if (t.includes(w)) return "angry";
  for (const w of HAPPY_WORDS) if (t.includes(w)) return "happy";
  return null;
}

// --- レンダ済みテキストの文分割（ミラーのカラオケ朗読用） ------------------------
// textContent 由来のテキスト（Markdown 記法は既に落ちている）を文単位に割る。句点は前の
// 文に含める。改行・連続空白は 1 つの空白に潰し、かな/漢字/英数字を 1 つも含まない断片
// （罫線・記号だけ等）は捨てる。
const SENT_END = "。．！？!?";
const SPEAKABLE = /[0-9A-Za-zぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/;

// --- 長文の合成用分割 --------------------------------------------------------------
// 句点で切った「1 文」が長すぎるとき、合成用にさらに弱い区切り（読点・中黒・スラッシュ・
// ダッシュ・閉じ括弧など）で割る。合成 1 回が長いと CPU エンジンの合成時間がそのまま
// 無音の待ちになる（先読みの息切れ）ため、開始レイテンシとパイプラインの持続性を優先する。
// 区切りは前の片に含める。max までに区切りが無ければ長さで強制分割。呼び手（tts.ts の
// submit / turnTts / ReaderView）は途中の片の間を詰めて連続再生し、ハイライトは元の文の
// 単位のまま扱う。
const SENT_SPLIT_BREAK = "、，・；：／—–」』）】";
const SPLIT_HEAD_MIN = 8; // 先頭 8 文字未満では切らない（細切れ防止）

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
    if (cut < 0) cut = max; // 区切りが無い → 長さで強制分割
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

// plainifyStreaming — 1 文分のテキストを読み上げ用にプレーン化。```fence``` は
// またぎ状態（inFence）を引き回して内側を丸ごと落とす。
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

// plainify — Markdown 記法・リンク・URL・記号を落として読み上げ用テキストにする。
// fence の除去は plainifyStreaming が済ませている前提。code を渡すとインラインコードを
// 省略読み（abbrevCode）にする（未指定は従来どおり中身をそのまま）。
export function plainify(s: string, code?: CodeReadOpts): string {
  return (
    s
      // インラインコード `x` → x（省略読み有効時は abbrevCode で頭＋フィラーに）
      .replace(/`([^`]*)`/g, (_, p: string) => (code?.abbrev ? abbrevCode(p, code.dict) : p))
      // 画像 ![alt](url) → 落とす
      .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
      // リンク [text](url) → text
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      // 裸の URL は読まない
      .replace(/https?:\/\/\S+/g, "")
      // 裸のパス（バッククォート無しの /a/b/c.ts, ./src/foo 等）は頭＋末尾に畳む
      .replace(PATH_RE, (m) => (code?.abbrev ? abbrevPath(m) : m))
      // 裸のハッシュ（バッククォート無しの f437e17 等）もコード片と同じ省略読みに
      .replace(BARE_HASH_RE, (m) => (code?.abbrev && isBareHash(m) ? abbrevCode(m, code.dict) : m))
      // 行頭の見出し/引用/リストマーカー
      .replace(/^\s{0,3}(#{1,6}\s+|>\s+|[-*+]\s+|\d+\.\s+)/gm, "")
      // 行頭の溜め（――・……等）はマーカーなので読まない（間は preGaps 側の TAME_BEAT で表現）
      .replace(/^\s*(?:[—–―]+|\.{3,}|…+)(?=[^\s—–―.…])/gm, "")
      // 強調・打ち消し
      .replace(/(\*\*|__|~~|\*|_)(.*?)\1/g, "$2")
      // 水平線
      .replace(/^\s*([-*_]\s*){3,}$/gm, "")
      // 余分な空白の圧縮
      .replace(/[ \t]+/g, " ")
      .trim()
  );
}
