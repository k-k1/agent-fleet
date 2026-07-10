// features/viewer/readerText — 朗読ビュー（docs/24）用のテキスト分割（純ロジック、依存は
// ttsText の plainify のみ）。ファイル本文を「段落 → 文」に分割する。Markdown はコードフェンス
// を読み飛ばし、各行の記法（見出し/リスト/引用/リンク/URL 等）を plainify で落とす。txt は
// そのまま段落・文に割る。カラオケ・ハイライトの単位＝文。node の vitest で直接テストできる。
import { plainify } from "../chat/ttsText.ts";

// splitSentences は段落を文に割る（句点類を末尾に含めて 1 文）。区切りが無ければ全体で 1 文。
export function splitSentences(para: string): string[] {
  const out: string[] = [];
  let buf = "";
  for (const ch of para) {
    buf += ch;
    if ("。．！？!?".includes(ch)) {
      const t = buf.trim();
      if (t) out.push(t);
      buf = "";
    }
  }
  const tail = buf.trim();
  if (tail) out.push(tail);
  return out;
}

// toReadingParagraphs は本文を段落（＝文の配列）の配列へ。空段落・コード・記法は除いて
// 「読める散文」だけにする。isMarkdown=false（txt 等）は行の記法除去のみで素直に段落化。
export function toReadingParagraphs(content: string, isMarkdown: boolean): string[][] {
  const paras: string[][] = [];
  let cur: string[] = []; // 現在の段落の生行
  let inFence = false;

  const flush = () => {
    if (cur.length) {
      // 各行を plainify（行頭の見出し/リスト/引用マーカーは行単位で落ちる）→ 空行を除き連結。
      const plain = cur
        .map((l) => plainify(l))
        .filter((s) => s.trim())
        .join(" ")
        .trim();
      cur = [];
      if (plain) {
        const sents = splitSentences(plain);
        if (sents.length) paras.push(sents);
      }
    }
  };

  // Markdown のブロック開始行（見出し / リスト項目 / 引用）。空行が無くても各行が別単位に
  // なる方が読み上げの区切りとして自然なので、これらは 1 行で 1 段落に切り出す。
  const isBlockStart = (line: string) => /^\s{0,3}(#{1,6}\s|[-*+]\s|\d+\.\s|>\s)/.test(line);

  for (const line of content.split(/\r?\n/)) {
    if (isMarkdown && line.trimStart().startsWith("```")) {
      inFence = !inFence; // フェンス開閉で段落を確定、内側は読み飛ばす
      flush();
      continue;
    }
    if (inFence) continue;
    if (line.trim() === "") {
      flush(); // 空行＝段落境界
      continue;
    }
    if (isMarkdown && isBlockStart(line)) {
      flush(); // 直前の段落を確定
      cur.push(line);
      flush(); // この行だけで 1 単位（見出し/リスト項目/引用）
      continue;
    }
    cur.push(line);
  }
  flush();
  return paras;
}
