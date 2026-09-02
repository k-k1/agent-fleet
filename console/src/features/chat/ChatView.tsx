import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties, KeyboardEvent, ClipboardEvent, ReactNode } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useChatStore } from "./store.ts";
import { chatGet, chatStream, chatStop, chatCreate, chatCompact, chatSetAgent, assistantGet, chatPasteImage, chatSuggestReplies } from "./api.ts";
import { errText, isTransientErr } from "../../core/api/client.ts";
import { takeChatSeed } from "../../lib/chatSeed.ts";
import { useDraft, moveDraft, clearDraft } from "../../lib/draft.ts";
import { useDragScroll } from "../../lib/dragScroll.ts";
import { autoGrowTextarea } from "../../lib/autoGrow.ts";
import { scrollComposerViewport } from "../../lib/keyScroll.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useSettings, setSetting, surfaceBg, surfaceAccent, effectiveTheme } from "../../lib/settings.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import {
  rankQuickReplies,
  recordQuickReply,
  isQuickReplyCandidate,
  forgetQuickReply,
  hideQuickReply,
  unhideQuickReply,
  pinQuickReply,
  unpinQuickReply,
  isQuickReplyPinned,
  quickReplyKey,
} from "../../lib/quickReplies.ts";
import {
  stepSuggestCycle,
  suggestFilterDraft,
  cycledSuggestion,
  type SuggestCycle,
} from "../../lib/suggestCycle.ts";
import { useChipMenu, SuggestChipMenu } from "../mirror/SuggestChipMenu.tsx";
import {
  startTts,
  stopTtsForReplacement,
  ttsOptsFromSettings,
  assistantVoiceOpts,
  workVoiceOpts,
  type TtsController,
  type TtsEndReason,
} from "./tts.ts";
import { ContextBar } from "../mirror/ContextBar.tsx";
import { ChatPlan } from "./ChatPlan.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { splitPastedImages, buildImagePrompt } from "../../lib/pastedImages.ts";
import { agentOf, AGENTS } from "../../agents/registry.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { SESSION_KINDS } from "../../types/session.ts";
import { getCachedConns, subscribeConns } from "../repos/connsCache.ts";
import { assistantName, assistantDesc } from "./assistantI18n.ts";
import { ChatMarkdown, StreamingMarkdown } from "./parts/ChatMarkdown.tsx";
import { ChatSteps } from "./parts/ChatSteps.tsx";
import { ChatMessageRow } from "./parts/ChatMessageRow.tsx";
import { kindClass } from "../../lib/sessionkind.ts";
import type { Conversation, ChatMessage, ChatStep } from "../../types/chat.ts";
import type { Assistant } from "../../types/assistant.ts";
import type { SessionKind } from "../../types/session.ts";

// ChatView renders one assistant-chat conversation (docs/log/19) — a headless-CLI LLM
// chat/translation thread. Unlike the terminal panes it never mounts xterm; it's a
// plain message list + composer over the /api/chat/* endpoints.
//
// Draft mode (docs/log/19): a pane opened from an assistant in the rail carries a
// draftAssistantId and NO conversationId — nothing is persisted yet. It shows the
// assistant's greeting; the conversation is created only when the user sends the first
// message, at which point the pane is promoted to the real conversation id.
interface ChatViewProps {
  conversationId: string | null;
  draftAssistantId?: string | null;
  paneId: string;
  active?: boolean;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}

// Backends a conversation can run on, straight off the registry cap — the same source as
// the assistant form's picker and the Agent's chatProviders map.
const CHAT_KINDS: SessionKind[] = SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat);

const chatDraftKey = (conversationId: string) => "af.chat-draft." + conversationId;

export function ChatView({ conversationId, draftAssistantId, paneId, active, headerActions }: ChatViewProps) {
  // Store bridge (old context values): promote a draft pane to its real
  // conversation id; bump the rail list; publish the busy chip.
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const promoteDraft = (pid: string, cid: string) =>
    setPaneTarget(pid, { content: { kind: "chat", conversationId: cid, draftAssistantId: null } });
  // Same signal MirrorView uses for a stopped session's read-only history view: while the
  // workspace agent is down, /api/chat/* 5xx's forever — show that plainly instead of
  // spinning "読み込み中…" indefinitely (chat.ts had no such distinction before).
  const wsRunning = useWorkspaceStore((s) => s.state) === "running";
  const bumpChatList = useChatStore((s) => s.bumpList);
  const markChatBusy = useChatStore((s) => s.markBusy);
  // In-flight turn state parked in the store, so closing + re-opening this pane mid-answer
  // re-attaches to the running turn instead of dropping its result (docs/log/19).
  const setLive = useChatStore((s) => s.setLive);
  const clearLive = useChatStore((s) => s.clearLive);
  const publishSnapshot = useChatStore((s) => s.publishSnapshot);
  const setConvTitle = useChatStore((s) => s.setConvTitle);
  const storeBusy = useChatStore((s) => (conversationId ? !!s.busy[conversationId] : false));
  const liveTurn = useChatStore((s) => (conversationId ? s.live[conversationId] : undefined));
  const snapshot = useChatStore((s) => (conversationId ? s.snapshots[conversationId] : undefined));
  const settings = useSettings();
  const toast = useToast();
  const askConfirm = useConfirm();
  const tr = useT();
  // Send key follows the user's global preference (shared with the Markdown mirror
  // composer): "mod-enter" = Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe);
  // "enter" = Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const [conv, setConv] = useState<Conversation | null>(null);
  const [draftAsst, setDraftAsst] = useState<Assistant | null>(null); // greeting source in draft mode
  // 回答の混線を防ぐキー。Opening another chat from the rail REPLACES the active pane's
  // content (layout/ops.openActive) — it does not remount this view — while a streaming
  // turn is a detached fetch that outlives the switch. So every piece of turn state below
  // is tagged with the chat it belongs to and only rendered while the pane still shows
  // that chat; otherwise assistant A's spinner, answer, error and karaoke would paint
  // onto assistant B's transcript. paneKey = the conversation id, or a draft's assistant
  // until its first turn creates the conversation.
  const paneKey = conversationId ?? (draftAssistantId ? "draft:" + draftAssistantId : null);
  const paneKeyRef = useRef(paneKey); // same value, readable from async turn callbacks
  paneKeyRef.current = paneKey;
  // Composer draft, persisted per conversation (draft panes key by assistant) so a
  // reload — or the browser dying — keeps what you were typing (mirrors MirrorView).
  const draftKey = conversationId
    ? chatDraftKey(conversationId)
    : draftAssistantId
      ? "af.chat-draft.asst." + draftAssistantId
      : null;
  const [input, setInput] = useDraft(draftKey);
  const [attachments, setAttachments] = useState<{ path: string; name: string; url: string }[]>([]);
  const [pasting, setPasting] = useState(false); // an image upload is in flight
  // Chats this pane is streaming a turn for. A set, not a flag: after switching away
  // mid-answer the old turn keeps running here, and the new chat must still be sendable.
  const [sendingKeys, setSendingKeys] = useState<Record<string, true>>({});
  const markSending = (key: string, on: boolean) =>
    setSendingKeys((m) => {
      if (on) return m[key] ? m : { ...m, [key]: true };
      if (!m[key]) return m;
      const n = { ...m };
      delete n[key];
      return n;
    });
  const sending = !!paneKey && !!sendingKeys[paneKey];
  const [compactKey, setCompactKey] = useState<string | null>(null); // 要約引き継ぎ実行中の会話（docs/log/33）
  const compacting = !!paneKey && compactKey === paneKey;
  // エージェント切替（docs/log/19）: 実行中の会話と、ピッカーの開閉。
  const [switchKey, setSwitchKey] = useState<string | null>(null);
  const switching = !!paneKey && switchKey === paneKey;
  const [agentPickerOpen, setAgentPickerOpen] = useState(false);
  const agentTagRef = useRef<HTMLButtonElement | null>(null);
  const agentMenuRef = useRef<HTMLDivElement | null>(null);
  // 接続状況は常設のリポジトリレールが温めた共有キャッシュから読む（HandoffModal と同じ
  // 作法）。冷えていれば null＝「不明」で、ピッカーは何も塞がない。
  const chatConns = useSyncExternalStore(subscribeConns, getCachedConns, getCachedConns);
  // 作業計画パネル（docs/log/33 第5段）の開閉。ペイン跨ぎで持ち回らない純粋な表示状態。
  const [planOpen, setPlanOpen] = useState(false);
  // a reloaded turn is still running on the backend; polling for the reply
  const [reattachKey, setReattachKey] = useState<string | null>(null);
  const reattaching = !!conversationId && reattachKey === conversationId;
  // handoff: fire this first turn automatically once the conversation loads
  const [pendingAuto, setPendingAuto] = useState<{ key: string; text: string } | null>(null);
  const [histIdx, setHistIdx] = useState<number | null>(null); // position in composer history (↑/↓ recall), or null
  // 返信サジェスト v2: ✨ボタンで取得した LLM 候補（Layer A のチップ列にマージ）と取得中フラグ。
  const [llmSuggestions, setLlmSuggestions] = useState<string[]>([]);
  const [suggesting, setSuggesting] = useState(false);
  // 入力途中の Tab 補完サイクル（lib/suggestCycle）。null = サイクル中でない。
  const [cycle, setCycle] = useState<SuggestCycle | null>(null);
  const suggestRef = useRef<HTMLDivElement>(null); // チップ行（Tab でここへフォーカスを移す）
  // 1行に収めた候補列をマウスのドラッグ/縦ホイールで左右スクロール（スワイプは既定動作）。
  // チップ行はストリーミング中に消えて戻るので、返り値のコールバック ref で付け替える
  // （ref オブジェクト任せだと戻ってきた要素にリスナーが付かない — dragScroll.ts の注記）。
  const attachSuggestRow = useDragScroll(suggestRef);
  // チップの右クリック / 長タップ / Menu キーで開くメニュー（ピン留め・削除）。MirrorView と共有。
  const chipMenu = useChipMenu();
  // 読み上げ中の文（ライブ配信カラオケ・docs/log/19）と、直近のターンエラー。どちらも
  // 発生元の会話で括り、別チャットへ切り替えた後に相手の吹き出しへ出ないようにする。
  const [karaoke, setKaraoke] = useState<{ key: string; text: string } | null>(null);
  const [err, setErr] = useState<{ key: string; text: string } | null>(null);
  const karaokeText = karaoke && karaoke.key === paneKey ? karaoke.text : null;
  // Clear only OUR highlight: a turn ending in one chat must not wipe the sentence
  // another chat is currently speaking.
  const clearKaraokeFor = (key: string) => setKaraoke((k) => (k && k.key !== key ? k : null));
  const error = err && err.key === paneKey ? err.text : "";
  const setErrorFor = (key: string | null, text: string) => setErr(text && key ? { key, text } : null);
  const [loadError, setLoadError] = useState("");
  const [loadingConv, setLoadingConv] = useState(false); // fetching the conversation (with retry while the WS agent boots)
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);
  const convRef = useRef<Conversation | null>(null); // mirror of conv, to guard reloads
  // Aborts an in-flight streaming turn, per chat: a turn left running when the pane was
  // pointed at another chat must still be the one 中断 stops when you come back to it.
  const abortsRef = useRef(new Map<string, AbortController>());
  // 音声読み上げ（docs/log/24）。有効時のみ生成。The pane plays one voice at a time, so the
  // slot is tagged with the chat that owns it: a turn finishing in the chat you switched
  // AWAY from must not adopt (or tear down) the playback the current chat just started.
  const ttsRef = useRef<{ key: string; ctl: TtsController } | null>(null);
  const paneTts = (key: string) => (ttsRef.current?.key === key ? ttsRef.current.ctl : null);
  const setPaneTts = (key: string, ctl: TtsController | null) => {
    if (ctl) ttsRef.current = { key, ctl };
    else if (ttsRef.current?.key === key) ttsRef.current = null;
  };
  const applyConv = (c: Conversation | null) => {
    convRef.current = c;
    setConv(c);
  };
  // Adopt a server copy only while the pane still shows that chat — a turn that finishes
  // after the user switched away publishes to the store instead (whoever shows it picks
  // it up), rather than dropping conversation A's transcript into conversation B's pane.
  const applyConvIfCurrent = (c: Conversation) => {
    if (paneKeyRef.current === c.id) applyConv(c);
  };

  // エージェント切替ピッカー: 外側クリック/Esc で閉じ、チップの真下にビューポート内で配置
  // （AssistantSection の ＋ ピッカーと同じ作法）。会話を切り替えたら開いたままにしない。
  useDismiss([agentTagRef, agentMenuRef], agentPickerOpen, () => setAgentPickerOpen(false));
  useLayoutEffect(() => {
    const el = agentMenuRef.current;
    const anchor = agentTagRef.current;
    if (!agentPickerOpen || !el || !anchor) return;
    el.style.position = "fixed";
    el.style.right = "auto";
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.left, a.bottom + 4);
  }, [agentPickerOpen]);
  useEffect(() => {
    setAgentPickerOpen(false);
  }, [conversationId]);

  // アシスタントの声（docs/log/24）: 明示指定（assistant.voice、作成/編集で設定）を読み上げの
  // 上書きに使う。draft はロード済みの draftAsst から、既存会話は assistant_id で 1 回引く。
  // 未指定（""）は assistantVoiceOpts が「セッションごとに声」ON のときプールから割り当てる。
  const [assistVoice, setAssistVoice] = useState("");
  const assistId = conv?.assistant_id || draftAssistantId || undefined;
  useEffect(() => {
    if (draftAsst && draftAsst.id === assistId) {
      setAssistVoice(draftAsst.voice || "");
      return;
    }
    setAssistVoice("");
    if (!assistId) return;
    let cancelled = false;
    assistantGet(assistId)
      .then((a) => {
        if (!cancelled && a && a.id) setAssistVoice(a.voice || "");
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [assistId, draftAsst]);

  // Load the conversation, or (draft mode) the assistant's greeting. Guarded so that
  // promoting a just-created draft (conversationId flips null→realId) doesn't reload and
  // interrupt the in-flight first turn — convRef already holds that conversation.
  useEffect(() => {
    let cancelled = false;
    let timer = 0;
    setLoadError("");
    setHistIdx(null); // switching conversations/drafts resets composer history-recall
    if (conversationId) {
      if (convRef.current?.id === conversationId) return; // already have it (e.g. just promoted)
      // Genuine switch to another chat (not a draft promotion): drop the previous chat's
      // error, and its pasted images — those were uploaded into THAT conversation's store
      // and must not ride along with the next prompt.
      setErr(null);
      clearAttachments();
      applyConv(null);
      setDraftAsst(null);
      // One-shot seed (Phase C prefill, or a session handoff). auto=false prefills the
      // composer for review; auto=true stashes the text to fire automatically once the
      // conversation has loaded (handoff — アシスタントを直接呼び出す). With no seed the
      // persisted draft (which useDraft reloads on the key change) is left standing.
      const seed = takeChatSeed(conversationId);
      if (seed) {
        if (seed.auto) setPendingAuto({ key: conversationId, text: seed.text });
        else setInput(seed.text);
      }
      // Load the conversation, retrying transient failures. When the workspace agent is
      // still booting (WS just started), the CP answers with an empty/gateway response
      // that api() resolves as { error } (NOT a throw) — reading that as "no id" would
      // stick the pane on 会話が見つかりません forever. So only a genuine 404
      // (chat_conversation_not_found = the conversation was deleted) is terminal; anything
      // else is retried with backoff until the backend is reachable, and we also retry
      // when the tab regains focus.
      let tries = 0;
      const retry = () => {
        if (cancelled) return;
        const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
        tries++;
        timer = window.setTimeout(load, delay);
      };
      const load = () => {
        setLoadingConv(true);
        chatGet(conversationId)
          .then((c) => {
            if (cancelled) return;
            if (c && c.id) {
              setLoadingConv(false);
              applyConv(c);
              return;
            }
            // A transient gateway failure (agent still booting) is retried; a genuine
            // 404 (code chat_conversation_not_found) or any other resolved-but-empty
            // response is terminal → 会話が見つかりません.
            if (isTransientErr(c)) {
              retry(); // keep the loading state up and try again
              return;
            }
            setLoadingConv(false);
            setLoadError(t("chat.not_found"));
          })
          .catch(() => {
            if (!cancelled) retry();
          });
      };
      const onVis = () => {
        if (!document.hidden && !cancelled && !convRef.current) {
          tries = 0;
          window.clearTimeout(timer);
          load();
        }
      };
      load();
      document.addEventListener("visibilitychange", onVis);
      return () => {
        cancelled = true;
        window.clearTimeout(timer);
        document.removeEventListener("visibilitychange", onVis);
      };
    } else if (draftAssistantId) {
      setLoadingConv(false);
      setErr(null);
      clearAttachments(); // a fresh draft starts clean — no stale error / pasted images (see above)
      applyConv(null);
      setDraftAsst(null);
      assistantGet(draftAssistantId)
        .then((a) => {
          if (!cancelled && a && a.id) setDraftAsst(a);
        })
        .catch(() => {});
    } else {
      setLoadingConv(false);
      applyConv(null);
      setDraftAsst(null);
    }
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [conversationId, draftAssistantId]);

  // Re-attach to a turn that completed while this pane was closed (or that another pane
  // ran): when a fresher conversation snapshot for this id lands in the store, adopt it.
  // Guarded by updated_at so a stale snapshot never clobbers a newer local state.
  useEffect(() => {
    if (!snapshot || snapshot.id !== conversationId) return;
    const cur = convRef.current;
    if (cur && cur.id === snapshot.id && cur.updated_at >= snapshot.updated_at) return;
    applyConv(snapshot);
  }, [snapshot, conversationId]);

  // Re-attach after a reload: a streaming turn is detached from its SSE request on the
  // backend, so a browser reload aborts the stream but the turn keeps running and saves
  // its reply. chatGet reports in_progress while that turn runs; poll until the reply
  // lands so the answer isn't lost (it would otherwise show only the user's message and a
  // frozen transcript). Skipped when THIS pane is the one sending — that path streams live.
  const inProgress = !!conv?.in_progress;
  useEffect(() => {
    if (!conversationId || sending || !inProgress) {
      setReattachKey(null);
      return;
    }
    setReattachKey(conversationId);
    let alive = true;
    let tries = 0;
    const maxTries = 200; // ~280s at 1.4s — past the backend chatTimeout ceiling
    let timer = 0;
    const tick = async () => {
      if (!alive) return;
      try {
        const c = await chatGet(conversationId);
        if (!alive) return;
        if (c && c.id) {
          applyConv(c);
          if (!c.in_progress) {
            setReattachKey(null);
            return; // reply landed (or the turn ended) — stop polling
          }
        }
      } catch {
        /* transient fetch failure — keep polling */
      }
      if (++tries >= maxTries) {
        setReattachKey(null);
        return;
      }
      timer = window.setTimeout(tick, 1400);
    };
    timer = window.setTimeout(tick, 1400);
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversationId, inProgress, sending]);

  // af_write conversations receive server-pushed session reports and auto turns
  // (docs/log/30) with no client-initiated request, so poll lightly while the pane is
  // OPEN — not just while it's the focused pane — to pick them up. Gating this on
  // `active` (= single || focused) meant a report/auto-turn never surfaced in an open
  // but unfocused operator pane until it was closed and reopened (the mount refetch);
  // it also starved the reattach poller below, which only kicks in once THIS client has
  // observed in_progress. A fresher updated_at adopts the server copy; a turn in flight
  // flips in_progress, which the reattach poller above then takes over.
  const reportCapable = conv?.tools === "af_write";
  useEffect(() => {
    if (!conversationId || !reportCapable) return;
    let alive = true;
    const timer = window.setInterval(async () => {
      if (!alive || sending) return;
      try {
        const c = await chatGet(conversationId);
        if (!alive || sending || !c || !c.id) return;
        const cur = convRef.current;
        if (!cur || cur.id !== c.id || c.updated_at > cur.updated_at || !!c.in_progress !== !!cur.in_progress) {
          applyConv(c);
        }
      } catch {
        /* transient — keep polling */
      }
    }, 5000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversationId, reportCapable, sending]);

  // Keep the transcript pinned to the newest turn (and to the thinking indicator). While
  // live karaoke is following the spoken sentence, defer to its scrollIntoView instead —
  // otherwise bottom-pin and the karaoke follow fight over the scroll position.
  useEffect(() => {
    if (karaokeText) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [conv?.messages.length, sending, liveTurn, karaokeText]);

  // Auto-grow the composer to fit its content (up to the CSS max-height, then it scrolls),
  // same as MirrorView's composer — 縮みを外へ出さない計測は lib/autoGrow.ts に集約。
  // 走るのは入力が変わるたび（seed の流し込み・送信時のクリアも含む）。
  useEffect(() => {
    autoGrowTextarea(inputRef.current);
  }, [input]);

  // Focus the composer when this pane becomes the active chat (opening a conversation or
  // an assistant draft) — but NOT on touch devices, where auto-focus would pop the
  // on-screen keyboard just from opening the chat to read it (mirrors MirrorView).
  useEffect(() => {
    if (active && !coarsePointer() && (conversationId || draftAssistantId)) inputRef.current?.focus();
  }, [active, conversationId, draftAssistantId]);

  // Image attach = upload + saved path referenced in the prompt. Offered where the
  // headless backend can actually open the path — claude (`-p`, Read tool) and codex
  // (`codex exec`, view_image; live-verified). opencode is excluded on purpose:
  // `opencode run` declines image input on non-vision models (big-pickle, live-verified),
  // and the chat can't know the model is vision-capable.
  const chatAgent = conv?.agent || draftAsst?.agent || "";
  const canAttach = chatAgent === "claude" || chatAgent === "codex";

  // Ensure a real conversation exists (approach A): a draft is created + promoted before
  // the first upload or send, so image attachments have a conversation id to post to.
  // 画像ペーストのアップロード中に送信が走るなど、作成中に再入しても会話を二重作成
  // しないよう、進行中の作成 Promise に相乗りする（成功すれば convRef が埋まり、
  // 失敗すれば解放されて再試行できる）。
  const ensureConvInflight = useRef<Promise<Conversation | null> | null>(null);
  const ensureConv = (): Promise<Conversation | null> => {
    if (convRef.current) return Promise.resolve(convRef.current);
    if (!draftAssistantId) return Promise.resolve(null);
    if (!ensureConvInflight.current) {
      ensureConvInflight.current = (async () => {
        try {
          const title = input.trim().slice(0, 40) || t("chat.new_title");
          const created = await chatCreate(draftAssistantId, title);
          if (!created || !created.id) return null;
          // グループ選択中に始めた会話はそのグループへ自動所属（docs/log/52 §1）。
          autoAddToActiveWorkingSet("convs", created.id);
          // Re-key the persisted composer draft to the real conversation, so the promotion's
          // key flip (useDraft reloads from storage) doesn't wipe the text mid-composition
          // (paste path: the user is still typing when this runs).
          moveDraft(draftKey, chatDraftKey(created.id));
          applyConv(created);
          promoteDraft(paneId, created.id);
          // The pane now shows the created conversation — move the key with it before any
          // await, so the first turn's callbacks recognise it as the current chat.
          paneKeyRef.current = created.id;
          setDraftAsst(null);
          return created;
        } finally {
          ensureConvInflight.current = null;
        }
      })();
    }
    return ensureConvInflight.current;
  };

  const removeAttachment = (i: number) => {
    setAttachments((a) => {
      if (a[i]) URL.revokeObjectURL(a[i].url);
      return a.filter((_, idx) => idx !== i);
    });
  };
  const clearAttachments = () => {
    setAttachments((a) => {
      a.forEach((x) => URL.revokeObjectURL(x.url));
      return [];
    });
  };

  // Paste image(s) from the clipboard into the composer: upload each to this chat and
  // hold it as an attachment chip. Non-image pastes fall through as ordinary text.
  const onPaste = async (e: ClipboardEvent<HTMLTextAreaElement>) => {
    if (!canAttach) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      if (it.kind === "file" && it.type.startsWith("image/")) {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (!files.length) return; // ordinary text paste — let it happen
    e.preventDefault();
    setPasting(true);
    // Approach A: a draft needs a real conversation id before we can upload.
    let target: Conversation | null = convRef.current;
    if (!target) {
      try {
        target = await ensureConv();
      } catch {
        target = null;
      }
    }
    if (!target) {
      setPasting(false);
      toast(t("chat.create_failed"));
      return;
    }
    for (const f of files) {
      try {
        const res = await chatPasteImage(target.id, f);
        if (res.status < 300 && res.path && res.name) {
          const path = res.path;
          const nm = res.name;
          const url = URL.createObjectURL(f);
          setAttachments((a) => [...a, { path, name: nm, url }]);
        } else {
          toast(res.error ? errText(res.error) : t("chat.image_paste_failed"));
        }
      } catch {
        toast(t("chat.image_paste_failed_net"));
      }
    }
    setPasting(false);
    inputRef.current?.focus();
  };

  // docs/log/33 第2段: 要約引き継ぎ（手動コンパクション）。バックエンドが要約1ターン →
  // resume ハンドル全リセット → 要約の次ターン注入準備まで行い、更新済み会話を返す。
  // 実行中は busy を店に立てて他ペインの送信もブロック（バックエンドの会話ロックと整合）。
  const doCompact = async () => {
    if (!conversationId || compacting || showStreaming) return;
    if (
      !(await askConfirm({
        title: tr("chat.compact_confirm_title"),
        body: tr("chat.compact_confirm_body"),
        confirmLabel: tr("chat.compact_btn"),
      }))
    )
      return;
    const key = conversationId;
    setErrorFor(key, "");
    setCompactKey(key);
    markChatBusy(key, true);
    try {
      const c2 = await chatCompact(key);
      if (c2 && c2.id) {
        applyConvIfCurrent(c2);
        publishSnapshot(c2); // 他ペイン/一覧にも新しい会話状態を届ける
        bumpChatList();
      } else {
        setErrorFor(key, c2?.error ? errText(c2.error) : tr("chat.compact_failed"));
      }
    } catch {
      setErrorFor(key, tr("chat.compact_failed"));
    } finally {
      markChatBusy(key, false);
      setCompactKey((k) => (k === key ? null : k));
    }
  };

  // 途中でバックエンド（CLI）を切り替える（docs/log/19）。設定「エージェント優先順位」は新規
  // 会話にしか効かないので、進行中の会話を動かす導線はここだけ。バックエンドはピン留めと
  // モデルを差し替え、新エージェントがまだ知らない履歴は次の送信でまとめて再生される。
  const doSwitchAgent = async (kind: SessionKind) => {
    if (!conversationId || kind === (conv?.agent as SessionKind | undefined) || showStreaming || compacting) return;
    const key = conversationId;
    setAgentPickerOpen(false);
    setErrorFor(key, "");
    setSwitchKey(key);
    try {
      const c2 = await chatSetAgent(key, kind);
      if (c2 && c2.id) {
        applyConvIfCurrent(c2);
        publishSnapshot(c2); // 他ペイン/一覧にも新しい会話状態を届ける
        bumpChatList();
      } else {
        setErrorFor(key, c2?.error ? errText(c2.error) : tr("chat.switch_agent_failed"));
      }
    } catch {
      setErrorFor(key, tr("chat.switch_agent_failed"));
    } finally {
      setSwitchKey((k) => (k === key ? null : k));
    }
  };

  const send = async (override?: string) => {
    // Block a second turn on this conversation, whether it was started here or by another
    // pane whose turn is still running in the background (store busy).
    if (!paneKey || sending || compacting || (conversationId && storeBusy)) return;
    // `override` drives the auto-sent handoff first turn (its text isn't in the composer).
    const text = (override ?? input).trim();
    const paths = attachments.map((a) => a.path);
    if (!text && !paths.length) return;
    // 返信サジェスト（lib/quickReplies）の学習: 短い純テキストのみ取り込む。一度メニューから消した文でも、
    // 自分で送り直したなら「また使う」意思表示なので隠しを解除する（MirrorView と同挙動）。
    if (text && isQuickReplyCandidate(text, paths.length > 0)) {
      setSetting("quickReplies", recordQuickReply(settings.quickReplies || {}, text, Date.now()));
      const hidden = settings.quickRepliesHidden || [];
      const unhidden = unhideQuickReply(hidden, text);
      if (unhidden !== hidden) setSetting("quickRepliesHidden", unhidden);
    }
    // A draft is keyed by its assistant until the conversation exists (see paneKey).
    const startKey = paneKey;
    setErrorFor(startKey, "");
    markSending(startKey, true);

    // Draft: create + promote the conversation before the first turn (approach A).
    let target: Conversation | null = conv;
    if (!target) {
      try {
        target = await ensureConv();
      } catch {
        target = null;
      }
      if (!target) {
        setErrorFor(startKey, t("chat.create_failed"));
        markSending(startKey, false);
        return;
      }
    }
    if (target.id !== startKey) {
      // Promoted draft: the turn now belongs to the created conversation.
      markSending(startKey, false);
      markSending(target.id, true);
    }

    // Append the machine-facing image instruction + paths (kind-appropriate wording:
    // Read tool for claude, tool-neutral for codex). The bubble strips it back out
    // (splitPastedImages) and shows thumbnails instead.
    const prompt = buildImagePrompt(text, paths, chatAgent);
    // Optimistically show the user's turn (full prompt so pasted-image thumbnails render
    // immediately); the server echoes the full conversation on done.
    const userMsg: ChatMessage = { role: "user", content: prompt, ts: Date.now() };
    setConv((c) => (c ? { ...c, messages: [...c.messages, userMsg] } : c));
    setInput("");
    setHistIdx(null); // sending leaves history-recall mode
    // Drop the stored draft synchronously: on a just-promoted first turn the key flip
    // makes useDraft reload from storage, which would resurrect the moved (now sent) text.
    clearDraft(chatDraftKey(target.id));
    clearAttachments();
    // On touch devices, drop focus so the soft keyboard (GBoard) retracts once the
    // turn is sent — the reply is what the user wants to read, not keep typing.
    if (coarsePointer()) inputRef.current?.blur();
    const convId = target.id;
    const ac = new AbortController();
    abortsRef.current.set(convId, ac);
    // 小声読みが OFF のときは従来どおり delta をライブ再生。ON のときは、途中テキストを
    // onStep で「作業過程」と確定してから小声で読み、onDone の最終回答を通常声で読む。
    const baseVoice = {
      ...(assistantVoiceOpts(target.assistant_id || draftAssistantId || undefined, assistVoice) ?? {}),
      paneId,
    };
    const workMode = settings.ttsWorkRead;
    const makeTts = (work = false, onEnd?: (reason: TtsEndReason) => void) =>
      settings.ttsEnabled
        ? startTts(
            {
              ...ttsOptsFromSettings(settings),
              // アシスタントの声: 明示指定 > プール割り当て（セッションごとに声 ON 時）> 設定の話者
              ...baseVoice,
              ...(work ? workVoiceOpts(baseVoice, workMode) : undefined),
            },
            work ? t("chat.tts_source_work") : t("chat.label"),
            (reason) => {
              if (!work) clearKaraokeFor(convId);
              onEnd?.(reason);
            },
            "",
            // 通常声はライブ中も最終回答確定後もカラオケ表示する。
            // 作業過程の小声再生だけは disclosure 内なのでハイライトしない。
            !work ? (t) => setKaraoke({ key: convId, text: t }) : undefined,
          )
        : null;
    stopTtsForReplacement(ttsRef.current?.ctl ?? null); // whatever this pane was reading
    setPaneTts(convId, workMode === "off" ? makeTts() : null);

    // 作業過程は確定順に 1 本ずつ読む。次の step が来ても再生中の step は止めず、
    // 最終回答が来た時点でだけ再生中・未再生をまとめて破棄して通常声へ譲る。
    // 他の読み上げに置換された場合も、それを即座に置換し返さず、グローバル再生が
    // 空いてから続きを再開する。
    const workQueue: string[] = [];
    let workCurrent: TtsController | null = null;
    let workClosed = false;
    let unsubscribeWork = () => {};
    let pumpWork = () => {};
    pumpWork = () => {
      if (workMode === "off" || workClosed || workCurrent || !workQueue.length) return;
      const st = useTtsStore.getState();
      if (st.active || st.speaking) return;
      const text = workQueue.shift()!;
      const c = makeTts(true, (reason) => {
        if (workCurrent === c) workCurrent = null;
        if (paneTts(convId) === c) setPaneTts(convId, null);
        if (reason === "explicit") {
          workClosed = true;
          workQueue.length = 0;
          unsubscribeWork();
        } else {
          queueMicrotask(pumpWork);
        }
      });
      if (!c) return;
      workCurrent = c;
      setPaneTts(convId, c);
      c.push(text);
      c.flush();
    };
    unsubscribeWork =
      workMode === "off"
        ? () => {}
        : useTtsStore.subscribe(() => {
            queueMicrotask(pumpWork);
          });
    const closeWork = () => {
      unsubscribeWork();
      if (workClosed) return;
      workClosed = true;
      workQueue.length = 0;
      stopTtsForReplacement(workCurrent);
      if (paneTts(convId) === workCurrent) setPaneTts(convId, null);
      workCurrent = null;
    };
    let streamDone = false;
    markChatBusy(convId, true); // publish 進行中 to the rail
    // The live reply + working steps + final conversation live in the store, keyed by
    // conversation, and the bubble renders from there — so the turn survives this pane
    // being closed OR pointed at another chat, and can only ever be painted into the
    // conversation it belongs to.
    let acc = ""; // current (tentative) answer text
    const steps: ChatStep[] = []; // working steps committed so far this turn
    let liveAgent: SessionKind | undefined;
    // Tear the streaming turn down in one batched render: applying the final conversation
    // (which now ends with the assistant reply) and removing the still-streaming bubble must
    // happen together. If the teardown runs only AFTER `await chatStream` resolves, a frame
    // slips in where BOTH show — the completed reply plus the slightly-behind (throttled)
    // streaming copy — which reads as the answer being erased and rewritten (打ち消し→再描画).
    const teardown = () => {
      clearLive(convId);
      markChatBusy(convId, false);
      clearKaraokeFor(convId);
      markSending(convId, false);
      abortsRef.current.delete(convId);
    };
    await chatStream(
      convId,
      prompt,
      {
        onAgent: (actual) => {
          liveAgent = actual;
          setLive(convId, { text: acc, steps: [...steps], agent: actual });
        },
        onDelta: (t) => {
          acc += t;
          setLive(convId, { text: acc, steps, agent: liveAgent }); // steps only change in onStep
          if (workMode === "off") paneTts(convId)?.push(t);
        },
        onStep: (step) => {
          // A tool-using message finished: its narration becomes a working step and the
          // tentative answer resets, so the next message streams as a fresh answer.
          steps.push(step);
          acc = "";
          clearKaraokeFor(convId);
          setLive(convId, { text: "", steps: [...steps], agent: liveAgent });
          if (workMode === "off") {
            stopTtsForReplacement(paneTts(convId));
            setPaneTts(convId, makeTts()); // 従来動作: 次の tentative message をライブ再生
          } else if (step.text?.trim()) {
            workQueue.push(step.text);
            pumpWork();
          }
        },
        onError: (m) => setErrorFor(convId, m),
        onDone: (updated) => {
          streamDone = true;
          if (updated) {
            applyConvIfCurrent(updated);
            publishSnapshot(updated); // reaches any live pane, even after this one unmounts
          }
          teardown(); // same synchronous batch as applyConv → no duplicate-bubble frame
          if (workMode === "off") {
            paneTts(convId)?.flush();
          } else {
            // 最終回答の到着で、残っている小声再生を置換して通常声へ戻す。
            closeWork();
            const finalText = acc.trim() || updated?.messages.at(-1)?.content || "";
            const c = finalText ? makeTts() : null;
            setPaneTts(convId, c);
            c?.push(finalText);
            c?.flush();
          }
        },
      },
      ac.signal,
    );
    // Abort/error paths emit no done event. Work playback must not outlive the turn.
    if (!streamDone) closeWork();
    // Abort/error paths emit no done event, so clear the streaming state here too. teardown is
    // idempotent — after a normal completion this re-run is a harmless no-op.
    teardown();
    bumpChatList(); // a new/updated thread should surface in the rail list
  };

  // Handoff auto-send: fire the seeded first turn once the conversation has loaded and is
  // idle. A ref keeps the effect calling the freshest send() without re-subscribing every
  // render; setPendingAuto(null) makes it one-shot (a later re-open won't resend).
  const sendRef = useRef(send);
  sendRef.current = send;
  useEffect(() => {
    if (pendingAuto == null) return;
    if (pendingAuto.key !== conversationId) return; // seeded for a chat this pane no longer shows
    if (!conv || conv.in_progress) return; // wait for the conversation to exist and no turn to be running
    if (sending || compacting) return;
    const text = pendingAuto.text;
    setPendingAuto(null);
    void sendRef.current(text);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- sendRef is a stable ref
  }, [pendingAuto, conversationId, conv, sending, compacting]);

  // Stop the in-flight turn. The turn is now detached from its SSE request on the backend
  // (so a reload can't kill it), which means aborting the fetch alone no longer cancels the
  // headless process — an explicit stop call does. We still abort the local fetch to stop
  // reading + tear down, and stop the 読み上げ.
  const stop = () => {
    if (!conversationId) return;
    void chatStop(conversationId); // cancel the detached backend turn
    abortsRef.current.get(conversationId)?.abort(); // …and this pane's reader, if it owns one
    paneTts(conversationId)?.stop(); // 読み上げも即停止（in-flight abort・再生停止・キュー破棄）
  };

  // ペインを閉じる/アンマウント時は読み上げを止める（音声が居残らないように）。
  useEffect(() => () => stopTtsForReplacement(ttsRef.current?.ctl ?? null), []);

  // Composer history = the user's own prompts in this conversation, so ↑ recalls them even
  // after a reload (built from conv, not just this mount). The visible words only — the
  // machine-facing pasted-image instruction is stripped. Newest last, consecutive dupes folded.
  const history: string[] = [];
  for (const m of conv?.messages ?? []) {
    if (m.role !== "user") continue;
    const s = splitPastedImages(m.content).text.trim();
    if (s && history[history.length - 1] !== s) history.push(s);
  }

  // 返信サジェスト（lib/quickReplies）。直近アシスタント発話を B-1 の文脈にし、頻度学習と統合。
  let chatLastReply = "";
  const msgs = conv?.messages ?? [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === "assistant") {
      chatLastReply = splitPastedImages(msgs[i].content).text.trim();
      break;
    }
  }
  // Tab 補完サイクル中は、絞り込みキーを「ユーザーが打った文字」に凍結する（入力欄は補完で
  // 候補そのものに変わっているので、そのまま渡すとチップ列が痩せてサイクルが崩れる）。
  const suggestDraft = suggestFilterDraft(cycle, input);
  const cycledText = cycledSuggestion(cycle, input); // いま入力欄に入っている候補（強調用）
  const learned = settings.quickRepliesEnabled
    ? rankQuickReplies(settings.quickReplies || {}, {
        draft: suggestDraft,
        lastReply: chatLastReply,
        locale: settings.locale,
        hidden: settings.quickRepliesHidden || [],
        pinned: settings.quickRepliesPinned || [],
        limit: 20, // チップ行は横スクロールなので、画面幅に収まらない分は流して見せる（ピンは別枠）
      })
    : [];
  // v2 の LLM 候補を先頭に、Layer A の学習候補を後ろにマージ（重複は畳む）。llm フラグで見た目を分ける。
  // 重複判定は学習キーと同じ畳み方（大小・空白に加えて全角半角）で行う。
  const llmSet = new Set(llmSuggestions.map((s) => quickReplyKey(s)));
  const suggestChips: { text: string; llm: boolean }[] = [
    ...llmSuggestions.map((text) => ({ text, llm: true })),
    ...learned.filter((s) => !llmSet.has(quickReplyKey(s))).map((text) => ({ text, llm: false })),
  ];
  // 会話が進む（新しい回答が来る）と古い LLM 候補は文脈遅れ。直近回答と会話切替で捨てる。
  useEffect(() => {
    setLlmSuggestions([]);
  }, [conversationId, chatLastReply]);
  // Tab 補完でたどっている候補が、1行スクロールのチップ行からはみ出していたら見える位置へ。
  useEffect(() => {
    if (!cycledText) return;
    const el = suggestRef.current?.querySelector<HTMLElement>(".chat-suggest-chip.cycling");
    // scrollIntoView は Chrome 150 で Promise を返す — 暗黙 return にすると effect の
    // クリーンアップ扱いで落ちるので、必ずブロック本体で捨てる（effect-implicit-return）。
    if (el) {
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }, [cycledText]);
  // サジェストのチップ: 通常クリックはコンポーサーへ差し込み、⌥/Alt で即送信（MirrorView と同挙動）。
  const applySuggestion = (text: string, immediate: boolean) => {
    if (showStreaming) return;
    if (immediate) {
      void send(text);
      return;
    }
    setInput(text);
    setHistIdx(null);
    // スマホ: チップ差し込みで textarea にフォーカスすると GBoard が開いて画面を覆う。タッチ端末では
    // フォーカスしない（キーボードを出さない）— ユーザーは送信 or タップして編集を選べる。
    if (coarsePointer()) {
      inputRef.current?.blur(); // 既に開いていたキーボードも畳む
      return;
    }
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }
    });
  };
  // メニューの「この候補を消す」: 学習を消し、隠しリストへ積み、ピンも外す（消すだけではシード/
  // 再学習で戻る）。LLM 候補（✨）は学習物ではないので、その場の候補列から外すだけ。
  const forgetSuggestion = (text: string, llm: boolean) => {
    if (llm) {
      setLlmSuggestions((prev) => prev.filter((s) => s !== text));
      return;
    }
    setSetting("quickReplies", forgetQuickReply(settings.quickReplies || {}, text));
    setSetting("quickRepliesHidden", hideQuickReply(settings.quickRepliesHidden || [], text));
    setSetting("quickRepliesPinned", unpinQuickReply(settings.quickRepliesPinned || [], text));
  };
  // メニューの「常に表示（ピン留め）」/「ピン留めを解除」。MirrorView と同じ扱い（ピンは隠しより
  // 強い意思表示なので、ピンするときは隠しを解除する）。
  const togglePin = (text: string) => {
    const pinned = settings.quickRepliesPinned || [];
    if (isQuickReplyPinned(pinned, text)) {
      setSetting("quickRepliesPinned", unpinQuickReply(pinned, text));
      return;
    }
    setSetting("quickRepliesPinned", pinQuickReply(pinned, text));
    setSetting("quickRepliesHidden", unhideQuickReply(settings.quickRepliesHidden || [], text));
  };
  // v2: ✨ボタン — 会話ログを一発ヘッドレス LLM に渡し、文脈に沿った返信候補をチップ列にマージ
  // （chat_suggest_reply.go）。会話が確定していない（下書き）ときは押せない。
  const fetchLlmSuggestions = async () => {
    if (!conversationId || suggesting) return;
    setSuggesting(true);
    try {
      const j = await chatSuggestReplies(conversationId);
      // apiJSON はサーバエラーを {error} で解決する — 失敗を「候補なし」トーストに化けさせない。
      if (j?.error) {
        toast(errText(j.error) || t("chat.suggest_failed"));
        return;
      }
      const list = Array.isArray(j?.suggestions) ? j.suggestions.filter((x): x is string => typeof x === "string") : [];
      setLlmSuggestions(list);
      if (!list.length) toast(t("chat.suggest_none"));
    } catch {
      toast(t("chat.suggest_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  // Recall the previous / next prompt (shared by ↑/↓ and the on-screen buttons shown on
  // phones, which have no arrow keys). Mirrors MirrorView's composer history.
  const recallPrev = () => {
    if (!history.length) return;
    const ni = histIdx !== null ? Math.max(0, histIdx - 1) : history.length - 1;
    setHistIdx(ni);
    setInput(history[ni]);
    inputRef.current?.focus();
  };
  const recallNext = () => {
    if (histIdx === null) return;
    const ni = histIdx + 1;
    if (ni >= history.length) {
      setHistIdx(null);
      setInput("");
    } else {
      setHistIdx(ni);
      setInput(history[ni]);
    }
    inputRef.current?.focus();
  };

  // 返信サジェストのフォーカスリング = ✨ボタン＋候補チップ（DOM 順）。MirrorView と同挙動。
  const suggestRing = (): HTMLButtonElement[] =>
    Array.from(suggestRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);

  // チップ行は1行スクロールなので、キー移動のフォーカス先が隠れないよう横だけ最小限追従させる
  // （focus 既定のスクロールは縦にも効いて本文が飛ぶため preventScroll で殺す）。
  const focusRingItem = (el: HTMLButtonElement) => {
    el.focus({ preventScroll: true });
    el.scrollIntoView({ block: "nearest", inline: "nearest" });
  };

  // リング内の移動。Tab/Shift+Tab は「候補＋入力欄」を一巡（端まで来たら入力欄へ戻る）。
  // ←/→ は候補内だけで循環。Escape で入力欄へ。処理したら true。
  const onSuggestNav = (e: KeyboardEvent<HTMLButtonElement>): boolean => {
    if (e.nativeEvent.isComposing) return false;
    if (e.key === "Escape") {
      e.preventDefault();
      inputRef.current?.focus();
      return true;
    }
    const ring = suggestRing();
    const i = ring.indexOf(e.currentTarget);
    if (i < 0 || !ring.length) return false;
    if (e.key === "Tab") {
      e.preventDefault();
      const next = e.shiftKey ? i - 1 : i + 1;
      if (next < 0 || next >= ring.length) inputRef.current?.focus(); // 端 → 入力欄へ戻る
      else focusRingItem(ring[next]);
      return true;
    }
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const d = e.key === "ArrowRight" ? 1 : -1;
      focusRingItem(ring[(i + d + ring.length) % ring.length]); // ←/→ は候補内で循環
      return true;
    }
    return false;
  };

  // チップ上のキー操作。移動系は onSuggestNav に委ね、Enter/Ctrl(⌘)+Enter の役割はコンポーサーの
  // 送信キー設定に合わせる: modSend なら mod+Enter=送信・素の Enter=差し込み、enter モードなら逆。
  const onSuggestKeyDown = (e: KeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => {
    if (onSuggestNav(e)) return;
    if (chipMenu.onKeyDown(e, text, llm)) return; // Menu キー / Shift+F10 → ピン留め・削除メニュー
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    e.preventDefault(); // ボタン既定の click（＝差し込み）と二重発火させない
    applySuggestion(text, modSend ? mod : !mod);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // 入力欄が空なら Tab で返信サジェストへ入る（＝入力欄→候補1→候補2→入力欄…のループ）。
    // 素の Tab は最初の「候補チップ」から（先頭の✨は飛ばす／Shift+Tab で戻れる）。Shift+Tab は
    // 逆回りなのでリング末尾から入る。テキストがあるときは従来どおりの Tab。
    if (e.key === "Tab" && !e.nativeEvent.isComposing && input === "") {
      const ring = suggestRing();
      const target = e.shiftKey
        ? ring[ring.length - 1]
        : suggestRef.current?.querySelector<HTMLButtonElement>(".chat-suggest-chip");
      if (target) {
        e.preventDefault();
        focusRingItem(target);
        return;
      }
    }
    // 入力途中の Tab は候補の補完サイクル（シェル流）。打った文字に前方一致する候補＝チップ行に
    // 見えているものを順に入力欄へ入れ、一周したら自分が打った文字へ戻る。Shift+Tab は逆回り。
    // 補完できる候補が無ければ何もせず、従来どおりの Tab（フォーカス移動）に落とす。
    if (e.key === "Tab" && !e.nativeEvent.isComposing && input !== "" && !showStreaming) {
      const next = stepSuggestCycle(cycle, input, suggestChips.map((c) => c.text), e.shiftKey);
      if (next) {
        e.preventDefault();
        setCycle(next);
        setInput(next.text);
        setHistIdx(null);
        // 値の差し替えでキャレットが動く（先頭に残る）ブラウザがあるので末尾に置き直す。
        requestAnimationFrame(() => {
          const el = inputRef.current;
          if (el) el.setSelectionRange(el.value.length, el.value.length);
        });
        return;
      }
    }
    // Scroll the message list without leaving the composer: Ctrl/⌘+↑/↓ nudges, PageUp/PageDown
    // (and Ctrl/⌘+[ / ]) page, Ctrl/⌘+End jumps to the newest message. Checked before history
    // recall so the modified arrows don't get swallowed by the ↑/↓ recall path below.
    if (!e.nativeEvent.isComposing && scrollComposerViewport(e, scrollRef.current)) return;
    // Shell-style history: ↑/↓ recall past prompts when the field is empty (or once recall
    // is underway). With text present, arrows move the caret as usual. Only the BARE arrows
    // recall — Shift+↑/↓ stays the textarea's select-by-line (it no longer scrolls the list).
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.nativeEvent.isComposing && !e.shiftKey && !e.altKey) {
      if (e.key === "ArrowUp" && (input === "" || histIdx !== null) && history.length) {
        e.preventDefault();
        recallPrev();
        return;
      }
      if (e.key === "ArrowDown" && histIdx !== null) {
        e.preventDefault();
        recallNext();
        return;
      }
    }
    // Don't intercept Enter while an IME candidate window is open (JP/CJK input).
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    if (modSend ? mod : !e.shiftKey && !mod) {
      // modSend: Ctrl/⌘+Enter submits (plain Enter newlines).
      // else:    Enter submits (Shift+Enter newlines).
      e.preventDefault();
      void send();
    }
  };

  // Header/badge come from the live conversation, or the draft assistant while composing.
  const agentKind = liveTurn?.agent || conv?.active_agent || conv?.agent || draftAsst?.agent || null;
  const agent = agentKind ? agentOf(agentKind) : null;
  const title = conv?.title || (draftAsst && assistantName(draftAsst)) || t("chat.label");
  // The pane's tab and the pop-out title bar label this chat by the same title, but they
  // outlive this view (a background tab has no ChatView at all). Publish it so a title the
  // backend just generated for a fresh conversation shows up there without waiting for the
  // rail's 15s list poll — which in a pop-out window never runs, there being no rail.
  useEffect(() => {
    if (conv?.id && conv.title) setConvTitle(conv.id, conv.title);
  }, [conv?.id, conv?.title, setConvTitle]);
  const isDraft = !conversationId && !!draftAssistantId;
  // A turn may be in flight because THIS pane is sending, because a background turn on this
  // conversation (started before the pane was closed + re-opened) is still running, or
  // because we reloaded into a detached turn and are polling for its reply (reattaching).
  const showStreaming = sending || storeBusy || reattaching;
  // Always the store's copy for THIS conversation — the pane never renders a turn it
  // merely happens to have started (that turn keeps streaming into its own chat).
  const streamBody = liveTurn?.text ?? "";
  const liveSteps = liveTurn?.steps ?? [];
  const empty = (!conv || conv.messages.length === 0) && !loadError && !showStreaming && !loadingConv;
  // Status chip like the Sessions list / MirrorView header: 停止中 when the workspace agent
  // is down (matches SessionRow/MirrorView's stateInfo for a stopped session), else 進行中
  // while streaming, else 待機中.
  const stateChip = !wsRunning
    ? { cls: "off", icon: "debug-pause", spin: false, text: tr("state.stopped") }
    : showStreaming
      ? { cls: "working", icon: "loading", spin: true, text: tr("chat.state_running") }
      : { cls: "on", icon: "check", spin: false, text: tr("chat.state_idle") };

  // When a turn finishes (streaming or reattach-poll → idle) on the active pane, return
  // focus to the composer so the user can type a follow-up right away. Fires on the
  // true→false transition of showStreaming, so it covers both the live send path and the
  // reattach path. Suppressed on touch devices for the same reason as the on-open focus
  // above — re-focusing would pop the on-screen keyboard the moment the answer lands.
  const wasStreamingRef = useRef(showStreaming);
  useEffect(() => {
    const was = wasStreamingRef.current;
    wasStreamingRef.current = showStreaming;
    if (was && !showStreaming && active && !coarsePointer()) inputRef.current?.focus();
  }, [showStreaming, active]);

  // assistantTheme scopes the base tokens (tokens.css) via data-theme; assistantColor gives
  // the assistant chat its own surface bg/accent (--chat-bg/--chat-accent, shared contract
  // with the mirror). Both derived for the chat's own effective theme. "inherit"/"default"
  // leave them unset so the chat follows the app theme.
  const asstEff = effectiveTheme(settings.assistantTheme, settings.theme);
  const asstBg = surfaceBg(settings.assistantColor, asstEff);
  const asstAccent = surfaceAccent(settings.assistantColor);

  return (
    <div
      className="chatview"
      data-theme={settings.assistantTheme !== "inherit" ? settings.assistantTheme : undefined}
      style={{
        ...(asstBg ? { "--chat-bg": asstBg } : {}),
        ...(asstAccent ? { "--chat-accent": asstAccent } : {}),
      } as CSSProperties}
    >
      <ViewHead className="fileinfo" actions={headerActions}>
        <span className="fi-name">
          <Icon name={draftAsst?.icon || "comment-discussion"} /> {title}
        </span>
        {/* エージェントのチップ。既存会話ではそのまま切替ピッカーのボタンを兼ねる
            （draft はまだ会話が無いので表示のみ）。 */}
        {agent && conversationId && (
          <button
            type="button"
            ref={agentTagRef}
            className={"kind-tag kind-" + kindClass(agentKind!) + " chat-agent-pick"}
            title={tr("chat.switch_agent_tip")}
            aria-haspopup="menu"
            aria-expanded={agentPickerOpen}
            disabled={switching || showStreaming || compacting || !wsRunning}
            onClick={() => setAgentPickerOpen((o) => !o)}
          >
            <Icon name={switching ? "loading" : agent.icon} spin={switching} />
            {agent.assistantName}
            <Icon name="chevron-down" className="chat-agent-caret" />
          </button>
        )}
        {agent && !conversationId && (
          <span className={"kind-tag kind-" + kindClass(agentKind!)}>
            <Icon name={agent.icon} />
            {agent.assistantName}
          </span>
        )}
        {agentPickerOpen &&
          createPortal(
            <div className="ui-menu chat-agent-menu" ref={agentMenuRef} role="menu" onMouseDown={(e) => e.stopPropagation()}>
              <div className="assistant-picker-label">{tr("chat.switch_agent")}</div>
              {CHAT_KINDS.map((k) => (
                <button
                  key={k}
                  type="button"
                  className="ui-menu-item"
                  role="menuitemradio"
                  aria-checked={k === conv?.agent}
                  // 未接続の CLI にピン留めしても、送信時に接続済みのバックエンドへ退避する
                  // だけ（chatProviderFor）＝選ばせても効かない。接続状況が分からないとき
                  // （キャッシュが冷えている）は塞がない。
                  disabled={!!chatConns && !agentOf(k).available({ conns: chatConns })}
                  title={
                    chatConns && !agentOf(k).available({ conns: chatConns })
                      ? tr("chat.switch_agent_offline")
                      : undefined
                  }
                  onClick={() => void doSwitchAgent(k)}
                >
                  <Icon name={k === conv?.agent ? "check" : "blank"} />
                  {/* kind の色は tokens.css の --kind-* が1ソース（agent-display-naming）。 */}
                  <span className="chat-agent-ic" style={{ color: `var(--kind-${kindClass(k)})` }}>
                    <Icon name={agentOf(k).icon} />
                  </span>
                  {agentOf(k).assistantName}
                </button>
              ))}
              <div className="chat-agent-note muted">{tr("chat.switch_agent_note")}</div>
            </div>,
            document.body,
          )}
        {(conv || showStreaming || !wsRunning) && (
          <span className={"session-state " + stateChip.cls}>
            <Icon name={stateChip.icon} spin={stateChip.spin} /> {stateChip.text}
          </span>
        )}
        {/* 作業計画（docs/log/33 第5段）: 圧縮を跨いで原文のまま運ばれる枠の開閉。計画が
            入っている会話は塗って示す — 「アシスタントが絶対に忘れない内容」がどれかを
            一目で分かるようにするのがこのバッジの役目。 */}
        {conversationId && (
          <button
            type="button"
            className={"chat-plan-toggle" + (conv?.plan ? " has-plan" : "") + (planOpen ? " open" : "")}
            title={tr("chat.plan.toggle_tip")}
            aria-expanded={planOpen}
            onClick={() => setPlanOpen((v) => !v)}
          >
            <Icon name="checklist" /> {tr("chat.plan.title")}
          </button>
        )}
      </ViewHead>
      {planOpen && conversationId && (
        <ChatPlan
          conversationId={conversationId}
          plan={conv?.plan || ""}
          updatedAt={conv?.plan_updated_at}
          disabled={compacting || switching || showStreaming || storeBusy || !wsRunning}
          onUpdated={(c2) => {
            applyConvIfCurrent(c2);
            publishSnapshot(c2); // 他ペイン/一覧にも新しい会話状態を届ける（圧縮と同じ流儀）
          }}
          onClose={() => setPlanOpen(false)}
        />
      )}
      {/* Context fill (docs/log/33): the same gauge the mirror shows, fed from the
          conversation's per-turn usage snapshot (chat_usage.go). Hidden until the
          first turn reports usage. The trailing 圧縮 button runs the summary
          handoff (docs/log/33 第2段). */}
      {conv?.context && conv.context.tokens > 0 && (
        <ContextBar
          read={conv.context.read || 0}
          create={conv.context.create || 0}
          fresh={conv.context.fresh || 0}
          model={conv.context.model}
          window={conv.context.window}
          action={
            conversationId ? (
              <button
                type="button"
                className="cb-action"
                disabled={compacting || showStreaming || !wsRunning}
                title={tr("chat.compact_tip")}
                onClick={() => void doCompact()}
              >
                <Icon name={compacting ? "loading" : "fold"} spin={compacting} />
                {compacting ? tr("chat.compacting") : tr("chat.compact_btn")}
              </button>
            ) : undefined
          }
        />
      )}
      {/* 縦へ送って読む面 — 横へはみ出しても横スワイプを殺さない（app/swipeGuard.ts）。 */}
      <div className="chat-scroll" data-swipe-y="" ref={scrollRef}>
        {loadError && (
          <div className="chat-error" role="alert">
            {loadError}
          </div>
        )}
        {/* Fetching the conversation (retried while the WS agent boots) — a spinner beats
            flashing the empty hint or a spurious "not found". Once we know the workspace
            itself is stopped, the retry can't succeed until Start is pressed — say so
            immediately instead of spinning forever (MirrorView's !loaded ws-stopped branch). */}
        {loadingConv && !conv && !loadError && (
          wsRunning ? (
            <div className="chat-empty">
              <Icon name="loading" spin /> {tr("common.loading")}
            </div>
          ) : (
            <div className="chat-empty">{tr("mirror.ws_stopped_history")}</div>
          )
        )}
        {/* Greeting: the assistant introduces itself while the chat hasn't started. */}
        {empty && draftAsst && (
          <div className="chat-greeting">
            <div className="chat-greeting-head">
              <Icon name={draftAsst.icon || "comment-discussion"} className="chat-greeting-ic" />
              <span className="chat-greeting-name">{assistantName(draftAsst)}</span>
            </div>
            <div className="chat-greeting-body">
              {assistantDesc(draftAsst) ? (
                <ChatMarkdown source={assistantDesc(draftAsst)!} breaks />
              ) : (
                tr("chat.greeting_empty")
              )}
            </div>
          </div>
        )}
        {empty && !draftAsst && !isDraft && (
          <div className="chat-empty">{tr("chat.empty_hint")}</div>
        )}
        {conv?.messages.map((m, i) => (
          <ChatMessageRow
            key={i}
            m={m}
            conv={conv}
            assistId={assistId}
            assistVoice={assistVoice}
            paneId={paneId}
            highlight={i === conv.messages.length - 1 ? karaokeText : null}
          />
        ))}
        {showStreaming && (
          <div className="chat-msg role-assistant">
            <div className="chat-role">{agent?.assistantName || tr("chat.assistant_fallback")}</div>
            {/* Working steps stream in above the answer, open so progress is visible. */}
            {liveSteps.length > 0 && <ChatSteps steps={liveSteps} defaultOpen live />}
            {streamBody ? (
              <div className="chat-body">
                <StreamingMarkdown text={streamBody} highlight={karaokeText} />
              </div>
            ) : liveSteps.length > 0 ? (
              // A step just committed; the next answer hasn't started streaming yet.
              <div className="chat-body">
                <span className="chat-thinking">
                  <Icon name="loading" spin /> {tr("chat.working")}
                </span>
              </div>
            ) : (
              <div className="chat-body">
                <span className="chat-thinking">
                  <Icon name="loading" spin /> {tr("chat.thinking")}
                </span>
              </div>
            )}
          </div>
        )}
      </div>
      {error && (
        <div className="chat-error" role="alert">
          {error}
        </div>
      )}
      <div className="chat-composer">
        {(attachments.length > 0 || pasting) && (
          <div className="chat-attach">
            {attachments.map((a, i) => (
              <div className="ca-chip" key={a.path}>
                <img className="ca-thumb" src={a.url} alt="" />
                <button type="button" className="ca-del" title={tr("chat.remove")} onClick={() => removeAttachment(i)}>
                  <Icon name="close" />
                </button>
              </div>
            ))}
            {pasting && (
              <span className="ca-loading">
                <Icon name="loading" spin /> {tr("chat.uploading")}
              </span>
            )}
          </div>
        )}
        {!wsRunning ? (
          // Workspace stopped: nothing here can succeed (no per-chat "alive" to resume —
          // the whole agent is down), so swap the input for MirrorView's same disabled
          // resume affordance instead of a composer that silently eats every keystroke.
          <div className="chat-composer-row chat-compose-resume">
            <button type="button" className="btn primary" disabled title={tr("mirror.ws_stopped")}>
              <Icon name="play" /> {tr("mirror.resume_continue")}
            </button>
            <span className="muted mirror-resume-hint">{tr("mirror.viewing_history_ws_stopped")}</span>
          </div>
        ) : (
        <>
        {/* 返信サジェスト: 常用短文＋直近回答に沿った候補（Layer A）＋✨の LLM 候補（v2）。
            クリックで差し込み・⌥で即送信。 */}
        {(conv || isDraft) && !showStreaming && (suggestChips.length > 0 || (settings.replySuggestEnabled && conversationId)) && (
          <div className="chat-suggest" ref={attachSuggestRow}>
            {settings.replySuggestEnabled && conversationId && (
              <button
                type="button"
                className="chat-suggest-ai"
                title={tr("chat.suggest_ai")}
                disabled={suggesting}
                onClick={fetchLlmSuggestions}
                onKeyDown={onSuggestNav} // Enter は既定の click（＝候補取得）に任せる
              >
                <Icon name={suggesting ? "loading" : "sparkle"} spin={suggesting} />
              </button>
            )}
            {suggestChips.map((sg) => (
              // ピン留めは先頭固定＋📌。削除/ピンは右クリック・長タップ・Menu キーのメニューから。
              <button
                key={(sg.llm ? "l:" : "a:") + sg.text}
                type="button"
                className={
                  "chat-suggest-chip" +
                  (sg.llm ? " llm" : "") +
                  (isQuickReplyPinned(settings.quickRepliesPinned, sg.text) ? " pinned" : "") +
                  (sg.text === cycledText ? " cycling" : "") // Tab でいま入力欄に入れている候補
                }
                aria-current={sg.text === cycledText ? "true" : undefined}
                title={tr("mirror.suggest_hint")}
                onClick={(e) => {
                  if (chipMenu.clickSwallowed()) return; // 長タップでメニューを出した指離し
                  applySuggestion(sg.text, e.ctrlKey || e.altKey || e.metaKey);
                }}
                onKeyDown={(e) => onSuggestKeyDown(e, sg.text, sg.llm)}
                {...chipMenu.chipProps(sg.text, sg.llm)}
              >
                {isQuickReplyPinned(settings.quickRepliesPinned, sg.text) && (
                  <Icon name="pinned" className="chat-suggest-pin" />
                )}
                {sg.text}
              </button>
            ))}
          </div>
        )}
        {chipMenu.menu && (
          <SuggestChipMenu
            menu={chipMenu.menu}
            pinned={isQuickReplyPinned(settings.quickRepliesPinned, chipMenu.menu.text)}
            onClose={chipMenu.close}
            onTogglePin={togglePin}
            onForget={forgetSuggestion}
          />
        )}
        <div className="chat-composer-row">
          {/* History nav for phones (no arrow keys); hidden on wider screens via CSS. */}
          <div className="chat-hist">
            <button
              type="button"
              className="btn chat-hist-btn"
              title={tr("chat.prev_input")}
              disabled={!history.length || (!conv && !isDraft) || showStreaming}
              onClick={recallPrev}
            >
              <Icon name="chevron-up" />
            </button>
            <button
              type="button"
              className="btn chat-hist-btn"
              title={tr("chat.next_input")}
              disabled={histIdx === null || (!conv && !isDraft) || showStreaming}
              onClick={recallNext}
            >
              <Icon name="chevron-down" />
            </button>
          </div>
          <textarea
            ref={inputRef}
            className="chat-input"
            value={input}
            placeholder={
              conv || isDraft
                ? canAttach
                  ? modSend
                    ? tr("chat.ph_mod_img")
                    : tr("chat.ph_enter_img")
                  : modSend
                    ? tr("chat.ph_mod")
                    : tr("chat.ph_enter")
                : tr("chat.ph_loading")
            }
            disabled={(!conv && !isDraft) || showStreaming}
            onChange={(e) => {
              setInput(e.target.value);
              setHistIdx(null); // typing leaves history-recall mode
            }}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
            rows={2}
          />
          {sending || reattaching ? (
            // reattaching: reloaded into a detached turn — stop still works via chatStop.
            <button type="button" className="btn chat-send chat-stop" onClick={stop} title={tr("chat.stop")}>
              <Icon name="debug-stop" />
            </button>
          ) : (
            <button
              type="button"
              className="btn primary chat-send"
              disabled={(!conv && !isDraft) || showStreaming || (!input.trim() && !attachments.length)}
              onClick={() => void send()}
              title={tr("chat.send")}
            >
              <Icon name="send" />
            </button>
          )}
        </div>
        </>
        )}
      </div>
    </div>
  );
}
