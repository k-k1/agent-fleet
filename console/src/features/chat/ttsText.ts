// features/chat/ttsText — 読み上げ用テキスト整形・分割（純ロジック、依存なし）。
// Markdown 記法・コードブロック・リンク・URL を落として TTS に渡せる素のテキストにする。
// tts.ts から使う。ブラウザ API に触れないので node の vitest で直接テストできる。

// --- 開始レイテンシ短縮（最初の 1 文だけ早出し） --------------------------------
// 長い第 1 文が句点で終わるまで待つと発話開始が遅い。**最初の発話に限り**句点を待たず、
// 読点などの軽い区切り（十分な長さがあれば）か、区切りが来なくても一定長で切り出して
// 鳴らし始める。2 文目以降は tts.ts が従来どおり句点粒度で切る（過度な細切れを避ける）。
const FIRST_MIN = 10; // 早出しの最小長（これ未満では切らない＝出だしが細切れにならない）
const FIRST_MAX = 28; // 区切りが来なくてもこの長さで最初だけ強制的に切る
const EARLY_BREAK = /[、，,；;）」』】]/; // 早出しの軽い区切り（読点・閉じ括弧類）

// firstChunkCut は「最初の発話」を早出しするための切り出し位置（1-origin の終端 index）を返す。
// 切らない場合は -1。読点等が FIRST_MIN 以降にあればそこで、無ければ FIRST_MAX で強制的に切る。
export function firstChunkCut(buf: string): number {
  const m = buf.match(EARLY_BREAK);
  if (m && m.index! + 1 >= FIRST_MIN) return m.index! + 1;
  if (buf.length >= FIRST_MAX) return FIRST_MAX;
  return -1;
}

// plainifyStreaming — 1 文分のテキストを読み上げ用にプレーン化。```fence``` は
// またぎ状態（inFence）を引き回して内側を丸ごと落とす。
export function plainifyStreaming(
  s: string,
  fence: { get: () => boolean; set: (v: boolean) => void },
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
  return plainify(out.join(""));
}

// plainify — Markdown 記法・リンク・URL・記号を落として読み上げ用テキストにする。
// fence の除去は plainifyStreaming が済ませている前提。
export function plainify(s: string): string {
  return (
    s
      // インラインコード `x` → x
      .replace(/`([^`]*)`/g, "$1")
      // 画像 ![alt](url) → 落とす
      .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
      // リンク [text](url) → text
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      // 裸の URL は読まない
      .replace(/https?:\/\/\S+/g, "")
      // 行頭の見出し/引用/リストマーカー
      .replace(/^\s{0,3}(#{1,6}\s+|>\s+|[-*+]\s+|\d+\.\s+)/gm, "")
      // 強調・打ち消し
      .replace(/(\*\*|__|~~|\*|_)(.*?)\1/g, "$2")
      // 水平線
      .replace(/^\s*([-*_]\s*){3,}$/gm, "")
      // 余分な空白の圧縮
      .replace(/[ \t]+/g, " ")
      .trim()
  );
}
