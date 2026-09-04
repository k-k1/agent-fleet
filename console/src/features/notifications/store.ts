import { create } from "zustand";
import { api, apiJSON, chatGet, isTransientErr } from "../../core/api/client.ts";
import { pushHealthy } from "../../core/push/events.ts";
import { getSettings } from "../../lib/settings.ts";
import { toast } from "../../ui/toast.ts";
import { t as tr } from "../../lib/i18n/index.ts";
import { activePane } from "../../layout/ops.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { announce, sessionVoiceOpts } from "../chat/tts.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { agentOf } from "../../agents/registry.ts";
import { openSessionChat, openSessionChatSplit, openSessionTerminal, openSessionTerminalSplit } from "../sessions/open.ts";
import { openChat } from "../chat/open.ts";
import { openRepoScm } from "../scm/open.ts";
import { openSharedSession } from "../sharing/open.ts";
import { useSchedulesStore } from "../schedules/store.ts";
import { unseenSessionEventIDs } from "./read.ts";
import { notificationWording } from "./wording.ts";

export type NotificationSourceState = "unknown" | "ready" | "offline" | "unsupported";
export interface FleetNotification {
  seq: number;
  id: string;
  kind: "answer-ready" | "question" | "plan-approval" | "permission-request" | "session-report" | "chat-auto-paused" | "chat-context-pressure" | "chat-context-overflow" | "usage-reset" | string;
  target: { type: string; id: string; kind?: string };
  displayName: string;
  payload: Record<string, unknown>;
  createdAt: string;
  seen: boolean;
}

interface NotificationState {
  items: FleetNotification[];
  maxSeq: number;
  unseenCount: number;
  sourceState: NotificationSourceState;
  initialized: boolean;
  reset(): void;
  /** Adopt one GET /api/notifications-shaped payload (poll result or pushed
   * api/events frame) and deliver the rows newer than the last seen maxSeq. */
  applyPayload(d: { error?: unknown; items?: FleetNotification[]; maxSeq?: number; unseenCount?: number; sourceState?: NotificationSourceState }): void;
  refresh(): Promise<void>;
  markSeen(throughSeq?: number, eventIds?: string[]): Promise<void>;
}

let generation = 0;
let requestSeq = 0;
let appliedSeq = 0;

export function replayNotification(n: FleetNotification): void {
  const text = notificationWording(n);
  const voice = n.target.type === "session" ? sessionVoiceOpts(n.target.id) : undefined;
  announce(text.speech, n.displayName || text.body, voice, n.target.type === "session" ? n.target.id : "", "manual");
}

async function deliver(n: FleetNotification): Promise<void> {
  if (n.seen) return;
  const active = activePane(useLayoutStore.getState().layout)?.session;
  if (n.target.type === "session" && active === n.target.id) {
    return;
  }
  const text = notificationWording(n);
  const s = getSettings();
  const deviceDelivery = n.kind !== "usage-reset" || s.usageResetNotify;
  if (deviceDelivery && "Notification" in window && Notification.permission === "granted") {
    try {
      const osn = new Notification(text.title, { body: text.body, tag: n.id });
      osn.onclick = () => {
        window.focus();
        void openNotificationTarget(n, false);
        void useNotificationStore.getState().markSeen(undefined, [n.id]);
        osn.close();
      };
    } catch {}
  }
  if (n.kind === "usage-reset") {
    if (s.usageResetNotify && s.ttsEnabled) announce(text.speech, text.body, undefined, "", "usage-notification");
  } else if (s.ttsSessionNotify) {
    // ⚠️ target.id を自分のセッション名として声色を引くので、共有セッション宛（引き継ぎ）は
    // 通さない。他人のセッション id で voice を引くと、無関係な設定が当たる。
    const mine = n.target.type === "session";
    announce(text.speech, n.displayName, mine ? sessionVoiceOpts(n.target.id) : undefined, mine ? n.target.id : "", "session-notification");
  }
}

// NotificationOpenResult は「開けたか」だけでなく「**宛先の会話がもう無い**」を返す。
// 通知は Control Plane に 7 日残るのに会話は消えうる（利用者の削除、あるいはテストが
// 実 HOME へ落とした幽霊通知）ので、開いた先が「会話が見つかりません」の赤字ペインで
// 行き止まりになる——押した人には、消えたのか壊れたのかも、どの会話だったのかも分からない。
// missingConversation はその場合だけ立ち（値は会話名。無題なら空文字）、呼び出し側が
// 理由を出す。
export interface NotificationOpenResult {
  opened: boolean;
  missingConversation?: string;
}

// conversationReachable は chatGet の結果を「その会話をこのまま開いてよいか」に畳む。
// ⚠️ **「取れなかった＝消えた」ではない**。WS の起動中に CP が返す 5xx も、通信断で
// reject した場合（呼び出し側が null にする）も、会話は生きている — ChatView が再試行して
// 開く経路なので、ここで「消えた」と判定するとセッションへ飛ばして利用者を混乱させる。
// 消えたと言えるのは **4xx（chat_conversation_not_found）で解決したとき**だけ。
export function conversationReachable(res: unknown): boolean {
  if (!res) return true; // throw（通信断）— 恒久的に無いことの証拠にはならない
  if (typeof (res as { id?: string }).id === "string" && (res as { id?: string }).id) return true;
  return isTransientErr(res);
}

export async function openNotificationTarget(n: FleetNotification, split: boolean): Promise<NotificationOpenResult> {
  // A session report's destination is the operator CONVERSATION, not the reporting
  // session (docs/log/30) — the conversation id rides the payload.
  if ((n.kind === "session-report" || n.kind === "chat-auto-paused" || n.kind === "chat-context-pressure" || n.kind === "chat-context-overflow") && typeof n.payload.conversation_id === "string" && n.payload.conversation_id) {
    const convID = n.payload.conversation_id;
    // 取得は「恒久的に無い」ことの確認にだけ使う。WS 起動中の 5xx や通信断（throw）は
    // ChatView 側の再試行に任せてそのまま開く —— ここで「消えた」と判定すると、起動待ちの
    // 一瞬に押しただけでセッションへ飛ばされる。
    const conv = await chatGet(convID).catch(() => null);
    if (conversationReachable(conv)) {
      openChat(convID);
      return { opened: true };
    }
    // 会話は本当に無い。session-report なら報告元セッション（target.id）が残っていること
    // があるので、そこへ落とす。他の kind は宛先が会話しかない。
    // 理由はここで言う（OS 通知からのクリックも同じ経路を通るので、UI 層へ返して各所で
    // 出し分けると片方が無言になる）。
    const title = typeof n.payload.conversationTitle === "string" ? n.payload.conversationTitle : "";
    const label = title || tr("noti.conversation_untitled");
    const r = n.kind === "session-report" ? await openNotificationSession(n, split) : { opened: false };
    toast(tr(r.opened ? "noti.conversation_gone_session" : "noti.conversation_gone", { title: label }), { kind: "warn" });
    return { ...r, missingConversation: title };
  }
  // A submodule notice belongs to a working copy, not a session (it is filed before any
  // session exists). Its answer to "what now?" is the Source Control view, which lists the
  // submodules and whether they are fetched.
  if (n.kind === "submodule-sync" && typeof n.payload.repo === "string" && n.payload.repo) {
    openRepoScm(n.payload.repo);
    return { opened: true };
  }
  // メンバーから受け取った引き継ぎ（docs/log/77）。行き先は自分のセッションではなく**共有ビュー**
  // なので、下の session 解決には落とせない。共有が切れたあとは開けないが、それは正しい
  // （offer は共有 ACL の派生物で、ACL が消えれば本文も消えている）。
  if (n.kind === "handoff-offer" && typeof n.payload.catalogId === "string" && n.payload.catalogId) {
    openSharedSession(n.payload.catalogId, split);
    return { opened: true };
  }
  // 定時実行の失敗/スキップ（docs/log/38）。行き先はセッションではなく左レールの
  // スケジュール行で、しかも**実行履歴**——「なぜ動かなかったのか」はそこにしか無い。
  // 落ちた発火にはそもそもセッションが存在しないので、下の session 解決には落とせない
  // （落とすと「該当セッションは一覧にありません」という無関係な警告で終わる）。
  if (n.target.type === "schedule" && n.target.id) {
    useSchedulesStore.getState().revealSchedule(n.target.id);
    return { opened: true };
  }
  return openNotificationSession(n, split);
}

// openNotificationSession は通知の target（セッション）を開く。上の kind 別の行き先が
// どれも当てはまらなかったときの既定の解決であり、**宛先の会話が消えていた報告**の
// フォールバックでもある（報告元セッションは残っていることが多い）。
async function openNotificationSession(n: FleetNotification, split: boolean): Promise<NotificationOpenResult> {
  if (n.target.type !== "session" || !n.target.id) return { opened: false };
  let session = useSessionsStore.getState().sessions.find((s) => s.name === n.target.id);
  if (!session) {
    await useSessionsStore.getState().refresh();
    session = useSessionsStore.getState().sessions.find((s) => s.name === n.target.id);
  }
  if (!session) return { opened: false };
  const caps = agentOf(session.kind).caps;
  if (session.alive) {
    (caps.chat ? (split ? openSessionChatSplit : openSessionChat) : split ? openSessionTerminalSplit : openSessionTerminal)(session.name);
    return { opened: true };
  }
  if (caps.transcript) {
    (split ? openSessionChatSplit : openSessionChat)(session.name);
    return { opened: true };
  }
  return { opened: false };
}

export const useNotificationStore = create<NotificationState>((set, get) => ({
  items: [], maxSeq: 0, unseenCount: 0, sourceState: "unknown", initialized: false,
  reset: () => {
    generation++;
    set({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "unknown", initialized: false });
  },
  applyPayload(d) {
    if (d.error) return;
    const items: FleetNotification[] = d.items || [];
    const previous = get().maxSeq;
    const initialized = get().initialized;
    set({ items, maxSeq: d.maxSeq || 0, unseenCount: d.unseenCount || 0, sourceState: d.sourceState || "offline", initialized: true });
    if (initialized) {
      for (const n of items.filter((x) => x.seq > previous).sort((a, b) => a.seq - b.seq)) void deliver(n);
    }
  },
  async refresh() {
    try {
      const ownGeneration = generation;
      const ownRequest = ++requestSeq;
      const d = await api("api/notifications");
      if (ownGeneration !== generation || ownRequest < appliedSeq) return;
      appliedSeq = ownRequest;
      get().applyPayload(d);
    } catch {}
  },
  async markSeen(throughSeq, eventIds) {
    let result;
    try {
      result = await apiJSON("api/notifications/seen", "POST", { throughSeq, eventIds });
    } catch {
      return;
    }
    if (result.error) return;
    const ids = new Set(eventIds || []);
    set((s) => {
      const matches = (n: FleetNotification) => !!(throughSeq && n.seq <= throughSeq) || ids.has(n.id);
      const markedLoaded = s.items.filter((n) => !n.seen && matches(n)).length;
      return {
        items: s.items.map((n) => (matches(n) ? { ...n, seen: true } : n)),
        // A throughSeq at the newest loaded row clears every older server row too.
        // Event-id marks only decrement the rows actually acknowledged.
        unseenCount: throughSeq && throughSeq >= s.maxSeq ? 0 : Math.max(0, s.unseenCount - markedLoaded),
      };
    });
  },
}));

// Opening a session is an explicit acknowledgement of every pending event for that
// session. Watch both sides: layout changes catch navigation/history, notification
// changes catch an event that arrives while its session is already active.
export function wireNotificationReadOnActiveSession(): () => void {
  const pending = new Set<string>();
  const sync = () => {
    const sessionName = activePane(useLayoutStore.getState().layout)?.session || "";
    const ids = unseenSessionEventIDs(useNotificationStore.getState().items, sessionName)
      .filter((id) => !pending.has(id));
    if (!ids.length) return;
    ids.forEach((id) => pending.add(id));
    void useNotificationStore.getState().markSeen(undefined, ids).finally(() => {
      ids.forEach((id) => pending.delete(id));
    });
  };
  const unLayout = useLayoutStore.subscribe(sync);
  const unNotifications = useNotificationStore.subscribe((state, previous) => {
    if (state.items !== previous.items) sync();
  });
  sync();
  return () => {
    unLayout();
    unNotifications();
  };
}

// applyPushedNotifications adopts a pushed api/events frame. Bumping the
// request/applied counters marks any in-flight poll as stale so its (older)
// response gets discarded on arrival — the same ordering guard refresh() uses.
export function applyPushedNotifications(d: Parameters<NotificationState["applyPayload"]>[0]): void {
  appliedSeq = ++requestSeq;
  useNotificationStore.getState().applyPayload(d);
}

// Poll fallback: skipped while the push channel covers this stream (api/events).
// While hidden the push channel disconnects and this poll resumes at its slow
// 15s cadence — preserving today's hidden-tab OS-notification delivery.
export function startNotificationPolling(): () => void {
  let stopped = false;
  let timer = 0;
  const load = async () => {
    if (!pushHealthy()) await useNotificationStore.getState().refresh();
    if (!stopped) timer = window.setTimeout(load, document.hidden ? 15000 : 5000);
  };
  void load();
  return () => { stopped = true; window.clearTimeout(timer); };
}
