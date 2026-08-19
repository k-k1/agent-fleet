// transcript/marks — 会話の「ここ」に引いた線（docs/69 / ADR 0050）。
//
// ミラー（所有者）と共有ビュー（受け手）の両方が使う。ここは「どこを指すか」の決め方だけ
// を持つ純粋なモジュールで（React も I/O も無し・model.ts から呼べる）、取得と書き込みの
// 配線は useMarks.ts。
//
// アンカーは W3C Web Annotation の TextQuoteSelector 相当（引用文字列 + 出現番号）で、
// 実際に DOM へ被せるのは features/viewer/quoteMarks.ts（プランコメントと同じ道具）。
//
// ⚠️ 数える範囲を間違えると「共有先だけ印が 1 つ隣」になる。詳細は docs/69 §69.3 だが、
// 要点は 2 つ:
//   - 転写行の序数（idx）は compaction で動くのでアンカーに使えない。
//   - ブロック（Group）相対の part 番号も使えない。groupTurns() は連続ターンの parts を
//     連結するので、番号は「そのブロックに何行畳み込まれたか」に依存し、ミラーと共有
//     ビューが別々の tail 窓を持つ以上、窓の切れ目で両側がずれる。
// そこで root は「元ターンの安定キー # 元ターン内の part 番号」で、nth はその root
// ひとつの描画後テキストの中だけで数える。root ＝ 共有 DTO を素通りする part 1 つなので、
// 両側が同じ文字列を数えることが保証される。

import type { Turn } from "./types.ts";

/** 選べる色。意味づけは利用者のもの（作成者は別の軸＝下線で示す — ADR 0050 決定 5）。 */
export const MARK_COLORS = ["yellow", "green", "blue", "pink"] as const;
export type MarkColor = (typeof MARK_COLORS)[number];

/** 印を置ける part の kind。Agent 側の markProseKinds と同じ表（docs/69 §69.4）。 */
export const MARKABLE_KINDS = new Set(["", "text", "plan", "answer", "output", "prompt"]);

export interface TranscriptMark {
  id: string;
  /** 元ターンの安定キー（anchorId、無ければ本文ハッシュ）。 */
  turn: string;
  /** 元ターン内の part 番号。ターン本文そのものは -1。 */
  part: number;
  kind: string;
  quote: string;
  nth: number;
  color: string;
  /** "" = セッションの所有者。共有先が付けたものは CP が刻んだ login id。 */
  author?: string;
  created_at?: number;
}

export type NewMark = Omit<TranscriptMark, "id" | "created_at">;

/** ターン本文（parts ではなくターンそのもののテキスト）を指す part 番号。 */
export const BODY_PART = -1;

/** DOM の data-mark-root と、印の引き当てに使う 1 本のキー。 */
export function markRootKey(turn: string, part: number): string {
  return turn + "#" + (part === BODY_PART ? "b" : part);
}

/** markRootKey の逆。DOM の data-mark-root から保存する形へ戻す。 */
export function parseRootKey(key: string): { turn: string; part: number } | null {
  const at = key.lastIndexOf("#");
  if (at <= 0) return null;
  const tail = key.slice(at + 1);
  if (tail === "b") return { turn: key.slice(0, at), part: BODY_PART };
  const part = Number(tail);
  if (!Number.isInteger(part) || part < 0) return null;
  return { turn: key.slice(0, at), part };
}

// hash32 — anchorId を持たない kind／版のためのフォールバック。暗号強度は要らない
// （必要なのは「両側が同じ文字列から同じ値を出す」ことだけ）ので FNV-1a で足りる。
function hash32(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16);
}

/**
 * ターンの安定キー。anchorId が正で、無い kind では本文のハッシュへ落とす。
 *
 * pending（送信直後のローカルエコー）と queued には印を置かせない: 本物のターンが届いた
 * 瞬間にキーが変わり、置いた印が宙に浮く。空文字を返すと呼び出し側が root を作らない。
 */
export function turnKey(t: Turn): string {
  if (t.pending || t.queued) return "";
  if (t.anchorId) return t.anchorId;
  const text = t.text || "";
  return text ? "h:" + hash32(text) : "";
}
