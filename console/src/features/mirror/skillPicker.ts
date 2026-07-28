// スキルピッカーの純ロジック（docs/50 / ADR0034）。DOM もストアも触らない —
// MirrorView が「/」トリガ判定・絞り込み・差し込みをここへ委譲する。
// claude のスラッシュ起動は「入力の先頭」でだけ成立するので、トリガも先頭の
// 1 トークン内にキャレットがある間だけ生きる（空白を打って引数に入ったら閉じる）。

import type { SessionSkill } from "../../core/api/client.ts";

export interface SlashToken {
  token: string; // 先頭 "/" を除いた入力中の断片（"" = "/" 直後）
  start: number; // 常に 0（先頭スラッシュ）— 差し込み置換の左端
  end: number; // 置換の右端（最初の空白 or 文末）
}

// slashTokenAt: draft とキャレット位置から「補完対象のスラッシュトークン」を返す。
// 対象外（先頭が "/" でない・キャレットがトークン外・改行を含む複数行の途中）は null。
export function slashTokenAt(text: string, caret: number): SlashToken | null {
  if (!text.startsWith("/")) return null;
  const ws = text.search(/[\s]/); // 最初の空白（改行含む）でトークン終了
  const end = ws < 0 ? text.length : ws;
  if (caret < 1 || caret > end) return null;
  return { token: text.slice(1, end), start: 0, end };
}

// filterSkills: 前方一致 > 名前部分一致 > 説明部分一致の順で並べる。大文字小文字は
// 無視。空クエリは全件（API の並び＝name 昇順のまま）。
export function filterSkills(skills: SessionSkill[], query: string): SessionSkill[] {
  const q = query.trim().toLowerCase();
  if (!q) return skills;
  const rank = (s: SessionSkill): number => {
    const nm = s.name.toLowerCase();
    if (nm.startsWith(q)) return 0;
    if (nm.includes(q)) return 1;
    if ((s.description || "").toLowerCase().includes(q)) return 2;
    return -1;
  };
  return skills
    .map((s) => ({ s, r: rank(s) }))
    .filter((x) => x.r >= 0)
    .sort((a, b) => a.r - b.r)
    .map((x) => x.s);
}

// applySkillToDraft: 選択したスキルを draft へ差し込み、新しい draft とキャレット
// 位置を返す。スラッシュ起動は先頭でだけ意味を持つので、常に「/name ＋既存の本文
// （引数として残す）」に組み立てる。入力中の "/tok" は置換して消える。
export function applySkillToDraft(
  draft: string,
  caret: number,
  name: string,
): { next: string; caret: number } {
  const inserted = "/" + name + " ";
  const tok = slashTokenAt(draft, caret);
  // トークンが生きていればその右側（既に書いた引数）を、そうでなければ（ボタン
  // 起点）スラッシュで始まらない下書き全体を引数位置へ残す。
  const tail = tok
    ? draft.slice(tok.end).trimStart()
    : draft.startsWith("/")
      ? ""
      : draft.trimStart();
  return { next: inserted + tail, caret: inserted.length };
}
