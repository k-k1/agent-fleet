import { create } from "zustand";
import { api, apiJSON } from "../../core/api/client.ts";
import { getSettings } from "../../lib/settings.ts";
import { t } from "../../lib/i18n/index.ts";
import { activePane } from "../../layout/ops.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { announce, sessionVoiceOpts } from "../chat/tts.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { agentOf } from "../../agents/registry.ts";
import { openSessionChat, openSessionChatSplit, openSessionTerminal, openSessionTerminalSplit } from "../sessions/open.ts";
import { openChat } from "../chat/open.ts";
import { unseenSessionEventIDs } from "./read.ts";

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
  refresh(): Promise<void>;
  markSeen(throughSeq?: number, eventIds?: string[]): Promise<void>;
}

let generation = 0;
let requestSeq = 0;
let appliedSeq = 0;

function wording(n: FleetNotification): { title: string; body: string; speech: string } {
  const name = n.displayName || t("notif.default_name");
  if (n.kind === "answer-ready") return { title: t("notif.answer_ready.title"), body: name, speech: t("notif.answer_ready.speech", { name }) };
  if (n.kind === "question") return { title: t("notif.question.title"), body: name, speech: t("notif.question.speech", { name }) };
  if (n.kind === "plan-approval") return { title: t("notif.plan_approval.title"), body: name, speech: t("notif.plan_approval.speech", { name }) };
  if (n.kind === "permission-request") return { title: t("notif.permission_request.title"), body: name, speech: t("notif.permission_request.speech", { name }) };
  if (n.kind === "session-report") {
    // A session reported back to its operator conversation (docs/30); the body names
    // the reporting session, the click target is the conversation.
    return { title: t("notif.session_report.title"), body: name, speech: t("notif.session_report.speech", { name }) };
  }
  if (n.kind === "chat-auto-paused") {
    // The operator's unattended auto-turn budget ran out and the loop paused (docs/30).
    // The body names the conversation; clicking opens it so the user can reply to resume.
    const title = String(n.payload.conversationTitle || name);
    return { title: t("notif.chat_auto_paused.title"), body: title, speech: t("notif.chat_auto_paused.speech") };
  }
  if (n.kind === "chat-context-pressure") {
    // A conversation crossed the context-fill threshold (chat_usage.go) — matters even
    // when it happened on an unattended auto turn; clicking opens the conversation.
    const title = String(n.payload.conversationTitle || name);
    return { title: t("notif.chat_ctx_pressure.title"), body: title, speech: t("notif.chat_ctx_pressure.speech") };
  }
  if (n.kind === "chat-context-overflow") {
    // A turn failed on context overflow and couldn't be auto-healed (chat_recover.go);
    // clicking opens the conversation so the user can compact or start fresh.
    const title = String(n.payload.conversationTitle || name);
    return { title: t("notif.chat_ctx_overflow.title"), body: title, speech: t("notif.chat_ctx_overflow.speech") };
  }
  const rawSource = String(n.payload.source || n.displayName || "AI");
  const source = rawSource === "claude" ? "Claude" : rawSource === "codex" ? "Codex" : rawSource;
  const win = n.payload.windowKey === "5h" ? t("notif.window.5h") : t("notif.window.week");
  return { title: t("notif.usage_reset.title", { source }), body: t("notif.usage_reset.body", { window: win }), speech: t("notif.usage_reset.speech", { source, window: win }) };
}

export function replayNotification(n: FleetNotification): void {
  const text = wording(n);
  const voice = n.target.type === "session" ? sessionVoiceOpts(n.target.id) : undefined;
  announce(text.speech, n.displayName || text.body, voice, n.target.type === "session" ? n.target.id : "", "manual");
}

async function deliver(n: FleetNotification): Promise<void> {
  if (n.seen) return;
  const active = activePane(useLayoutStore.getState().layout)?.session;
  if (n.target.type === "session" && active === n.target.id) {
    return;
  }
  const text = wording(n);
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
    announce(text.speech, n.displayName, sessionVoiceOpts(n.target.id), n.target.id, "session-notification");
  }
}

export async function openNotificationTarget(n: FleetNotification, split: boolean): Promise<boolean> {
  // A session report's destination is the operator CONVERSATION, not the reporting
  // session (docs/30) — the conversation id rides the payload.
  if ((n.kind === "session-report" || n.kind === "chat-auto-paused" || n.kind === "chat-context-pressure" || n.kind === "chat-context-overflow") && typeof n.payload.conversation_id === "string" && n.payload.conversation_id) {
    openChat(n.payload.conversation_id);
    return true;
  }
  if (n.target.type !== "session" || !n.target.id) return false;
  let session = useSessionsStore.getState().sessions.find((s) => s.name === n.target.id);
  if (!session) {
    await useSessionsStore.getState().refresh();
    session = useSessionsStore.getState().sessions.find((s) => s.name === n.target.id);
  }
  if (!session) return false;
  const caps = agentOf(session.kind).caps;
  if (session.alive) {
    (caps.chat ? (split ? openSessionChatSplit : openSessionChat) : split ? openSessionTerminalSplit : openSessionTerminal)(session.name);
    return true;
  }
  if (caps.transcript) {
    (split ? openSessionChatSplit : openSessionChat)(session.name);
    return true;
  }
  return false;
}

export const useNotificationStore = create<NotificationState>((set, get) => ({
  items: [], maxSeq: 0, unseenCount: 0, sourceState: "unknown", initialized: false,
  reset: () => {
    generation++;
    set({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "unknown", initialized: false });
  },
  async refresh() {
    try {
      const ownGeneration = generation;
      const ownRequest = ++requestSeq;
      const d = await api("api/notifications");
      if (ownGeneration !== generation || ownRequest < appliedSeq) return;
      appliedSeq = ownRequest;
      if (d.error) return;
      const items: FleetNotification[] = d.items || [];
      const previous = get().maxSeq;
      const initialized = get().initialized;
      set({ items, maxSeq: d.maxSeq || 0, unseenCount: d.unseenCount || 0, sourceState: d.sourceState || "offline", initialized: true });
      if (initialized) {
        for (const n of items.filter((x) => x.seq > previous).sort((a, b) => a.seq - b.seq)) void deliver(n);
      }
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

export function startNotificationPolling(): () => void {
  let stopped = false;
  let timer = 0;
  const load = async () => {
    await useNotificationStore.getState().refresh();
    if (!stopped) timer = window.setTimeout(load, document.hidden ? 15000 : 5000);
  };
  void load();
  return () => { stopped = true; window.clearTimeout(timer); };
}
