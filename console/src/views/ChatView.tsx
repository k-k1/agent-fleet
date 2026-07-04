import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import MarkdownView from "./MarkdownView.jsx";
import Icon from "../components/Icon.jsx";
import { chatGet, chatSend, errText } from "../api.js";
import { agentOf } from "../agents/registry.ts";
import { kindClass } from "../lib/sessionkind.js";
import type { Conversation, ChatMessage } from "../types/chat.ts";

// ChatView renders one assistant-chat conversation (docs/19) — a headless-CLI LLM
// chat/translation thread. Unlike the terminal panes it never mounts xterm; it's a
// plain message list + composer over the /api/chat/* endpoints. The conversation's
// `agent` (claude/…) only selects the backend + the header badge; the view is shared.
interface ChatViewProps {
  conversationId: string | null;
}

export default function ChatView({ conversationId }: ChatViewProps) {
  const [conv, setConv] = useState<Conversation | null>(null);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const scrollRef = useRef<HTMLDivElement | null>(null);

  // Load (and reload when the pane switches to another conversation).
  useEffect(() => {
    let cancelled = false;
    setConv(null);
    setError("");
    setLoadError("");
    if (!conversationId) return;
    chatGet(conversationId)
      .then((c) => {
        if (cancelled) return;
        if (c && c.id) setConv(c);
        else setLoadError("会話が見つかりません");
      })
      .catch(() => {
        if (!cancelled) setLoadError("会話の読み込みに失敗しました");
      });
    return () => {
      cancelled = true;
    };
  }, [conversationId]);

  // Keep the transcript pinned to the newest turn (and to the thinking indicator).
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [conv?.messages.length, sending]);

  const send = async () => {
    const text = input.trim();
    if (!text || sending || !conv) return;
    setError("");
    setSending(true);
    // Optimistically show the user's turn; the server echoes the full conversation.
    const userMsg: ChatMessage = { role: "user", content: text, ts: Date.now() };
    setConv((c) => (c ? { ...c, messages: [...c.messages, userMsg] } : c));
    setInput("");
    try {
      const res = await chatSend(conv.id, text);
      if (res.conversation) setConv(res.conversation);
      else setError(errText(res.error) || "応答の取得に失敗しました");
    } catch {
      setError("送信に失敗しました");
    } finally {
      setSending(false);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  const agent = conv ? agentOf(conv.agent) : null;
  return (
    <div className="chatview">
      <header className="view-head fileinfo">
        <span className="fi-name">
          <Icon name="comment-discussion" /> {conv?.title || "チャット"}
        </span>
        {conv && (
          <span className={"kind-tag kind-" + kindClass(conv.agent)}>{agent?.assistantName}</span>
        )}
      </header>
      <div className="chat-scroll" ref={scrollRef}>
        {loadError && (
          <div className="chat-error" role="alert">
            {loadError}
          </div>
        )}
        {conv && conv.messages.length === 0 && !loadError && (
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
              <span className="chat-thinking">
                <Icon name="loading" spin /> 考え中…
              </span>
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
          className="chat-input"
          value={input}
          placeholder={
            conv ? "メッセージを入力（Enter で送信 / Shift+Enter で改行）" : "読み込み中…"
          }
          disabled={!conv || sending}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          rows={2}
        />
        <button
          type="button"
          className="btn chat-send"
          disabled={!conv || sending || !input.trim()}
          onClick={() => void send()}
          title="送信"
        >
          <Icon name="send" />
        </button>
      </div>
    </div>
  );
}
