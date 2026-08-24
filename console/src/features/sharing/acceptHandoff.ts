// 引き継ぎの受諾（docs/77 / ADR 0057 決定 3）は「起動できた」の**事後申告**である。
//
// 起動を CP に代行させない —— させると CP が他人の Workspace を操作することになり、この機能が
// 避けた構造そのものになる。受け手は自分の権限で自分の Workspace にセッションを作り、その後で
// ここが「受け取った」を送る。だから起動導線（StartHost）の成功パス以外から呼んではいけない。
//
// 起動導線から切り出してあるのは循環 import を避けるためで、置き場所は sharing 側が正しい
// （offer の寿命は共有 ACL に従属する）。
import { apiJSON } from "../../core/api/client.ts";
import { useHandoffStore } from "./handoffStore.ts";

export async function acceptHandoffOffer(offerId: string, sessionName: string): Promise<void> {
  await apiJSON(`api/session-handoff-offers/${encodeURIComponent(offerId)}/accept`, "POST", { sessionName }).catch(
    () => undefined, // best-effort: 起動は済んでいる。申告の失敗で起動を壊さない
  );
  void useHandoffStore.getState().refresh();
}
