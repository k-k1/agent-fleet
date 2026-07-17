import { useEffect, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent, ClipboardEvent } from "react";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import { useChatStore } from "./store.ts";
import { chatGet, chatStream, chatStop, chatCreate, assistantGet, chatPasteImage } from "./api.ts";
import { errText, raw, isTransientErr } from "../../core/api/client.ts";
import { takeChatSeed } from "../../lib/chatSeed.ts";
import { useDraft, moveDraft, clearDraft } from "../../lib/draft.ts";
import { scrollComposerViewport } from "../../lib/keyScroll.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { t, tCount, useT } from "../../lib/i18n/index.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useSettings, surfaceBg, surfaceAccent, effectiveTheme } from "../../lib/settings.ts";
import {
  startTts,
  stopTtsForReplacement,
  ttsOptsFromSettings,
  assistantVoiceOpts,
  workVoiceOpts,
  type TtsController,
  type TtsEndReason,
  type TtsOptions,
} from "./tts.ts";
import { readTurn, collectBlocks, blockIndexAt, type TurnReadHandle } from "../mirror/turnTts.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { splitPastedImages, buildImagePrompt } from "../../lib/pastedImages.ts";
import { agentOf } from "../../agents/registry.ts";
import { assistantName, assistantDesc } from "./assistantI18n.ts";
import { kindClass } from "../../lib/sessionkind.ts";
import type { Conversation, ChatMessage, ChatStep } from "../../types/chat.ts";
import type { Assistant } from "../../types/assistant.ts";

// ChatView renders one assistant-chat conversation (docs/19) — a headless-CLI LLM
// chat/translation thread. Unlike the terminal panes it never mounts xterm; it's a
// plain message list + composer over the /api/chat/* endpoints.
//
// Draft mode (docs/19): a pane opened from an assistant in the rail carries a
// draftAssistantId and NO conversationId — nothing is persisted yet. It shows the
// assistant's greeting; the conversation is created only when the user sends the first
// message, at which point the pane is promoted to the real conversation id.
interface ChatViewProps {
  conversationId: string | null;
  draftAssistantId?: string | null;
  paneId: string;
  active?: boolean;
}

const chatDraftKey = (conversationId: string) => "af.chat-draft." + conversationId;

export function ChatView({ conversationId, draftAssistantId, paneId, active }: ChatViewProps) {
  // Store bridge (old context values): promote a draft pane to its real
  // conversation id; bump the rail list; publish the busy chip.
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const promoteDraft = (pid: string, cid: string) =>
    setPaneTarget(pid, { content: { kind: "chat", conversationId: cid, draftAssistantId: null } });
  const bumpChatList = useChatStore((s) => s.bumpList);
  const markChatBusy = useChatStore((s) => s.markBusy);
  // In-flight turn state parked in the store, so closing + re-opening this pane mid-answer
  // re-attaches to the running turn instead of dropping its result (docs/19).
  const setLive = useChatStore((s) => s.setLive);
  const clearLive = useChatStore((s) => s.clearLive);
  const publishSnapshot = useChatStore((s) => s.publishSnapshot);
  const storeBusy = useChatStore((s) => (conversationId ? !!s.busy[conversationId] : false));
  const liveTurn = useChatStore((s) => (conversationId ? s.live[conversationId] : undefined));
  const snapshot = useChatStore((s) => (conversationId ? s.snapshots[conversationId] : undefined));
  const settings = useSettings();
  const toast = useToast();
  const tr = useT();
  // Send key follows the user's global preference (shared with the Markdown mirror
  // composer): "mod-enter" = Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe);
  // "enter" = Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const [conv, setConv] = useState<Conversation | null>(null);
  const [draftAsst, setDraftAsst] = useState<Assistant | null>(null); // greeting source in draft mode
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
  const [sending, setSending] = useState(false);
  const [streamText, setStreamText] = useState(""); // live-accumulating (tentative) answer
  const [streamSteps, setStreamSteps] = useState<ChatStep[]>([]); // working steps committed this turn (分離)
  const [reattaching, setReattaching] = useState(false); // a reloaded turn is still running on the backend; polling for the reply
  const [histIdx, setHistIdx] = useState<number | null>(null); // position in composer history (↑/↓ recall), or null
  const [karaoke, setKaraoke] = useState<string | null>(null); // 読み上げ中の文（ライブ配信カラオケ・docs/19）
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const [loadingConv, setLoadingConv] = useState(false); // fetching the conversation (with retry while the WS agent boots)
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);
  const convRef = useRef<Conversation | null>(null); // mirror of conv, to guard reloads
  const abortRef = useRef<AbortController | null>(null); // aborts the in-flight streaming turn
  const ttsRef = useRef<TtsController | null>(null); // 音声読み上げ（docs/24）。有効時のみ生成
  const applyConv = (c: Conversation | null) => {
    convRef.current = c;
    setConv(c);
  };

  // アシスタントの声（docs/24）: 明示指定（assistant.voice、作成/編集で設定）を読み上げの
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
    setError("");
    setLoadError("");
    setHistIdx(null); // switching conversations/drafts resets composer history-recall
    if (conversationId) {
      if (convRef.current?.id === conversationId) return; // already have it (e.g. just promoted)
      applyConv(null);
      setDraftAsst(null);
      // One-shot composer prefill (Phase C); with no seed the persisted draft (which
      // useDraft reloads on the key change) is left standing.
      const seed = takeChatSeed(conversationId);
      if (seed !== undefined) setInput(seed);
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
      setReattaching(false);
      return;
    }
    setReattaching(true);
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
            setReattaching(false);
            return; // reply landed (or the turn ended) — stop polling
          }
        }
      } catch {
        /* transient fetch failure — keep polling */
      }
      if (++tries >= maxTries) {
        setReattaching(false);
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

  // Keep the transcript pinned to the newest turn (and to the thinking indicator). While
  // live karaoke is following the spoken sentence, defer to its scrollIntoView instead —
  // otherwise bottom-pin and the karaoke follow fight over the scroll position.
  useEffect(() => {
    if (karaoke) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [conv?.messages.length, sending, streamText, streamSteps, liveTurn, karaoke]);

  // Auto-grow the composer to fit its content (up to the CSS max-height, then it scrolls),
  // same as MirrorView's composer. Reset to auto first so it also shrinks when text is
  // deleted; runs on every input change (including a seed prefill / cleared-on-send).
  useEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = el.scrollHeight + "px";
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
  const ensureConv = async (): Promise<Conversation | null> => {
    if (convRef.current) return convRef.current;
    if (!draftAssistantId) return null;
    const title = input.trim().slice(0, 40) || t("chat.new_title");
    const created = await chatCreate(draftAssistantId, title);
    if (!created || !created.id) return null;
    // Re-key the persisted composer draft to the real conversation, so the promotion's
    // key flip (useDraft reloads from storage) doesn't wipe the text mid-composition
    // (paste path: the user is still typing when this runs).
    moveDraft(draftKey, chatDraftKey(created.id));
    applyConv(created);
    promoteDraft(paneId, created.id);
    setDraftAsst(null);
    return created;
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

  const send = async () => {
    // Block a second turn on this conversation, whether it was started here or by another
    // pane whose turn is still running in the background (store busy).
    if (sending || (conversationId && storeBusy)) return;
    const text = input.trim();
    const paths = attachments.map((a) => a.path);
    if (!text && !paths.length) return;
    setError("");
    setSending(true);
    setStreamText("");

    // Draft: create + promote the conversation before the first turn (approach A).
    let target: Conversation | null = conv;
    if (!target) {
      try {
        target = await ensureConv();
      } catch {
        target = null;
      }
      if (!target) {
        setError(t("chat.create_failed"));
        setSending(false);
        return;
      }
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
    const ac = new AbortController();
    abortRef.current = ac;
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
              if (!work) setKaraoke(null);
              onEnd?.(reason);
            },
            "",
            // 通常声はライブ中も最終回答確定後もカラオケ表示する。
            // 作業過程の小声再生だけは disclosure 内なのでハイライトしない。
            !work ? (t) => setKaraoke(t) : undefined,
          )
        : null;
    stopTtsForReplacement(ttsRef.current);
    ttsRef.current = workMode === "off" ? makeTts() : null;

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
        if (ttsRef.current === c) ttsRef.current = null;
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
      ttsRef.current = c;
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
      if (ttsRef.current === workCurrent) ttsRef.current = null;
      workCurrent = null;
    };
    let streamDone = false;
    markChatBusy(target.id, true); // publish 進行中 to the rail
    // Mirror the live reply + working steps + final conversation into the store so a pane
    // re-opened on this conversation mid-stream re-attaches and picks up the answer even if
    // THIS pane (and component) is gone by the time the turn finishes.
    const convId = target.id;
    let acc = ""; // current (tentative) answer text
    const steps: ChatStep[] = []; // working steps committed so far this turn
    // Tear the streaming turn down in one batched render: applying the final conversation
    // (which now ends with the assistant reply) and removing the still-streaming bubble must
    // happen together. If the teardown runs only AFTER `await chatStream` resolves, a frame
    // slips in where BOTH show — the completed reply plus the slightly-behind (throttled)
    // streaming copy — which reads as the answer being erased and rewritten (打ち消し→再描画).
    const teardown = () => {
      clearLive(convId);
      markChatBusy(convId, false);
      setStreamText("");
      setStreamSteps([]);
      setKaraoke(null);
      setSending(false);
    };
    await chatStream(
      convId,
      prompt,
      {
        onDelta: (t) => {
          acc += t;
          setStreamText(acc);
          setLive(convId, { text: acc, steps }); // steps only change in onStep
          if (workMode === "off") ttsRef.current?.push(t);
        },
        onStep: (step) => {
          // A tool-using message finished: its narration becomes a working step and the
          // tentative answer resets, so the next message streams as a fresh answer.
          steps.push(step);
          acc = "";
          setStreamText("");
          setStreamSteps([...steps]);
          setKaraoke(null);
          setLive(convId, { text: "", steps: [...steps] });
          if (workMode === "off") {
            stopTtsForReplacement(ttsRef.current);
            ttsRef.current = makeTts(); // 従来動作: 次の tentative message をライブ再生
          } else if (step.text?.trim()) {
            workQueue.push(step.text);
            pumpWork();
          }
        },
        onError: (m) => setError(m),
        onDone: (updated) => {
          streamDone = true;
          if (updated) {
            applyConv(updated);
            publishSnapshot(updated); // reaches any live pane, even after this one unmounts
          }
          teardown(); // same synchronous batch as applyConv → no duplicate-bubble frame
          if (workMode === "off") {
            ttsRef.current?.flush();
          } else {
            // 最終回答の到着で、残っている小声再生を置換して通常声へ戻す。
            closeWork();
            const finalText = acc.trim() || updated?.messages.at(-1)?.content || "";
            const c = finalText ? makeTts() : null;
            ttsRef.current = c;
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
    abortRef.current = null;
    teardown();
    bumpChatList(); // a new/updated thread should surface in the rail list
  };

  // Stop the in-flight turn. The turn is now detached from its SSE request on the backend
  // (so a reload can't kill it), which means aborting the fetch alone no longer cancels the
  // headless process — an explicit stop call does. We still abort the local fetch to stop
  // reading + tear down, and stop the 読み上げ.
  const stop = () => {
    if (conversationId) void chatStop(conversationId); // cancel the detached backend turn
    abortRef.current?.abort();
    ttsRef.current?.stop(); // 読み上げも即停止（in-flight abort・再生停止・キュー破棄）
  };

  // ペインを閉じる/アンマウント時は読み上げを止める（音声が居残らないように）。
  useEffect(() => () => stopTtsForReplacement(ttsRef.current), []);

  // Composer history = the user's own prompts in this conversation, so ↑ recalls them even
  // after a reload (built from conv, not just this mount). The visible words only — the
  // machine-facing pasted-image instruction is stripped. Newest last, consecutive dupes folded.
  const history: string[] = [];
  for (const m of conv?.messages ?? []) {
    if (m.role !== "user") continue;
    const s = splitPastedImages(m.content).text.trim();
    if (s && history[history.length - 1] !== s) history.push(s);
  }

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

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Scroll the message list without leaving the composer: Shift+↑/↓ nudges, Ctrl/⌘+↑/↓
    // and Ctrl/⌘+[ / ] page. Checked before history recall so the modified arrows don't get
    // swallowed by the ↑/↓ recall path below.
    if (!e.nativeEvent.isComposing && scrollComposerViewport(e, scrollRef.current)) return;
    // Shell-style history: ↑/↓ recall past prompts when the field is empty (or once recall
    // is underway). With text present, arrows move the caret as usual.
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.nativeEvent.isComposing) {
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
  const agentKind = conv?.agent || draftAsst?.agent || null;
  const agent = agentKind ? agentOf(agentKind) : null;
  const title = conv?.title || (draftAsst && assistantName(draftAsst)) || t("chat.label");
  const isDraft = !conversationId && !!draftAssistantId;
  // A turn may be in flight because THIS pane is sending, because a background turn on this
  // conversation (started before the pane was closed + re-opened) is still running, or
  // because we reloaded into a detached turn and are polling for its reply (reattaching).
  const showStreaming = sending || storeBusy || reattaching;
  const streamBody = sending ? streamText : (liveTurn?.text ?? "");
  const liveSteps = sending ? streamSteps : (liveTurn?.steps ?? []);
  const empty = (!conv || conv.messages.length === 0) && !loadError && !showStreaming && !loadingConv;
  // Status chip like the Sessions list / MirrorView header: 進行中 while streaming, else 待機中.
  const stateChip = showStreaming
    ? { cls: "working", icon: "loading", spin: true, text: tr("chat.state_running") }
    : { cls: "on", icon: "check", text: tr("chat.state_idle") };

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
      <header className="view-head fileinfo">
        <span className="fi-name">
          <Icon name={draftAsst?.icon || "comment-discussion"} /> {title}
        </span>
        {agent && (
          <span className={"kind-tag kind-" + kindClass(agentKind!)}>
            <Icon name={agent.icon} />
            {agent.assistantName}
          </span>
        )}
        {(conv || showStreaming) && (
          <span className={"session-state " + stateChip.cls}>
            <Icon name={stateChip.icon} spin={stateChip.spin} /> {stateChip.text}
          </span>
        )}
      </header>
      <div className="chat-scroll" ref={scrollRef}>
        {loadError && (
          <div className="chat-error" role="alert">
            {loadError}
          </div>
        )}
        {/* Fetching the conversation (retried while the WS agent boots) — a spinner beats
            flashing the empty hint or a spurious "not found". */}
        {loadingConv && !conv && !loadError && (
          <div className="chat-empty">
            <Icon name="loading" spin /> {tr("common.loading")}
          </div>
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
        {conv?.messages.map((m, i) => {
          // Assistant replies render through AssistantTurn, which owns the bubble ref so
          // its footer can karaoke-read the rendered Markdown (docs/24).
          if (m.role === "assistant") {
            return (
              <div key={i} className="chat-msg role-assistant">
                <AssistantTurn
                  text={m.content}
                  steps={m.steps}
                  ts={m.ts}
                  agentName={agent?.assistantName || tr("chat.assistant_fallback")}
                  voice={{ ...(assistantVoiceOpts(assistId, assistVoice) ?? {}), paneId }}
                  highlight={i === conv.messages.length - 1 ? karaoke : null}
                />
              </div>
            );
          }
          // Split off any pasted-image references so a user bubble shows the user's
          // words + clickable thumbnails, not the machine-facing paths — and so the
          // copy button copies the words, not the image instruction.
          const { text, images } = splitPastedImages(m.content);
          return (
            <div key={i} className="chat-msg role-user">
              <div className="chat-role">{tr("chat.you")}</div>
              <div className="chat-body">
                {/* Both roles render as Markdown; `breaks` keeps plain newlines as
                    line breaks (mirrors MirrorView's user turns). */}
                {text && <ChatMarkdown source={text} breaks />}
                {images.length > 0 && conv && (
                  <div className="chat-imgs">
                    {images.map((nm) => (
                      <ChatPastedThumb key={nm} convId={conv.id} name={nm} />
                    ))}
                  </div>
                )}
              </div>
              {/* Footer under the bubble — time + copy, mirroring MirrorView's turn foot. */}
              <div className="chat-msg-foot">
                {m.ts > 0 && <span className="cm-time">{formatMsgTS(m.ts)}</span>}
                <ChatCopyButton text={text} />
              </div>
            </div>
          );
        })}
        {showStreaming && (
          <div className="chat-msg role-assistant">
            <div className="chat-role">{agent?.assistantName || tr("chat.assistant_fallback")}</div>
            {/* Working steps stream in above the answer, open so progress is visible. */}
            {liveSteps.length > 0 && <ChatSteps steps={liveSteps} defaultOpen live />}
            {streamBody ? (
              <div className="chat-body">
                <StreamingMarkdown text={streamBody} highlight={karaoke} />
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
      </div>
    </div>
  );
}

// StreamingMarkdown renders the live-accumulating reply as Markdown, throttled to one
// re-render per ~120ms — per-delta would re-parse and innerHTML-swap the whole bubble on
// every SSE chunk (killing text selection, wasting CPU). Trailing updates are always
// flushed, so the shown text never lags more than one window behind the stream.
const STREAM_RENDER_MS = 120;
function StreamingMarkdown({ text, highlight }: { text: string; highlight?: string | null }) {
  const [shown, setShown] = useState(text);
  const lastRef = useRef(0); // when we last flushed
  const timerRef = useRef<number | null>(null);
  const textRef = useRef(text); // latest text, for the trailing flush
  textRef.current = text;
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const litRef = useRef<HTMLElement | null>(null);
  const litNeedleRef = useRef(""); // last highlighted sentence, so we scroll only when it changes
  useEffect(() => {
    const due = lastRef.current + STREAM_RENDER_MS;
    const now = Date.now();
    if (now >= due) {
      lastRef.current = now;
      setShown(text);
    } else if (timerRef.current == null) {
      timerRef.current = window.setTimeout(() => {
        timerRef.current = null;
        lastRef.current = Date.now();
        setShown(textRef.current);
      }, due - now);
    }
  }, [text]);
  useEffect(
    () => () => {
      if (timerRef.current != null) clearTimeout(timerRef.current);
    },
    [],
  );
  // Live karaoke (docs/19): each ~120ms re-render rebuilds the bubble DOM and wipes any
  // highlight, so we (re)apply .tts-active after every render, driven by `highlight` (the
  // sentence the TTS just started). We locate the sentence's block by matching its
  // (whitespace-stripped) text against the rendered blocks — the same block set the
  // completed-turn karaoke walks (collectBlocks). Not found → keep the current highlight
  // (avoids flicker at sentence boundaries); no highlight → clear.
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    if (!highlight) {
      litRef.current?.classList.remove("tts-active");
      litRef.current = null;
      litNeedleRef.current = "";
      return;
    }
    const norm = (s: string) => s.replace(/\s+/g, "");
    const needle = norm(highlight).slice(0, 16);
    if (!needle) return;
    const target = collectBlocks(wrap).find((b) => norm(b.textContent || "").includes(needle));
    if (!target) return; // not found → keep the current highlight (no flicker at boundaries)
    // Re-apply the class every render (the DOM was rebuilt), but only scroll when the spoken
    // sentence actually changed — otherwise the ~120ms re-renders would spam smooth-scroll.
    if (litRef.current && litRef.current !== target) litRef.current.classList.remove("tts-active");
    target.classList.add("tts-active");
    litRef.current = target;
    if (litNeedleRef.current !== needle) {
      litNeedleRef.current = needle;
      target.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }, [shown, highlight]);
  useEffect(() => () => litRef.current?.classList.remove("tts-active"), []);
  return (
    <div ref={wrapRef}>
      <ChatMarkdown source={shown} breaks streaming />
    </div>
  );
}

function ChatMarkdown({ source, breaks, streaming }: { source: string; breaks?: boolean; streaming?: boolean }) {
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  return (
    <MarkdownView
      source={source}
      breaks={breaks}
      streaming={streaming}
      onOpenFile={(path, line, column) =>
        openTargetInNew({ content: { kind: "file", filePath: path, targetLine: line, targetColumn: column } }, true)
      }
    />
  );
}

// ChatSteps renders an assistant turn's 作業過程 (docs/19 分離): the narration the model
// emitted before each tool call, kept separate from — but alongside — the final answer.
// Collapsible; open while streaming so progress is visible, collapsed once the turn is done.
function ChatSteps({ steps, defaultOpen, live }: { steps: ChatStep[]; defaultOpen?: boolean; live?: boolean }) {
  const tr = useT();
  const [open, setOpen] = useState(!!defaultOpen);
  if (!steps.length) return null;
  const toolCount = steps.reduce((n, step) => n + (step.tools?.length ?? 0), 0);
  return (
    <details
      className={"mt-work chat-steps" + (live ? " live" : "")}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary className="mt-work-head">
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        {live && <Icon name="loading" spin />}
        <span className="mt-work-title">{tr("chat.work_process")}</span>
        <span className="mt-work-count muted">
          {tCount("chat.tool_count", toolCount)}
          {steps.length > 0 ? tCount("chat.interim_count", steps.length) : ""}
        </span>
      </summary>
      <div className="mt-work-body">
        {foldStepParts(steps).map((it, i) =>
          it.kind === "text" ? (
            <div key={i} className="chat-step">
              <ChatMarkdown source={it.text} breaks />
            </div>
          ) : (
            <ChatToolRun key={i} tools={it.tools} />
          ),
        )}
      </div>
    </details>
  );
}

// Flatten a turn's 作業過程 into an ordered list of parts (narration text / tool name), then
// coalesce each maximal run of CONSECUTIVE tool calls into one folded run — matching
// MirrorView's foldParts/ToolRun. Narration between tools breaks a run (so a lone tool stays
// on its own; back-to-back tool-only steps fold together).
type StepItem = { kind: "text"; text: string } | { kind: "toolrun"; tools: string[] };
function foldStepParts(steps: ChatStep[]): StepItem[] {
  const items: StepItem[] = [];
  const pushTool = (name: string) => {
    const last = items[items.length - 1];
    if (last && last.kind === "toolrun") last.tools.push(name);
    else items.push({ kind: "toolrun", tools: [name] });
  };
  for (const s of steps) {
    const text = s.text?.trim();
    if (text) items.push({ kind: "text", text });
    for (const tool of s.tools ?? []) pushTool(tool);
  }
  return items;
}

// ChatToolRun renders a run of consecutive tool/mcp calls in the 作業過程. A lone call shows
// as a plain chip (as the mirror does for output-less traces); two or more fold into one
// collapsed "N 件のツール · tally" summary that expands to the individual calls. Mirrors
// MirrorView's ToolRun, reusing its .mt-toolrun / .mt-tool styling.
function ChatToolRun({ tools }: { tools: string[] }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  if (tools.length === 1) {
    return (
      <div className="mt-tool">
        <Icon name="tools" />
        <span className="mt-tool-name">{tools[0]}</span>
      </div>
    );
  }
  // Tally repeated names (Read×3 · Grep) so a long run reads at a glance.
  const tally: [string, number][] = [];
  const at: Record<string, number> = {};
  for (const name of tools) {
    if (at[name] === undefined) {
      at[name] = tally.length;
      tally.push([name, 0]);
    }
    tally[at[name]][1]++;
  }
  const summary = tally.map(([n, c]) => (c > 1 ? `${n}×${c}` : n)).join(" · ");
  return (
    <div className={"mt-toolrun" + (open ? " open" : "")}>
      <button
        type="button"
        className="mt-tool mt-toolrun-head"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        title={open ? tr("mirror.collapse_tools") : tr("mirror.expand_tools")}
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        <span className="mt-tool-name">{tCount("mirror.tools_count", tools.length)}</span>
        <span className="mt-tool-info">{summary}</span>
      </button>
      {open && (
        <div className="mt-toolrun-body">
          {tools.map((name, i) => (
            <div key={i} className="mt-tool">
              <Icon name="tools" />
              <span className="mt-tool-name">{name}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// AssistantTurn renders one completed assistant reply and its footer. It owns a ref to the
// bubble body so the footer's read control can karaoke-read the RENDERED Markdown (docs/24):
// readTurn (features/mirror/turnTts) walks the .markdown DOM into blocks, speaks it sentence
// by sentence, and highlights the block whose sentence is playing (.tts-active) with scroll
// follow — the same engine the mirror/ReaderView use. Live streaming stays plain (the bubble
// re-renders every ~120ms, which would wipe any DOM highlight); karaoke is offered only once
// the turn is complete, i.e. here.
function AssistantTurn({
  text,
  steps,
  ts,
  agentName,
  voice,
  highlight,
}: {
  text: string;
  steps?: ChatStep[];
  ts: number;
  agentName: string;
  voice?: Partial<TtsOptions>;
  highlight?: string | null;
}) {
  const ttsEnabled = useSettings().ttsEnabled;
  const tr = useT();
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const handleRef = useRef<TurnReadHandle | null>(null);
  const [state, setState] = useState<"idle" | "playing" | "paused">("idle");
  // Floating "ここから読み上げ" pill anchored to a mouse selection inside the bubble.
  const [selPill, setSelPill] = useState<{ x: number; y: number; block: number } | null>(null);
  const autoLitRef = useRef<HTMLElement | null>(null);

  // 自動読み上げは最終回答の確定後にこの完成済み DOM へ移るため、
  // startTts から通知された文を本文ブロックへ対応付けて光らせる。
  useEffect(() => {
    const body = bodyRef.current;
    if (!body || !highlight) {
      autoLitRef.current?.classList.remove("tts-active");
      autoLitRef.current = null;
      return;
    }
    const norm = (s: string) => s.replace(/\s+/g, "");
    const needle = norm(highlight).slice(0, 16);
    if (!needle) return;
    const target = collectBlocks(body).find((b) => norm(b.textContent || "").includes(needle));
    if (!target) return;
    if (autoLitRef.current && autoLitRef.current !== target) autoLitRef.current.classList.remove("tts-active");
    target.classList.add("tts-active");
    autoLitRef.current = target;
    target.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [highlight]);
  useEffect(() => () => autoLitRef.current?.classList.remove("tts-active"), []);

  // Stop this bubble's reading if the pane/component goes away mid-read.
  useEffect(() => () => handleRef.current?.stop("replaced"), []);

  const start = (fromBlock: number) => {
    const body = bodyRef.current;
    if (!body) return;
    handleRef.current?.stop("replaced");
    // onEnd fires once on natural end AND on preemption (TopBar stop / another playback),
    // so the footer always falls back to the idle "読み上げ" state.
    const h = readTurn(body, t("chat.label"), fromBlock, () => {
      handleRef.current = null;
      setState("idle");
    }, voice);
    if (h) {
      handleRef.current = h;
      setState("playing");
    }
  };
  const pause = () => {
    handleRef.current?.pause();
    setState("paused");
  };
  const resume = () => {
    handleRef.current?.resume();
    setState("playing");
  };
  const stop = () => {
    handleRef.current?.stop();
    handleRef.current = null;
    setState("idle");
  };

  // After a mouse selection inside the bubble, surface a "ここから読み上げ" pill at the
  // selection head — reading (re)starts from the block the selection begins in. Desktop
  // mouse only (touch selection emits no mouseup); the footer button still reads from top.
  const onMouseUp = () => {
    const body = bodyRef.current;
    const sel = window.getSelection();
    if (!ttsEnabled || !body || !sel || sel.isCollapsed || sel.rangeCount === 0) {
      setSelPill(null);
      return;
    }
    const range = sel.getRangeAt(0);
    if (!body.contains(range.startContainer)) {
      setSelPill(null);
      return;
    }
    const idx = blockIndexAt(collectBlocks(body), range.startContainer);
    if (idx < 0) {
      setSelPill(null);
      return;
    }
    const rect = range.getBoundingClientRect();
    setSelPill({ x: Math.round(rect.left), y: Math.round(rect.top - 34), block: idx });
  };
  const startFromSelection = () => {
    if (!selPill) return;
    start(selPill.block);
    setSelPill(null);
    window.getSelection()?.removeAllRanges();
  };

  return (
    <>
      <div className="chat-role">{agentName}</div>
      {/* 作業過程（ツール応答）は最終回答の上に折りたたんで表示（既定は畳む・保持）。 */}
      {steps && steps.length > 0 && <ChatSteps steps={steps} />}
      <div className="chat-body" ref={bodyRef} onMouseUp={onMouseUp}>
        {text && <ChatMarkdown source={text} breaks />}
      </div>
      {selPill && (
        <div className="sel-pill-group" style={{ left: selPill.x, top: Math.max(4, selPill.y) }}>
          <button
            type="button"
            className="sel-send-pill"
            onMouseDown={(e) => e.preventDefault()}
            onClick={startFromSelection}
          >
            <Icon name="unmute" /> {tr("chat.read_from_here")}
          </button>
        </div>
      )}
      <div className="chat-msg-foot">
        {ts > 0 && <span className="cm-time">{formatMsgTS(ts)}</span>}
        {ttsEnabled && text.trim() && (
          state === "idle" ? (
            <button type="button" className="ghost cm-copy" title={tr("chat.read_title")} onClick={() => start(0)}>
              <Icon name="unmute" /> {tr("chat.read")}
            </button>
          ) : (
            <>
              {state === "playing" ? (
                <button type="button" className="ghost cm-copy" title={tr("chat.pause")} onClick={pause}>
                  <Icon name="debug-pause" /> {tr("chat.pause")}
                </button>
              ) : (
                <button type="button" className="ghost cm-copy" title={tr("chat.resume")} onClick={resume}>
                  <Icon name="play" /> {tr("chat.resume")}
                </button>
              )}
              <button type="button" className="ghost cm-copy" title={tr("chat.stop")} onClick={stop}>
                <Icon name="debug-stop" /> {tr("chat.stop")}
              </button>
            </>
          )
        )}
        <ChatCopyButton text={text} />
      </div>
    </>
  );
}

// formatMsgTS renders a unix-millis timestamp as local "MM/DD HH:MM" — same shape as
// MirrorView's turn footer (date kept so a thread that spans days stays unambiguous).
const formatMsgTS = (ms: number) => fmtDateTime(ms);

// ChatCopyButton copies the reply's RAW Markdown (not the rendered HTML) to the
// clipboard — same behavior as MirrorView's CopyButton.
function ChatCopyButton({ text }: { text: string }) {
  const tr = useT();
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    } catch {
      /* clipboard blocked (insecure context / permission) — no-op */
    }
  };
  return (
    <button type="button" className="ghost cm-copy" title={tr("chat.copy_md_title")} onClick={copy}>
      <Icon name={done ? "check" : "copy"} /> {done ? tr("chat.copied") : tr("chat.copy")}
    </button>
  );
}

// ChatPastedThumb previews a pasted image referenced in a chat turn. It fetches the bytes
// through the authenticated API wrapper (an <img src> can't carry the tenant header) into
// an object URL; clicking opens the full image in a new tab.
function ChatPastedThumb({ convId, name }: { convId: string; name: string }) {
  const tr = useT();
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let alive = true;
    let obj = "";
    raw(`api/chat/conversations/${encodeURIComponent(convId)}/pasted/${encodeURIComponent(name)}`)
      .then((r) => (r.ok ? r.blob() : null))
      .then((b) => {
        if (!alive) return;
        if (!b) {
          setFailed(true);
          return;
        }
        obj = URL.createObjectURL(b);
        setUrl(obj);
      })
      .catch(() => {
        if (alive) setFailed(true);
      });
    return () => {
      alive = false;
      if (obj) URL.revokeObjectURL(obj);
    };
  }, [convId, name]);
  if (failed) {
    return (
      <span className="chat-img chat-img-loading" title={tr("chat.preview_failed")}>
        <Icon name="file-media" />
      </span>
    );
  }
  if (!url) {
    return (
      <span className="chat-img chat-img-loading">
        <Icon name="loading" spin />
      </span>
    );
  }
  return (
    <button type="button" className="chat-img" title={tr("chat.click_to_zoom")} onClick={() => window.open(url, "_blank", "noopener")}>
      <img src={url} alt={tr("chat.pasted_image_alt")} />
    </button>
  );
}
