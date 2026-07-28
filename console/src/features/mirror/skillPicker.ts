// スキルピッカーの純ロジック（docs/50 / ADR0034）。DOM もストアも触らない —
// MirrorView がトリガ判定・絞り込み・差し込みをここへ委譲する。
// トリガ文字は kind 依存（claude/opencode/cursor は "/"、codex は "$" メンション —
// registry の skillTrigger）。起動は「入力の先頭」でだけ成立するとみなし、トリガも
// 先頭の 1 トークン内にキャレットがある間だけ生きる（空白を打って引数に入ったら閉じる）。

import type { SessionSkill } from "../../core/api/client.ts";

export interface SlashToken {
  token: string; // トリガ文字を除いた入力中の断片（"" = トリガ直後）
  start: number; // 常に 0（先頭トリガ）— 差し込み置換の左端
  end: number; // 置換の右端（最初の空白 or 文末）
}

// slashTokenAt: draft とキャレット位置から「補完対象のトークン」を返す。
// 対象外（先頭がトリガでない・キャレットがトークン外）は null。
export function slashTokenAt(text: string, caret: number, trigger = "/"): SlashToken | null {
  if (!trigger || !text.startsWith(trigger)) return null;
  const ws = text.search(/[\s]/); // 最初の空白（改行含む）でトークン終了
  const end = ws < 0 ? text.length : ws;
  if (caret < trigger.length || caret > end) return null;
  return { token: text.slice(trigger.length, end), start: 0, end };
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

// applySkillToDraft: 選択したスキルの起動文字列（invoke — 末尾空白込み。"/name " や
// "$name "）を draft へ差し込み、新しい draft とキャレット位置を返す。起動は先頭で
// だけ意味を持つので、常に「invoke ＋既存の本文（引数として残す）」に組み立てる。
// 入力中のトークンは置換して消える。
export function applySkillToDraft(
  draft: string,
  caret: number,
  invoke: string,
  trigger = "/",
): { next: string; caret: number } {
  const tok = slashTokenAt(draft, caret, trigger);
  // トークンが生きていればその右側（既に書いた引数）を、そうでなければ（ボタン
  // 起点）トリガで始まらない下書き全体を引数位置へ残す。
  const tail = tok
    ? draft.slice(tok.end).trimStart()
    : trigger && draft.startsWith(trigger)
      ? ""
      : draft.trimStart();
  return { next: invoke + tail, caret: invoke.length };
}
