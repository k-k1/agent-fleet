// Chat store (zustand): rail-list refresh tick + per-conversation busy flags
// (進行中 chips published by ChatView, read by the AssistantSection rail).
// Replaces the old chatListKey / chatBusy context slices.
//
// It also parks the state of an *in-flight* streaming turn keyed by conversation id
// (`live` accumulating text, `snapshots` final conversation). A streaming turn is a
// floating fetch that outlives the ChatView that started it, so if the pane is closed
// mid-answer the turn still finishes on the backend — but its result would otherwise be
// delivered to an unmounted component and dropped. Publishing here lets a pane that is
// re-opened on the same conversation re-attach to the live turn and pick up the final
// answer instead of a stale pre-turn snapshot (docs/log/19: "close pane mid-stream" fix).
import { create } from "zustand";
import { chatList } from "./api.ts";
import { isTransientErr } from "../../core/api/client.ts";
import type { Conversation, ConversationMeta, ChatStep } from "../../types/chat.ts";
import type { SessionKind } from "../../types/session.ts";

// Live in-flight turn state: the tentative answer text plus the working steps committed so
// far (docs/log/19 分離), so a re-opened pane re-attaches to both the process and the answer.
export interface LiveTurn {
  text: string;
  steps: ChatStep[];
  agent?: SessionKind;
}

interface ChatStore {
  listTick: number;
  bumpList(): void;
  // Conversation list (light metas), shared app-wide so surfaces other than the
  // left-rail AssistantSection can resolve a conversation slug ("a…") — the
  // markdown auto-linkifier gates conv-slug links on existence here, the same way
  // session slugs gate on the sessions store. null = never loaded (distinguishes
  // "no chats yet" from "haven't asked"); AssistantSection keeps it fresh while
  // mounted, ensureConvs() covers surfaces that render before/without it.
  convs: ConversationMeta[] | null;
  setConvs(convs: ConversationMeta[]): void;
  busy: Record<string, boolean>;
  markBusy(id: string, busy: boolean): void;
  // Accumulating assistant reply + working steps of an in-flight turn, so a re-opened pane
  // can show the live answer/process rather than an empty spinner.
  live: Record<string, LiveTurn>;
  setLive(id: string, turn: LiveTurn): void;
  clearLive(id: string): void;
  // Latest conversation after a turn completes. Published even when the ChatView that ran
  // the turn is gone, so any pane still showing this conversation picks up the final answer.
  snapshots: Record<string, Conversation>;
  publishSnapshot(c: Conversation): void;
}

export const useChatStore = create<ChatStore>((set) => ({
  listTick: 0,
  bumpList: () => set((s) => ({ listTick: s.listTick + 1 })),
  convs: null,
  setConvs: (convs) => set({ convs }),
  busy: {},
  markBusy: (id, b) =>
    set((s) => {
      if (b) return s.busy[id] ? s : { busy: { ...s.busy, [id]: true } };
      if (!s.busy[id]) return s;
      const n = { ...s.busy };
      delete n[id];
      return { busy: n };
    }),
  live: {},
  setLive: (id, turn) => set((s) => ({ live: { ...s.live, [id]: turn } })),
  clearLive: (id) =>
    set((s) => {
      if (!(id in s.live)) return s;
      const n = { ...s.live };
      delete n[id];
      return { live: n };
    }),
  snapshots: {},
  publishSnapshot: (c) => set((s) => ({ snapshots: { ...s.snapshots, [c.id]: c } })),
}));

// ensureConvs loads the conversation list into the store once, for surfaces that
// need slug lookup before AssistantSection has fetched (or when it isn't mounted).
// A single in-flight promise dedups concurrent callers. A transport failure or a
// transient gateway error (api() resolves those as {error: http_5xx} while the
// agent boots — the ws-boot-view-stuck class of bug) leaves convs = null instead
// of confirming an empty list, so a later caller retries.
let convsLoading: Promise<void> | null = null;
export function ensureConvs(): Promise<void> {
  if (useChatStore.getState().convs !== null) return Promise.resolve();
  if (!convsLoading) {
    convsLoading = chatList()
      .then((r) => {
        if (!isTransientErr(r)) useChatStore.getState().setConvs(r.conversations || []);
      })
      .catch(() => {})
      .finally(() => {
        convsLoading = null;
      });
  }
  return convsLoading;
}

/** Poll every 15s while the tab is visible: bump listTick so AssistantSection's
 * useRetryLoad re-fetches. Unlike sessions/repos, chats are edited from any
 * browser on the account with no push channel between them — without this, a
 * tab open before a chat was created elsewhere never learns about it. No
 * immediate call: AssistantSection already fetches once on its own mount. */
export function startChatPolling(): () => void {
  const t = setInterval(() => {
    if (!document.hidden) useChatStore.getState().bumpList();
  }, 15000);
  return () => clearInterval(t);
}
