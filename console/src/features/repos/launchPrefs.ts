// 作業を始める（LaunchModal）の折りたたみセクションの開閉を覚える。リポジトリ単位ではなく
// 端末単位: 「毎回ブランチ名を打つ人」と「既定のまま起動する人」の違いは人に付くもので、
// リポジトリを移っても変わらないため。設定（サーバ同期）に置くほどの重みはないので
// localStorage に留める — 読めなければ既定（畳んだ状態）で困らない。
export type LaunchSectionKey = "place" | "adv";

const KEY = (k: LaunchSectionKey) => "af.launch-open." + k;

export function readLaunchOpen(k: LaunchSectionKey): boolean {
  try {
    return localStorage.getItem(KEY(k)) === "1";
  } catch {
    return false; // private mode — 畳んだ既定で開く
  }
}

export function writeLaunchOpen(k: LaunchSectionKey, open: boolean): void {
  try {
    if (open) localStorage.setItem(KEY(k), "1");
    else localStorage.removeItem(KEY(k));
  } catch {
    /* private mode / quota — 次回また畳んだ状態で開くだけ */
  }
}
