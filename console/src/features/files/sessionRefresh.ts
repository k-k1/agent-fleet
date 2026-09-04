// sessionRefresh — 「セッションのターンが終わったら、そのセッションの作業コピーの
// ぶんだけ FILES を読み直す」配線。
//
// なぜ要るか: ツリーの更新は今まで **手動の更新ボタンと WS の起動/停止だけ**だった
// （store.ts の tick）。エージェントが作った/消したファイルは次に人が更新ボタンを
// 押すまで出てこず、そのボタンに気づかない人が「反映されない」と詰まっていた。
//
// なぜ「入力待ちに入った瞬間」か: ターン中はファイルが増減し続けるので、そこを
// 追いかけるには監視か定期取得が要る（どちらも往復が要る・docs の検討を見よ）。
// 一方ターンの終わりは **すでに手元にある情報**で分かる — セッション一覧は push でも
// poll でも applyList を通り、state を運んでくる。だから追加の通信はゼロで、
// 「エージェントが手を止めた ＝ 結果が出そろった」という人の期待とも一致する。
//
// 前例: 「稼働 → 非稼働」の縁の取り方は useSessionNotifications / waiting.ts と同じ。
// 初観測では**発火しない**のも同じ理由で、リロード直後に全セッションぶんが一斉に
// 走るのを防ぐ（waiting.ts の note を見よ）。
//
// ターンの終わりだけでは足りない場面が 2 つあり、それぞれ別の引き金で埋める:
//   - **長いターンの最中に見に行く人** → 走っているセッションの作業コピーだけを
//     WORKING_TICK_MS 間隔で読み直す（下の tickWorking。走っているものが無ければ
//     タイマーごと止まる）。
//   - **離席していた人** → タブ復帰／ウィンドウ focus での再検証。これはツリー側
//     （ProjectFiles / FilesChanges）に置く。state を持たない shell セッションが
//     触ったファイルを拾えるのも、事実上ここだけ。
import { useSessionsStore } from "../sessions/store.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { COALESCE_MS, MIN_GAP_MS, WORKING_TICK_MS } from "./refreshPolicy.ts";
import { useFilesStore } from "./store.ts";
import type { Session } from "../../types/session.ts";

/** 走っている状態。これ以外（idle / question / plan / permission / limited /
 *  blocked / auth …）は「ターンが終わって人の番になった」側で、どれも読み直す価値が
 *  ある — 質問で止まったセッションも、そこまでに書いたファイルは残っている。 */
const BUSY_STATES = new Set(["working", "compacting"]);

/** 稼働中（＝まだ書いているかもしれない）か。backgroundBusy は「hook 上は idle だが
 *  バックグラウンドのタスクがまだ走っている」— そこで読んでも取りこぼすので busy 扱い。 */
export const isBusySession = (s: Pick<Session, "alive" | "state" | "backgroundBusy">): boolean =>
  !!s.alive && (BUSY_STATES.has(s.state || "") || !!s.backgroundBusy);

/** そのセッションが書き換えうる範囲（home 相対）。作業コピーを持たないセッション
 *  （home で動く shell など）は "" — 範囲が決められないので何もしない。
 *  subdir があっても作業コピー単位に丸める: エージェントは cwd の外も普通に触る。 */
export const sessionPrefix = (s: Pick<Session, "repo">): string => (s.repo ? "repos/" + s.repo : "");

// 間合いの数字とその根拠は refreshPolicy.ts（撃つ側と読む側で共有する）。

/**
 * 一覧が届くたびに呼び、「読み直すべき作業コピー」を返す純関数を作る。
 * 台帳（name → 直前は busy だったか）は呼び出しごとに更新される。
 *
 * ★ 初観測は記録だけで発火しない。★ 一覧から消えたセッション（削除・アーカイブ）も
 * 発火しない — 消えた行は repo を持って行ってしまうので、そもそも範囲が引けない。
 */
export function createTurnEndDetector(): (list: Session[]) => string[] {
  let before = new Map<string, boolean>();
  return (list: Session[]): string[] => {
    const out = new Set<string>();
    const next = new Map<string, boolean>();
    for (const s of list) {
      const busy = isBusySession(s);
      next.set(s.name, busy);
      // true → false だけが縁。undefined（初観測）は素通り。alive が落ちた行も
      // busy=false になるので、停止/終了で終わったターンもここで拾える。
      if (before.get(s.name) === true && !busy) {
        const prefix = sessionPrefix(s);
        if (prefix) out.add(prefix);
      }
    }
    before = next;
    return [...out];
  };
}

/**
 * ストアへ配線する（App が 1 回だけ呼ぶ）。返り値は解除（StrictMode 安全）。
 *
 * 発火は作業コピーごとに合流させ、最短間隔を空ける。FILES セクションが閉じていても
 * 止めない — 合図はストアのカウンタを 1 つ進めるだけで、木が生えていなければ誰も
 * 読みに行かない（ProjectFiles は閉じるとアンマウントされる）。**この形のおかげで、
 * 走行中の低頻度更新も「誰も見ていなければ 0 リクエスト」になる。**
 */
export function wireFilesSessionRefresh(): () => void {
  const detect = createTurnEndDetector();
  const lastAt = new Map<string, number>();
  const timers = new Map<string, number>();
  let busyPrefixes: string[] = [];
  let ticker = 0;

  const fire = (prefix: string) => {
    timers.delete(prefix);
    lastAt.set(prefix, Date.now());
    useFilesStore.getState().refreshUnder(prefix);
  };
  const schedule = (prefix: string) => {
    if (timers.has(prefix)) return; // 予約済み — その 1 回が今回のぶんも兼ねる
    const since = Date.now() - (lastAt.get(prefix) || 0);
    const delay = Math.max(COALESCE_MS, MIN_GAP_MS - since);
    timers.set(prefix, window.setTimeout(() => fire(prefix), delay));
  };

  // 走行中の低頻度更新。見えていない（タブが裏・WS が停止）ときは撃たない —
  // 「見ている人のための更新」であって、監視ではない。
  const tickWorking = () => {
    if (document.hidden || !wsRunning(useWorkspaceStore.getState().state)) return;
    for (const prefix of busyPrefixes) schedule(prefix);
  };

  const unsub = useSessionsStore.subscribe((s) => {
    for (const prefix of detect(s.sessions)) schedule(prefix);
    busyPrefixes = [
      ...new Set(s.sessions.filter(isBusySession).map(sessionPrefix).filter(Boolean)),
    ];
    // 走っているものが 1 つも無ければタイマーを畳む（常駐させない）。
    if (busyPrefixes.length && !ticker) ticker = window.setInterval(tickWorking, WORKING_TICK_MS);
    else if (!busyPrefixes.length && ticker) {
      window.clearInterval(ticker);
      ticker = 0;
    }
  });
  return () => {
    unsub();
    if (ticker) window.clearInterval(ticker);
    ticker = 0;
    for (const t of timers.values()) window.clearTimeout(t);
    timers.clear();
  };
}
