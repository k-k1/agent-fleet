// api/connections を「1 回の失敗で確定させない」ための取り直し方針（純ロジック — 実際の
// fetch と WS 状態の判定は useRepoRail 側が注入する）。
//
// WS 起動直後は Agent がまだ listen しておらず、この呼び出しは 502 で落ちる。そこで
// null に確定させると launchKinds が空のまま固定され（connTick が動くまで戻らない）、
// 起動導線が丸ごと「使用できるエージェントがありません」になる。症状は「モーダルは開くのに
// エージェントが並ばず、起動ボタンだけが押せない」で、原因が見えないぶん誤診されやすい
// （2026-08: 引き継ぎカードから開いた 作業を始める モーダルで「引き継ぎ元セッションが
// 停止中だから押せない」と読まれた実例）。なので成功するまで間隔を空けて取り直す。
import type { ConnectionsStatus } from "../../types/session.ts";

/** 取り直しの間隔（ms）。合計 ~22s ＋ 各試行の所要時間。これで足りない長い起動
 *  （native rootfs の boot-install は分単位）は、WS が running へ移った時点で
 *  useRepoRail が新しいキーで取り直すので、こちらで際限なく粘る必要はない。 */
export const CONNS_RETRY_MS = [1500, 3000, 6000, 12000];

const wait = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export interface ConnsRetryDeps {
  /** 1 回ぶんの取得。失敗（ネットワーク / 非 2xx / error ボディ）は null で返すこと。 */
  once: () => Promise<ConnectionsStatus | null>;
  sleep?: (ms: number) => Promise<void>;
  delays?: number[];
  /** true を返した時点で取り直しをやめる（WS が止まった / この系列が古くなった）。 */
  abort: () => boolean;
}

/** 成功するまで delays の間隔で取り直す。使い切ったら null（＝呼び手は「無い」を表示）。 */
export async function fetchConnsWithRetry({
  once,
  sleep = wait,
  delays = CONNS_RETRY_MS,
  abort,
}: ConnsRetryDeps): Promise<ConnectionsStatus | null> {
  for (let attempt = 0; ; attempt++) {
    const d = await once();
    if (d || attempt >= delays.length || abort()) return d;
    await sleep(delays[attempt]);
    if (abort()) return null;
  }
}
