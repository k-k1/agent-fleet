import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import MarkdownView from "./MarkdownView.jsx";
import Icon from "../components/Icon.jsx";
import { useApp } from "../state.jsx";
import { chatGet, chatStream, chatCreate, assistantGet } from "../api.js";
import { takeChatSeed } from "../lib/chatSeed.js";
import { coarsePointer } from "../lib/device.js";
import { agentOf } from "../agents/registry.ts";
import { kindClass } from "../lib/sessionkind.js";
import type { Conversation, ChatMessage } from "../types/chat.ts";
import type { Assistant } from "../types/assistant.ts";

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

export default function ChatView({ conversationId, draftAssistantId, paneId, active }: ChatViewProps) {
  const { promoteDraft, bumpChatList } = useApp();
  const [conv, setConv] = useState<Conversation | null>(null);
  const [draftAsst, setDraftAsst] = useState<Assistant | null>(null); // greeting source in draft mode
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const [streamText, setStreamText] = useState(""); // live-accumulating assistant reply
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);
  const convRef = useRef<Conversation | null>(null); // mirror of conv, to guard reloads
  const applyConv = (c: Conversation | null) => {
    convRef.current = c;
    setConv(c);
  };

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

  // Keep the transcript pinned to the newest turn (and to the thinking indicator).
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [conv?.messages.length, sending, streamText]);

  // Focus the composer when this pane becomes the active chat (opening a conversation or
  // an assistant draft) — but NOT on touch devices, where auto-focus would pop the
  // on-screen keyboard just from opening the chat to read it (mirrors MirrorView).
  useEffect(() => {
    if (active && !coarsePointer() && (conversationId || draftAssistantId)) inputRef.current?.focus();
  }, [active, conversationId, draftAssistantId]);

  const send = async () => {
    const text = input.trim();
    if (!text || sending) return;
    setError("");
    setSending(true);
    setStreamText("");

    // Draft: create the conversation on the first message, then promote this pane to it.
    let target = conv;
    if (!target) {
      if (!draftAssistantId) {
        setSending(false);
        return;
      }
      try {
        const created = await chatCreate(draftAssistantId, text.slice(0, 40));
        if (!created || !created.id) {
          setError("会話の作成に失敗しました");
          setSending(false);
          return;
        }
        target = created;
        applyConv(created);
        promoteDraft(paneId, created.id);
        setDraftAsst(null);
      } catch {
        setError("会話の作成に失敗しました");
        setSending(false);
        return;
      }
    }

    // Optimistically show the user's turn; the server echoes the full conversation on done.
    const userMsg: ChatMessage = { role: "user", content: text, ts: Date.now() };
    setConv((c) => (c ? { ...c, messages: [...c.messages, userMsg] } : c));
    setInput("");
    await chatStream(target.id, text, {
      onDelta: (t) => setStreamText((s) => s + t),
      onError: (m) => setError(m),
      onDone: (updated) => {
        if (updated) applyConv(updated);
      },
    });
    setStreamText("");
    setSending(false);
    bumpChatList(); // a new/updated thread should surface in the rail list
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  // Header/badge come from the live conversation, or the draft assistant while composing.
  const agentKind = conv?.agent || draftAsst?.agent || null;
  const agent = agentKind ? agentOf(agentKind) : null;
  const title = conv?.title || draftAsst?.name || "チャット";
  const isDraft = !conversationId && !!draftAssistantId;
  const empty = (!conv || conv.messages.length === 0) && !loadError;

  return (
    <div className="chatview">
      <header className="view-head fileinfo">
        <span className="fi-name">
          <Icon name={draftAsst?.icon || "comment-discussion"} /> {title}
        </span>
        {agent && <span className={"kind-tag kind-" + kindClass(agentKind!)}>{agent.assistantName}</span>}
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
        {conv?.messages.map((m, i) => (
          <div key={i} className={"chat-msg role-" + m.role}>
            <div className="chat-role">
              {m.role === "user" ? "あなた" : agent?.assistantName || "アシスタント"}
            </div>
            <div className="chat-body">
              {m.role === "assistant" ? (
                <MarkdownView source={m.content} breaks />
              ) : (
                <div className="chat-text">{m.content}</div>
              )}
            </div>
          </div>
        ))}
        {sending && (
          <div className="chat-msg role-assistant">
            <div className="chat-role">{agent?.assistantName || "アシスタント"}</div>
            <div className="chat-body">
              {streamText ? (
                <div className="chat-text chat-streaming">{streamText}</div>
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
        <textarea
          ref={inputRef}
          className="chat-input"
          value={input}
          placeholder={
            conv || isDraft ? "メッセージを入力（Enter で送信 / Shift+Enter で改行）" : "読み込み中…"
          }
          disabled={(!conv && !isDraft) || sending}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          rows={2}
        />
        <button
          type="button"
          className="btn chat-send"
          disabled={(!conv && !isDraft) || sending || !input.trim()}
          onClick={() => void send()}
          title="送信"
        >
          <Icon name="send" />
        </button>
      </div>
    </div>
  );
}
