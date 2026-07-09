// features/chat/ttsText — 読み上げ用テキスト整形（純ロジック、依存なし）。
// Markdown 記法・コードブロック・リンク・URL を落として TTS に渡せる素のテキストにする。
// tts.ts から使う。ブラウザ API に触れないので node の vitest で直接テストできる。

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
