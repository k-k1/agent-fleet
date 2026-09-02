// コマンドパレットのセッション一覧の並び — 純関数だけ（時計も store も触らない）。
//
// 並びは「今この人の答えを待っているもの」から始まり、「今は動いていないもの」で終わる 3 段:
//
//   段 0 入力待ち … 稼働中で question / plan / permission。**最後に入力待ちになった順**
//                   （新しいものが上）。パレットを開く動機のほとんどはこれ。
//   段 1 稼働中   … それ以外の生きているセッション。ここも「最後に入力待ちになった順」を
//                   使う — 直前に答えたセッションが先頭に来るので、「答えた → 続きを見る」
//                   の往復が最短になる。待ちに入ったことが無いものは作成の新しい順。
//   段 2 停止中   … 畳まれた/落ちたもの。「下部でよい」ので中身の緊急度で段は上げないが、
//                   段の中では carried（畳まれたときに未回答の対話を抱えていた）を先に置く
//                   — 停止中の山に埋めると、それこそが docs/log/75 で拾えるようにした行。
//
// waitingAt は「そのセッションが最後に人待ちに入った epoch ms（0 = 不明）」を返す関数で、
// 呼び手が通知台帳と観測台帳から合成する（waitingAtFromNotifications + observedWaitingAt）。
// ここに時計を持ち込まないのは、順序をテストで固定できるようにするため。
import { compareText } from "../../lib/intl.ts";
import { isWaiting } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

/** 段。小さいほど上。 */
export const TIER_WAITING = 0;
export const TIER_ALIVE = 1;
export const TIER_STOPPED = 2;

export function sessionTier(s: Session): number {
  if (!s.alive) return TIER_STOPPED;
  return isWaiting(s) ? TIER_WAITING : TIER_ALIVE;
}

/** 通知が「人待ちになった」と言っている種別。通知台帳（GET /api/notifications）の kind。 */
const WAITING_NOTIFICATIONS = new Set(["question", "plan-approval", "permission-request"]);

/** 通知台帳 → セッションごとの「最後に人待ちになった時刻」（epoch ms）。
 *
 * 型を構造的に受けるのは循環 import を避けるため（通知ストアはセッションストアを参照して
 * いる）。既読（seen）でも数える — 読んだかどうかと、いつ待ちに入ったかは別の話。 */
export function waitingAtFromNotifications(
  items: { kind?: string; target?: { type?: string; id?: string }; createdAt?: string }[],
): Record<string, number> {
  const out: Record<string, number> = {};
  for (const n of items) {
    if (!n.kind || !WAITING_NOTIFICATIONS.has(n.kind)) continue;
    if (n.target?.type !== "session" || !n.target.id) continue;
    const at = new Date(n.createdAt || "").getTime();
    if (!isFinite(at)) continue;
    if (at > (out[n.target.id] || 0)) out[n.target.id] = at;
  }
  return out;
}

/** 段 2 の中の順位: 未回答を抱えて畳まれたものが先。 */
const carriedRank = (s: Session): number => (s.carried ? 0 : 1);

/** 新しい順（欠落は末尾）。ISO 文字列はそのまま辞書順で時系列になる。 */
const byCreatedDesc = (a: Session, b: Session): number => compareText(b.createdAt || "", a.createdAt || "");

/** 上の 3 段でセッションを並べ替える（入力は変更しない）。 */
export function sortSessionsByAttention(sessions: Session[], waitingAt: (name: string) => number): Session[] {
  return [...sessions].sort((a, b) => {
    const tier = sessionTier(a) - sessionTier(b);
    if (tier) return tier;
    if (sessionTier(a) === TIER_STOPPED) {
      const carried = carriedRank(a) - carriedRank(b);
      if (carried) return carried;
    } else {
      const at = waitingAt(b.name) - waitingAt(a.name);
      if (at) return at;
    }
    return byCreatedDesc(a, b) || compareText(a.name, b.name);
  });
}
