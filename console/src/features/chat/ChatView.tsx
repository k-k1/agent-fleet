import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, ClipboardEvent } from "react";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { useChatStore } from "./store.ts";
import { chatGet, chatStream, chatCreate, assistantGet, chatPasteImage } from "./api.ts";
import { errText, raw } from "../../core/api/client.ts";
import { takeChatSeed } from "../../lib/chatSeed.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useSettings } from "../../lib/settings.ts";
import { startTts, ttsOptsFromSettings, assistantVoiceOpts, type TtsController } from "./tts.ts";
import { TtsReadButton } from "./TtsReadButton.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { splitPastedImages, buildImagePrompt } from "../../lib/pastedImages.ts";
import { agentOf } from "../../agents/registry.ts";
import { kindClass } from "../../lib/sessionkind.ts";
import type { Conversation, ChatMessage } from "../../types/chat.ts";
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
  const liveText = useChatStore((s) => (conversationId ? s.live[conversationId] : undefined));
  const snapshot = useChatStore((s) => (conversationId ? s.snapshots[conversationId] : undefined));
  const settings = useSettings();
  const toast = useToast();
  // Send key follows the user's global preference (shared with the Markdown mirror
  // composer): "mod-enter" = Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe);
  // "enter" = Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const [conv, setConv] = useState<Conversation | null>(null);
  const [draftAsst, setDraftAsst] = useState<Assistant | null>(null); // greeting source in draft mode
  const [input, setInput] = useState("");
  const [attachments, setAttachments] = useState<{ path: string; name: string; url: string }[]>([]);
  const [pasting, setPasting] = useState(false); // an image upload is in flight
  const [sending, setSending] = useState(false);
  const [streamText, setStreamText] = useState(""); // live-accumulating assistant reply
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
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
    setError("");
    setLoadError("");
    if (conversationId) {
      if (convRef.current?.id === conversationId) return; // already have it (e.g. just promoted)
      applyConv(null);
      setDraftAsst(null);
      setInput(takeChatSeed(conversationId) ?? ""); // one-shot composer prefill (Phase C)
      chatGet(conversationId)
        .then((c) => {
          if (cancelled) return;
          if (c && c.id) applyConv(c);
          else setLoadError("会話が見つかりません");
        })
        .catch(() => {
          if (!cancelled) setLoadError("会話の読み込みに失敗しました");
        });
    } else if (draftAssistantId) {
      applyConv(null);
      setInput("");
      setDraftAsst(null);
      assistantGet(draftAssistantId)
        .then((a) => {
          if (!cancelled && a && a.id) setDraftAsst(a);
        })
        .catch(() => {});
    } else {
      applyConv(null);
      setDraftAsst(null);
    }
    return () => {
      cancelled = true;
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

  // Keep the transcript pinned to the newest turn (and to the thinking indicator).
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [conv?.messages.length, sending, streamText, liveText]);

  // Focus the composer when this pane becomes the active chat (opening a conversation or
  // an assistant draft) — but NOT on touch devices, where auto-focus would pop the
  // on-screen keyboard just from opening the chat to read it (mirrors MirrorView).
  useEffect(() => {
    if (active && !coarsePointer() && (conversationId || draftAssistantId)) inputRef.current?.focus();
  }, [active, conversationId, draftAssistantId]);

  // Image attach rides claude's Read-tool flow (saved path referenced in the prompt),
  // so it's offered only for claude-agent chats (codex has no image-read path here).
  const canAttach = (conv?.agent || draftAsst?.agent) === "claude";

  // Ensure a real conversation exists (approach A): a draft is created + promoted before
  // the first upload or send, so image attachments have a conversation id to post to.
  const ensureConv = async (): Promise<Conversation | null> => {
    if (convRef.current) return convRef.current;
    if (!draftAssistantId) return null;
    const title = input.trim().slice(0, 40) || "新しいチャット";
    const created = await chatCreate(draftAssistantId, title);
    if (!created || !created.id) return null;
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
      toast("会話の作成に失敗しました");
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
          toast(res.error ? errText(res.error) : "画像の貼り付けに失敗しました");
        }
      } catch {
        toast("画像の貼り付けに失敗しました（通信エラー）");
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
        setError("会話の作成に失敗しました");
        setSending(false);
        return;
      }
    }

    // Append the machine-facing image instruction + paths for claude's Read tool. The
    // bubble strips it back out (splitPastedImages) and shows thumbnails instead.
    const prompt = buildImagePrompt(text, paths);
    // Optimistically show the user's turn (full prompt so pasted-image thumbnails render
    // immediately); the server echoes the full conversation on done.
    const userMsg: ChatMessage = { role: "user", content: prompt, ts: Date.now() };
    setConv((c) => (c ? { ...c, messages: [...c.messages, userMsg] } : c));
    setInput("");
    clearAttachments();
    // On touch devices, drop focus so the soft keyboard (GBoard) retracts once the
    // turn is sent — the reply is what the user wants to read, not keep typing.
    if (coarsePointer()) inputRef.current?.blur();
    const ac = new AbortController();
    abortRef.current = ac;
    // 音声読み上げ（docs/24）: 有効時のみコントローラを起こし、delta を句点区切りで逐次合成。
    // 直前のターンが残っていれば止めてから。
    ttsRef.current?.stop();
    ttsRef.current = settings.ttsEnabled
      ? startTts(
          {
            ...ttsOptsFromSettings(settings),
            // アシスタントの声: 明示指定 > プール割り当て（セッションごとに声 ON 時）> 設定の話者
            ...assistantVoiceOpts(target.assistant_id || draftAssistantId || undefined, assistVoice),
          },
          "チャット",
        )
      : null;
    markChatBusy(target.id, true); // publish 進行中 to the rail
    // Mirror the live reply + final conversation into the store so a pane re-opened on this
    // conversation mid-stream re-attaches and picks up the answer even if THIS pane (and
    // component) is gone by the time the turn finishes.
    const convId = target.id;
    let acc = "";
    await chatStream(
      convId,
      prompt,
      {
        onDelta: (t) => {
          acc += t;
          setStreamText(acc);
          setLive(convId, acc);
          ttsRef.current?.push(t);
        },
        onError: (m) => setError(m),
        onDone: (updated) => {
          if (updated) {
            applyConv(updated);
            publishSnapshot(updated); // reaches any live pane, even after this one unmounts
          }
          ttsRef.current?.flush();
        },
      },
      ac.signal,
    );
    clearLive(convId);
    abortRef.current = null;
    markChatBusy(convId, false);
    setStreamText("");
    setSending(false);
    bumpChatList(); // a new/updated thread should surface in the rail list
  };

  // Stop the in-flight turn: aborting the fetch cancels the request context up the chain
  // (CP → Agent), which kills the headless `claude` process (docs/19).
  const stop = () => {
    abortRef.current?.abort();
    ttsRef.current?.stop(); // 読み上げも即停止（in-flight abort・再生停止・キュー破棄）
  };

  // ペインを閉じる/アンマウント時は読み上げを止める（音声が居残らないように）。
  useEffect(() => () => ttsRef.current?.stop(), []);

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
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
  const title = conv?.title || draftAsst?.name || "チャット";
  const isDraft = !conversationId && !!draftAssistantId;
  // A turn may be in flight because THIS pane is sending, or because a background turn on
  // this conversation (started before the pane was closed + re-opened) is still running.
  const showStreaming = sending || storeBusy;
  const streamBody = sending ? streamText : (liveText ?? "");
  const empty = (!conv || conv.messages.length === 0) && !loadError && !showStreaming;
  // Status chip like the Sessions list / MirrorView header: 進行中 while streaming, else 待機中.
  const stateChip = showStreaming
    ? { cls: "working", icon: "loading", spin: true, text: "進行中" }
    : { cls: "on", icon: "check", text: "待機中" };

  return (
    <div className="chatview">
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
        {/* Greeting: the assistant introduces itself while the chat hasn't started. */}
        {empty && draftAsst && (
          <div className="chat-greeting">
            <div className="chat-greeting-head">
              <Icon name={draftAsst.icon || "comment-discussion"} className="chat-greeting-ic" />
              <span className="chat-greeting-name">{draftAsst.name}</span>
            </div>
            <div className="chat-greeting-body">
              {draftAsst.description ? (
                <MarkdownView source={draftAsst.description} breaks />
              ) : (
                "メッセージを送って会話を始めましょう。"
              )}
            </div>
          </div>
        )}
        {empty && !draftAsst && !isDraft && (
          <div className="chat-empty">
            メッセージを送って会話を始めましょう。Markdown 文書の翻訳や要約、質問への回答などを依頼できます。
          </div>
        )}
        {conv?.messages.map((m, i) => {
          // Split off any pasted-image references so a user bubble shows the user's
          // words + clickable thumbnails, not the machine-facing paths — and so the
          // copy button copies the words, not the image instruction.
          const { text, images } =
            m.role === "user" ? splitPastedImages(m.content) : { text: m.content, images: [] as string[] };
          return (
            <div key={i} className={"chat-msg role-" + m.role}>
              <div className="chat-role">
                {m.role === "user" ? "あなた" : agent?.assistantName || "アシスタント"}
              </div>
              <div className="chat-body">
                {/* Both roles render as Markdown; `breaks` keeps plain newlines as
                    line breaks (mirrors MirrorView's user turns). */}
                {text && <MarkdownView source={text} breaks />}
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
                {m.role === "assistant" && <TtsReadButton text={text} voice={assistantVoiceOpts(assistId, assistVoice)} />}
                <ChatCopyButton text={text} />
              </div>
            </div>
          );
        })}
        {showStreaming && (
          <div className="chat-msg role-assistant">
            <div className="chat-role">{agent?.assistantName || "アシスタント"}</div>
            <div className="chat-body">
              {streamBody ? (
                <StreamingMarkdown text={streamBody} />
              ) : (
                <span className="chat-thinking">
                  <Icon name="loading" spin /> 考え中…
                </span>
              )}
            </div>
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
                <button type="button" className="ca-del" title="削除" onClick={() => removeAttachment(i)}>
                  <Icon name="close" />
                </button>
              </div>
            ))}
            {pasting && (
              <span className="ca-loading">
                <Icon name="loading" spin /> アップロード中…
              </span>
            )}
          </div>
        )}
        <div className="chat-composer-row">
          <textarea
            ref={inputRef}
            className="chat-input"
            value={input}
            placeholder={
              conv || isDraft
                ? canAttach
                  ? modSend
                    ? "メッセージを入力（Ctrl+Enter で送信 / Enter で改行 / 画像は貼り付け）"
                    : "メッセージを入力（Enter で送信 / Shift+Enter で改行 / 画像は貼り付け）"
                  : modSend
                    ? "メッセージを入力（Ctrl+Enter で送信 / Enter で改行）"
                    : "メッセージを入力（Enter で送信 / Shift+Enter で改行）"
                : "読み込み中…"
            }
            disabled={(!conv && !isDraft) || showStreaming}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
            rows={2}
          />
          {sending ? (
            <button type="button" className="btn chat-send chat-stop" onClick={stop} title="停止">
              <Icon name="debug-stop" />
            </button>
          ) : (
            <button
              type="button"
              className="btn chat-send"
              disabled={(!conv && !isDraft) || showStreaming || (!input.trim() && !attachments.length)}
              onClick={() => void send()}
              title="送信"
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
function StreamingMarkdown({ text }: { text: string }) {
  const [shown, setShown] = useState(text);
  const lastRef = useRef(0); // when we last flushed
  const timerRef = useRef<number | null>(null);
  const textRef = useRef(text); // latest text, for the trailing flush
  textRef.current = text;
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
  return <MarkdownView source={shown} breaks streaming />;
}

// formatMsgTS renders a unix-millis timestamp as local "MM/DD HH:MM" — same shape as
// MirrorView's turn footer (date kept so a thread that spans days stays unambiguous).
function formatMsgTS(ms: number) {
  const d = new Date(ms);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// ChatCopyButton copies the reply's RAW Markdown (not the rendered HTML) to the
// clipboard — same behavior as MirrorView's CopyButton.
function ChatCopyButton({ text }: { text: string }) {
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
    <button type="button" className="ghost cm-copy" title="Markdown をコピー" onClick={copy}>
      <Icon name={done ? "check" : "copy"} /> {done ? "コピー済" : "コピー"}
    </button>
  );
}

// ChatPastedThumb previews a pasted image referenced in a chat turn. It fetches the bytes
// through the authenticated API wrapper (an <img src> can't carry the tenant header) into
// an object URL; clicking opens the full image in a new tab.
function ChatPastedThumb({ convId, name }: { convId: string; name: string }) {
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
      <span className="chat-img chat-img-loading" title="プレビューを取得できませんでした">
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
    <button type="button" className="chat-img" title="クリックで拡大" onClick={() => window.open(url, "_blank", "noopener")}>
      <img src={url} alt="貼り付け画像" />
    </button>
  );
}
