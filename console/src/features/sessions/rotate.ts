// 稼働中セッションのローテート — スマホの ← スワイプ（app/App.tsx）が使う選択規則。
//
// ここは PURE（ストアも DOM も触らない）。node vitest プロジェクトで単体テストできる
// ようにするため、実際にペインへ開く副作用側は sessions/open.ts に置く（workingSets.ts
// と workingSetsStore.ts の分け方と同じ理由）。
//
// 対象と順序（docs/52 の作業グループを尊重する）:
// - 対象＝alive なセッションだけ。停止中は「切り替え先」ではなく再開の判断が要るので入れない。
// - 作業グループが選ばれていれば、その絞り込みに従う（左ペインで見えている集合と一致させる）。
// - 順序は GET /api/sessions のまま（CreatedAt の降順＝新しい順、session_handlers.go）。
//   一覧が入れ替わらない限りローテート順も安定する。
import { sessionInSet } from "../../lib/workingSets.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

/** ローテートの対象集合。set=null は「すべて」（絞り込みなし）。 */
export function rotatableSessions(sessions: Session[], set: WorkingSet | null): Session[] {
  return sessions.filter((s) => !!s.alive && (!set || sessionInSet(set, s)));
}

export interface RotateTarget {
  session: Session;
  /** 0 始まりの行き先位置（トーストの「2/3」表示用）。 */
  index: number;
  total: number;
}

/** current から delta 個ぶん進めた行き先（端は巻き戻る）。
 *
 * - current が対象外（停止済み・別グループ・そもそもセッション以外のペイン）のときは
 *   前進なら先頭から、後退なら末尾から始める。
 * - 行き先が現在地と同じになるとき（対象が 1 件だけ）は null＝何もしない。 */
export function rotateTarget(
  list: Session[],
  current: string | null | undefined,
  delta: number,
): RotateTarget | null {
  if (list.length === 0 || delta === 0) return null;
  const at = list.findIndex((s) => s.name === current);
  if (list.length === 1) return at === 0 ? null : { session: list[0], index: 0, total: 1 };
  // 未ヒット時の基準: 前進は -1（→ 先頭）、後退は 0（→ 末尾）。
  const base = at < 0 ? (delta > 0 ? -1 : 0) : at;
  // 二重 mod: |delta| > list.length の負方向でも負インデックスにならない（layout/nav と同じ）。
  const i = (((base + delta) % list.length) + list.length) % list.length;
  return { session: list[i], index: i, total: list.length };
}
