// メンバーへの引き継ぎ（docs/77 / ADR 0057）のクライアント状態。
//
// 3 つの役割を混ぜないのがこの機能の要（docs/77 §77.10）:
//   通知 = 流れ物（既読で消えてよい）／バッジ = 未処理の在庫／台帳 = 出した側の履歴。
// ここが持つのは後ろ 2 つで、どちらも CP の DB スナップショットを読むだけ ——
// **所有者 Workspace が停止していても出る**のが要件そのものなので、共有一覧のように
// 所有者へ往復する経路には乗せない。
import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export type HandoffOfferStatus = "pending" | "accepted" | "declined" | "withdrawn" | "expired";

export interface HandoffOffer {
  id: string;
  /** 共有カタログ id（受信側の共有ビューを開く鍵）。 */
  sessionId: string;
  /** 引き継ぎ元のセッション名（所有者側の表示・遷移先）。 */
  sessionName: string;
  recipientUserKey: string;
  ownerUserKey?: string;
  title: string;
  status: HandoffOfferStatus;
  branch?: string;
  repoRemote?: string;
  headSha?: string;
  createdAt?: string;
  expiresAt?: string;
  decidedAt?: string;
  acceptedSessionName?: string;
  /** 受信箱だけが本文を持つ。受け取るかどうかを決めるのに読む必要があるため。 */
  prompt?: string;
  sourceSessionKind?: string;
}

interface HandoffStore {
  /** 自分が出した引き継ぎ（台帳）。決着済みも含む。 */
  owned: HandoffOffer[];
  /** 自分が受け取った未処理の引き継ぎ（受信箱）。 */
  received: HandoffOffer[];
  refresh(): Promise<void>;
}

export const useHandoffStore = create<HandoffStore>((set) => ({
  owned: [],
  received: [],
  async refresh() {
    const [owned, received] = await Promise.all([
      api("api/session-handoff-offers").catch(() => null),
      api("api/session-handoff-offers/received").catch(() => null),
    ]);
    set({
      owned: owned && !owned.error && Array.isArray(owned.offers) ? owned.offers : [],
      received: received && !received.error && Array.isArray(received.offers) ? received.offers : [],
    });
  },
}));

/** 未処理の在庫は「既読」では消さない（docs/77 §77.10）ので、素の件数がそのままバッジ。 */
export function pendingHandoffCount(offers: HandoffOffer[]): number {
  return offers.filter((o) => o.status === "pending").length;
}

/** そのセッションについて今どうなっているか。1 セッションにつき未処理は 1 件（ADR 0057
 *  決定 10）なので、pending があればそれが答え。無ければ直近の決着を返す。 */
export function offerForSession(offers: HandoffOffer[], sessionName: string): HandoffOffer | undefined {
  const mine = offers.filter((o) => o.sessionName === sessionName);
  return mine.find((o) => o.status === "pending") ?? mine[0];
}

export function startHandoffPolling(): () => void {
  const load = () => {
    if (!document.hidden) void useHandoffStore.getState().refresh();
  };
  load();
  const timer = window.setInterval(load, 15000);
  return () => window.clearInterval(timer);
}
