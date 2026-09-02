// 「そのセッションが最後に人待ちに入った時刻」の台帳。コマンドパレットのセッション一覧を
// **最後に入力待ちになった順**で並べるための材料で、順序以外には使わない。
//
// なぜ台帳が要るか: GET /api/sessions は state（question / plan / permission）を返すが、
// **いつそうなったか**は返さない。並び順の根拠はそこにしか無いので、Console 側で 2 つの
// 材料を突き合わせる（order.ts の waitingAt が合成する）:
//
//   1. 通知台帳（サーバ側・createdAt つき）… リロードや別端末を跨いでも残る土台。
//   2. この画面で観測した遷移（このファイル）… 通知では埋まらない穴を埋める:
//      通知を出さない Agent、通知の保持窓から溢れた古い質問、そして「質問→回答→また質問」
//      の 2 度目（1 度目の通知しか残っていないと、順序が古いままになる）。
//
// 観測は localStorage にだけ置く。サーバへは送らない — 端末ローカルの並び順の都合であって
// 利用者の設定ではないし、失っても「順序が通知台帳だけに戻る」だけで壊れない。
import type { Session } from "../../types/session.ts";

const KEY = "af.sessionWaitingAt";

/** 人の答えを待っている state。時計待ち（limited）や進行中は入らない — 待っている相手が
 *  人でないものをここに混ぜると、パレットの最上段が「今すぐ答えるべき行」でなくなる。 */
export const WAITING_STATES = new Set(["question", "plan", "permission"]);

/** 稼働中でかつ人待ちか。停止中の carried（畳まれたときに抱えていた質問）は**含めない** —
 *  それは「今すぐ答えれば進む」ではないので、段としては停止中に置く（order.ts）。 */
export const isWaiting = (s: { alive?: boolean; state?: string }): boolean =>
  !!s.alive && WAITING_STATES.has(s.state || "");

// name -> epoch ms。null = まだ localStorage から読んでいない（初回アクセスで読む）。
let observed: Record<string, number> | null = null;
// 直前の観測で人待ちだったか。**未観測は欠落**で、これが遷移検出の要（下の note を見よ）。
let before: Record<string, boolean> = {};

function read(): Record<string, number> {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === "number" && isFinite(v) && v > 0) out[k] = v;
    }
    return out;
  } catch {
    return {}; // 壊れた値／localStorage が無い環境（node のテスト）— 順序が落ちるだけ
  }
}

function write(map: Record<string, number>): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(map));
  } catch {
    /* private mode / quota — 並び順の補助であって、失っても機能は動く */
  }
}

/** この端末で観測した「最後に人待ちに入った時刻」（epoch ms）。0 = 観測していない。 */
export function observedWaitingAt(name: string): number {
  if (!observed) observed = read();
  return observed[name] || 0;
}

/** セッション一覧が届くたびに呼ぶ（poll でも push でも applyList を通る）。
 *
 * ★ 初観測が人待ちでも記録しない。**いつ**入ったのか分からないのに now を焼くと、
 *   リロード直後に全員が同じ時刻で並び、通知台帳が持っている本物の順序をこちらの
 *   偽の時刻が上書きしてしまう（max を採るので、新しい方＝偽物が勝つ）。 */
export function noteSessions(list: Session[], now: number = Date.now()): void {
  // 空一覧は「全部消えた」ではない: 起動直後の初期値でもあり、ストアは取得失敗時に
  // 最後の一覧を保持する（store.ts の refresh を見よ）。ここで刈ると順序だけが消える。
  if (!list.length) return;
  if (!observed) observed = read();
  let changed = false;
  const next: Record<string, boolean> = {};
  const live = new Set<string>();
  for (const s of list) {
    live.add(s.name);
    const waiting = isWaiting(s);
    next[s.name] = waiting;
    if (before[s.name] === false && waiting) {
      observed[s.name] = now;
      changed = true;
    }
  }
  before = next;
  // 一覧から消えたセッション（削除・アーカイブ）を落とす。件数はセッション数で頭打ちになる。
  for (const name of Object.keys(observed)) {
    if (!live.has(name)) {
      delete observed[name];
      changed = true;
    }
  }
  if (changed) write(observed);
}

/** テスト用の継ぎ目: モジュールの観測状態を捨てる。 */
export function resetWaitingLedgerForTest(): void {
  observed = null;
  before = {};
}
