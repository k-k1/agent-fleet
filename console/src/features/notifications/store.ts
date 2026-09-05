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
    // The voice is looked up from target.id as one of our own session names, so a notification
    // aimed at a shared session (a handoff) must not take it: looking up a voice by someone
    // else's session id would apply unrelated settings.
    const mine = n.target.type === "session";
    announce(text.speech, n.displayName, mine ? sessionVoiceOpts(n.target.id) : undefined, mine ? n.target.id : "", "session-notification");
  }
}

// NotificationOpenResult reports not only whether the target opened, but also that the target
// conversation is gone. A notification lives 7 days in the Control Plane while the conversation
// can disappear (the user deleted it, or a test wrote a ghost notification into the real HOME),
// so clicking would dead-end in a red "conversation not found" pane that says neither whether it
// was deleted or broken, nor which conversation it was. missingConversation is set only in that
// case (its value is the conversation name, empty when untitled) and the caller states the
// reason.
export interface NotificationOpenResult {
  opened: boolean;
  missingConversation?: string;
}

// conversationReachable folds a chatGet result into "may this conversation be opened as is".
// A failed fetch does not mean the conversation is gone: the 5xx the CP returns while the WS is
// starting, and a rejection from a dropped connection (which the caller turns into null), both
// leave the conversation alive — ChatView retries and opens it, so deciding "gone" here would
// divert the user to a session instead. Only a 4xx (chat_conversation_not_found) proves it.
export function conversationReachable(res: unknown): boolean {
  if (!res) return true; // a throw (dropped connection) is no proof of permanent absence
  if (typeof (res as { id?: string }).id === "string" && (res as { id?: string }).id) return true;
  return isTransientErr(res);
}

export async function openNotificationTarget(n: FleetNotification, split: boolean): Promise<NotificationOpenResult> {
  // A session report's destination is the operator CONVERSATION, not the reporting
  // session (docs/log/30) — the conversation id rides the payload.
  if ((n.kind === "session-report" || n.kind === "chat-auto-paused" || n.kind === "chat-context-pressure" || n.kind === "chat-context-overflow") && typeof n.payload.conversation_id === "string" && n.payload.conversation_id) {
    const convID = n.payload.conversation_id;
    // The fetch is only used to confirm permanent absence. A 5xx while the WS starts, or a
    // dropped connection (throw), is left to ChatView's retry and the conversation is opened
    // anyway — judging "gone" here would divert anyone who clicked during the boot window.
    const conv = await chatGet(convID).catch(() => null);
    if (conversationReachable(conv)) {
      openChat(convID);
      return { opened: true };
    }
    // The conversation really is gone. For session-report the reporting session (target.id) may
    // still exist, so fall back to it; the other kinds have no destination but the conversation.
    // The reason is stated here because a click on an OS notification takes the same path, and
    // leaving it to each UI layer would leave one of them silent.
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
  // A handoff received from another member (docs/log/77). Its destination is the shared view,
  // not one of our own sessions, so it must not fall through to the session resolution below.
  // It stops opening once the share is revoked, which is correct: the offer derives from the
  // share ACL, and when the ACL is gone so is the content.
  if (n.kind === "handoff-offer" && typeof n.payload.catalogId === "string" && n.payload.catalogId) {
    openSharedSession(n.payload.catalogId, split);
    return { opened: true };
  }
  // A failed or skipped scheduled run (docs/log/38). The destination is the schedule row in the
  // left rail and specifically its run history, the only place that answers "why didn't it run".
  // A failed firing has no session at all, so this must not fall through to the session
  // resolution below, which would end in an unrelated "session not in the list" warning.
  if (n.target.type === "schedule" && n.target.id) {
    useSchedulesStore.getState().revealSchedule(n.target.id);
    return { opened: true };
  }
  return openNotificationSession(n, split);
}

// openNotificationSession opens the notification's target session. It is both the default
// resolution when none of the per-kind destinations above applied, and the fallback for a report
// whose target conversation is gone (the reporting session usually still exists).
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
