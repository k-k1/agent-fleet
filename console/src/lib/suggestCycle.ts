// 返信サジェストの Tab 補完（シェルの補完サイクル）。
//
// チップ行は入力中の draft で前方一致フィルタされる（lib/quickReplies の rankQuickReplies）。
// 「ok」まで打つと ok で始まる候補だけが残るので、そこから先はキーボードだけで選べたほうが速い
// ＝ Tab で候補を順に入力欄へ入れ、もう一度 Tab で次の候補へ、というシェルの補完と同じ操作。
//
// リングは [自分が打った文字, 候補1, 候補2, …] の順で、一周すると打った文字へ戻る（＝補完を
// 取り消す手段が Tab だけで完結する）。Shift+Tab は逆回り。候補は Tab を押した時点で凍結する:
// 入力欄の中身は補完で候補そのものに変わり、そのまま再計算すると絞り込みが「候補1に前方一致
// するもの」へ狭まってリングが崩れるため。凍結した base はチップ行の絞り込みにも使う
// （＝サイクル中もチップ列は動かず、いま入っている候補だけが強調される）。
//
// 入力欄が空のときの Tab は従来どおりチップへのフォーカス移動（MirrorView / ChatView 側）。
// ここは「何か打ってから絞り込まれた候補をたどる」ためだけの経路。

// 空白畳み・全角半角の畳み込み・小文字化は rankQuickReplies の前方一致フィルタと同じものを使う
// （lib/quickReplies の quickReplyKey）。「チップ行に見えている候補」と「Tab でたどれる候補」を
// 必ず一致させるため、突合の基準はここで独自に持たない。
import { quickReplyKey as norm } from "./quickReplies.ts";

export type SuggestCycle = {
  /** ユーザーが自分で打った文字（リングの原点＝チップ行の絞り込みキー）。 */
  base: string;
  /** base に前方一致する候補（Tab を押した時点で凍結）。 */
  items: string[];
  /** リング位置。0 = base、1..items.length = items[idx - 1]。 */
  idx: number;
  /** いま入力欄へ入れた文字。draft と一致しなくなったら手入力で崩れた合図（＝サイクル終了）。 */
  text: string;
};

/** base に前方一致する候補（表示綴りのまま・重複は畳む・base そのものは除く）。 */
export function suggestMatches(base: string, chips: string[]): string[] {
  const b = norm(base);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const c of chips) {
    const k = norm(c);
    if (!k || k === b) continue; // 打った文字そのものへ「補完」しても意味がない
    if (b && !k.startsWith(b)) continue;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(c);
  }
  return out;
}

/**
 * Tab / Shift+Tab を1回進める。サイクルを続けられないときは null（＝呼び出し側は Tab を
 * 素通しし、フォーカス移動など従来の挙動に任せる）。
 */
export function stepSuggestCycle(
  cur: SuggestCycle | null,
  draft: string,
  chips: string[],
  backward: boolean,
): SuggestCycle | null {
  // 進行中のサイクル（入力欄が前回入れた候補のままなら継続）。
  if (cur && cur.text === draft) {
    const n = cur.items.length;
    const idx = (cur.idx + (backward ? -1 : 1) + n + 1) % (n + 1);
    return { ...cur, idx, text: idx === 0 ? cur.base : cur.items[idx - 1] };
  }
  // 新しいサイクル。空（空白だけ）や複数行の下書きは対象外 — 前者は空入力の Tab（チップへ
  // フォーカス）の領分、後者はもう「短い返信の打ち始め」ではない。
  if (!norm(draft) || /[\r\n]/.test(draft)) return null;
  const items = suggestMatches(draft, chips);
  if (!items.length) return null;
  const idx = backward ? items.length : 1; // Shift+Tab は末尾から
  return { base: draft, items, idx, text: items[idx - 1] };
}

/** いま生きているサイクル（入力欄が手で編集されていたら null）。 */
export function activeSuggestCycle(cur: SuggestCycle | null, draft: string): SuggestCycle | null {
  return cur && cur.text === draft ? cur : null;
}

/** チップ行の絞り込みに使う文字列（サイクル中は凍結した base）。 */
export function suggestFilterDraft(cur: SuggestCycle | null, draft: string): string {
  return activeSuggestCycle(cur, draft)?.base ?? draft;
}

/** いま入力欄に入っている候補（= 強調するチップ）。base に戻っている / 非サイクル時は null。 */
export function cycledSuggestion(cur: SuggestCycle | null, draft: string): string | null {
  const a = activeSuggestCycle(cur, draft);
  return a && a.idx > 0 ? a.items[a.idx - 1] : null;
}
