// プランへのコメント（VSCode のプランレビュー相当）— 溜める側の一次ストア。
//
// 体験の分割:
//   - ためる: プランを開いた doc ペイン（DocView）で本文を選択 → コメントを追加。
//   - 送る:   ミラーのプランカードで一覧を確認してから1操作で送信。
// この分割のおかげで「承認待ちのあいだ」に限定されない — 却下したあとでも、plan モードの
// まま入力待ちに戻ったセッションへ追加のコメントを送れる（送信経路はミラー側が状態で選ぶ）。
//
// 束ねる鍵（planKey）は「セッション名 + プラン本文のハッシュ」。承認待ちのプランには
// tool_use id が無く（ペイロードは本文だけ）、履歴カードの id も Console 側では引き回して
// いないので、本文そのものが唯一の安定した同一性になる。改訂されたプランは別の鍵になり、
// 古いコメントは当時のカードに残る（消えない）ぶん、改訂後の本文に古い指摘が紛れ込まない。
//
// アンカーは「引用文字列 + その出現番号」。プラン Markdown はレンダリング後の DOM しか
// 手元に無いので、オフセットではなく **描画後テキスト上の n 番目の一致** で位置を復元する
// （W3C Web Annotation の TextQuoteSelector 相当）。本文が変われば一致しなくなるだけで、
// 誤った箇所へハイライトが付くことはない。
//
// 保存は localStorage 1本。サーバを持たない代わりに `storage` イベントを購読して、
// 切り離した別タブ（ペインの別タブ表示）で付けたコメントも元タブのミラーへ届く。
// キーは `af` プレフィックスなのでログアウト時の clearLocalState が掃除する。
import { useSyncExternalStore } from "react";
import { t } from "../../lib/i18n/index.ts";

export interface PlanComment {
  id: string;
  /** 選択されたプラン本文の抜粋（描画後テキスト）。 */
  quote: string;
  /** quote が本文中で何番目の出現か（0 始まり）。同じ語が複数あるときの取り違え防止。 */
  nth: number;
  /** 利用者が書いた指摘。 */
  body: string;
  ts: number;
  /** 送信済みになった時刻（消さずに畳んで残す — 何を送ったかが履歴になる）。 */
  sentAt?: number;
}

type Store = Record<string, PlanComment[]>;

const LS_KEY = "af.plan-comments";
/** 保存する束の上限（古い鍵から捨てる）。localStorage を無制限に太らせない。 */
const MAX_KEYS = 30;
/** 1つのプランに付けられるコメント数の上限。 */
export const MAX_COMMENTS = 50;
/** 引用の保存長。長すぎる選択は先頭だけ残す（アンカーとしては十分で、表示も潰れない）。 */
export const MAX_QUOTE = 300;

let store: Store = load();
const listeners = new Set<() => void>();
// useSyncExternalStore は getSnapshot の参照同一性で再描画を決めるので、
// 変更のたびに差し替わるオブジェクトを1つ持つ（配列を都度作らない）。
let snapshot = store;

function load(): Store {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Store = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (!Array.isArray(v)) continue;
      const list = v.filter(isComment);
      if (list.length) out[k] = list;
    }
    return out;
  } catch {
    return {}; // private mode / 壊れた JSON — コメントは補助機能なので黙って空から始める
  }
}

function isComment(c: unknown): c is PlanComment {
  const o = c as PlanComment;
  return !!o && typeof o.id === "string" && typeof o.quote === "string" && typeof o.body === "string";
}

function persist() {
  snapshot = store;
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(store));
  } catch {
    /* quota / private mode: メモリ上の状態は生きているので続行 */
  }
  for (const l of listeners) l();
}

function commit(next: Store) {
  // 上限を超えたら「最後に触ったコメントが古い」束から捨てる。
  const keys = Object.keys(next);
  if (keys.length > MAX_KEYS) {
    const lastTouch = (k: string) => Math.max(0, ...next[k].map((c) => c.ts));
    for (const k of keys.sort((a, b) => lastTouch(a) - lastTouch(b)).slice(0, keys.length - MAX_KEYS)) {
      delete next[k];
    }
  }
  store = next;
  persist();
}

// 別タブ（切り離したペイン）での追加を取り込む。storage イベントは自タブには飛ばない
// 仕様なので、自タブの更新は commit() 側の通知でまかなう（二重に走らない）。
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== null && e.key !== LS_KEY) return; // null = clear()（ログアウト）
    store = load();
    snapshot = store;
    for (const l of listeners) l();
  });
}

/** planKey は「どのセッションの、どの本文のプランか」を束ねる鍵。 */
export function planKey(session: string, plan: string): string {
  return session + ":" + hash(normalizePlan(plan));
}

// 前後の空白と行末空白だけ均す。中身の差（改訂）は別の鍵にしたいので、それ以上は正規化しない。
function normalizePlan(plan: string): string {
  return (plan || "")
    .split("\n")
    .map((l) => l.replace(/\s+$/, ""))
    .join("\n")
    .trim();
}

// FNV-1a（32bit）— 衝突耐性より安定性と短さが要る用途（鍵はセッション名で既に分かれる）。
function hash(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(36);
}

export function getPlanComments(key: string): PlanComment[] {
  return store[key] || EMPTY;
}
const EMPTY: PlanComment[] = [];

/** 未送信のコメントだけ（送信ボタンの活性と本文組み立てに使う）。 */
export const unsentComments = (list: PlanComment[]): PlanComment[] => list.filter((c) => !c.sentAt);

export function addPlanComment(key: string, c: { quote: string; nth: number; body: string }): void {
  const body = c.body.trim();
  if (!body) return;
  const list = store[key] || [];
  if (list.length >= MAX_COMMENTS) return;
  const next: PlanComment = {
    id: Math.random().toString(36).slice(2, 10) + Date.now().toString(36),
    quote: c.quote.slice(0, MAX_QUOTE),
    nth: Math.max(0, c.nth | 0),
    body,
    ts: Date.now(),
  };
  commit({ ...store, [key]: [...list, next] });
}

export function removePlanComment(key: string, id: string): void {
  const list = store[key];
  if (!list) return;
  const next = list.filter((c) => c.id !== id);
  const copy = { ...store };
  if (next.length) copy[key] = next;
  else delete copy[key];
  commit(copy);
}

/** 送信できたぶんだけ「送信済み」にする。届かなかったときは呼ばない（打ち直せるように）。 */
export function markPlanCommentsSent(key: string, ids: string[]): void {
  const list = store[key];
  if (!list) return;
  const at = Date.now();
  const mark = new Set(ids);
  commit({ ...store, [key]: list.map((c) => (mark.has(c.id) ? { ...c, sentAt: at } : c)) });
}

export function usePlanComments(key: string | null): PlanComment[] {
  const snap = useSyncExternalStore(subscribe, () => snapshot, () => snapshot);
  return key ? snap[key] || EMPTY : EMPTY;
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** テスト用: モジュール状態を捨てて localStorage から読み直す。 */
export function resetPlanCommentsForTest(): void {
  store = load();
  snapshot = store;
}

// formatPlanFeedback は溜めたコメントを1本のフィードバック文へ組む。ワイヤ上コメントは
// 構造を持てない（CLI の承認ダイアログが受け取れるのは feedback 文字列1本だけ。VSCode
// 拡張も同じく引用＋本文を1本に畳んで渡している）ので、ここが唯一の表現になる。
// 引用は blockquote、指摘はその直下。エージェントが読むと同時にミラーの発話としても
// 表示される文なので、Console の表示言語に合わせる（docs/ADR0033 の「誰が読む文字列か」）。
export function formatPlanFeedback(comments: PlanComment[], note?: string): string {
  const items = comments.filter((c) => c.body.trim());
  const extra = (note || "").trim();
  if (!items.length) return extra;
  const body = items
    .map((c, i) => {
      const quote = c.quote
        .trim()
        .split("\n")
        .map((l) => "> " + l)
        .join("\n");
      return `${i + 1}.\n${quote}\n\n${c.body.trim()}`;
    })
    .join("\n\n");
  const head = t("mirror.plan_feedback_head", { count: items.length });
  return extra ? `${head}\n\n${body}\n\n${extra}` : `${head}\n\n${body}`;
}

// deliverPlanComments は「溜めたコメントをどう届け、いつ送信済みにするか」の判断だけを
// 持つ。送信そのもの（respond = /plan-respond の reject、say = 普通の発話）は呼び出し側
// から注入するので、ここは MirrorView を描画せずに検証できる — その MirrorView が
// 巨大すぎてレンダリングテストを持てないことが、この判断のバグ（下記）を素通しにした。
//
// 経路は「押した瞬間の状態」で決まる:
//   pending  → respond。承認ダイアログが開いたまま自由文を送ると本文がモーダルに飲まれ
//     Enter が承認になるため、Escape で閉じてから投入する経路でしか安全に届かない。
//   それ以外（却下後・plan モードで入力待ち／実行中） → say（普通の発話）。
//   pending のつもりが既に判断済みだった（no_plan）→ say へ落とす。
//
// 送信済みの印は「実際に届いた」ときだけ付ける。届かなかったコメントを畳むと unsent が
// 0 になって送信ボタンごと消え、利用者は打ち直せない（2026-08-10 の実障害: say の失敗を
// 見ずに畳んでいたため、エラートーストと「送信済み」が同時に出ていた）。
export interface PlanRespondLike {
  ok: boolean;
  code?: string;
  delivered?: boolean;
  message?: string;
}

// 失敗の reason は「誰が利用者に伝えるか」も決める:
//   say         — 発話経路が拒否された。say（＝sendPrompt）が理由を通知済みなので、
//                 呼び出し側が重ねて出すと汎用文言が具体的な理由に被さる。
//   respond     — /plan-respond が断った。message をそのまま見せる。
//   undelivered — 却下は通ったが本文が入らなかった（コンポーザ復帰を確認できず）。
export type PlanDeliveryResult =
  /** 届いた＝コメントは畳み済み。via は経路、feedback は実際に送った本文（エコー用）。 */
  | { ok: true; via: "reject" | "prompt"; feedback: string }
  /** 届かなかった＝何も畳んでいない。 */
  | { ok: false; reason: "say" | "respond" | "undelivered"; message?: string };

export async function deliverPlanComments(
  key: string,
  deps: {
    pending: boolean;
    respond: (feedback: string) => Promise<PlanRespondLike>;
    say: (feedback: string) => Promise<boolean>;
  },
): Promise<PlanDeliveryResult | null> {
  const list = unsentComments(getPlanComments(key));
  if (!list.length) return null; // 送るものが無い（ボタンも出ない状態）
  const feedback = formatPlanFeedback(list);
  const ids = list.map((c) => c.id);
  // 発話経路。say の戻り値を見ずに畳んだのが実障害の正体なので、ここが唯一の分岐点。
  const bySaying = async (): Promise<PlanDeliveryResult> => {
    if (!(await deps.say(feedback))) return { ok: false, reason: "say" };
    markPlanCommentsSent(key, ids);
    return { ok: true, via: "prompt", feedback };
  };
  if (!deps.pending) return bySaying();
  const res = await deps.respond(feedback);
  if (!res.ok) {
    // すでに別経路で判断済み（no_plan）なら、ただの発話として届ける。
    if (res.code === "no_plan") return bySaying();
    return { ok: false, reason: "respond", message: res.message };
  }
  // 却下は通ったがフィードバックは届いていない（コンポーザ復帰を確認できず）。
  if (!res.delivered) return { ok: false, reason: "undelivered", message: res.message };
  markPlanCommentsSent(key, ids);
  return { ok: true, via: "reject", feedback };
}
