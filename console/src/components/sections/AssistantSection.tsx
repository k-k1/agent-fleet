import { useCallback, useEffect, useState } from "react";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import { useApp } from "../../state.jsx";
import { useToast } from "../ToastProvider.jsx";
import { chatList, chatCreate, chatDelete } from "../../api.js";
import { AGENTS } from "../../agents/registry.ts";
import { SESSION_KINDS } from "../../types/session.ts";
import type { ConversationMeta } from "../../types/chat.ts";
import type { SessionKind } from "../../types/session.ts";

// The agent kinds that can back an assistant chat (docs/19). Data-driven off the
// registry cap, so Phase A.2 lights up codex by flipping headlessChat — no edit here.
const chatKinds: SessionKind[] = SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat);

// AssistantSection is the left-rail home for headless-CLI chat conversations: a list
// of threads plus a per-kind "new chat" action. Selecting a thread opens it in the
// active pane (a chat-kind pane rendering ChatView).
export default function AssistantSection() {
  const { openChat } = useApp();
  const toast = useToast();
  const [convs, setConvs] = useState<ConversationMeta[]>([]);

  const refresh = useCallback(() => {
    chatList()
      .then((r) => setConvs(r.conversations || []))
      .catch(() => {});
  }, []);
  useEffect(() => {
    refresh();
  }, [refresh]);

  const create = async (agent: SessionKind) => {
    try {
      const c = await chatCreate(agent);
      if (c && c.id) {
        refresh();
        openChat(c.id);
      } else toast("チャットの作成に失敗しました");
    } catch {
      toast("チャットの作成に失敗しました");
    }
  };

  const remove = async (id: string) => {
    try {
      await chatDelete(id);
      refresh();
    } catch {
      toast("削除に失敗しました");
    }
  };

  // One "+"" per chat-capable agent (labeled by its assistant name). With a single
  // kind it reads as one add button; more kinds surface side by side.
  const actions = chatKinds.map((k) => (
    <button
      key={k}
      type="button"
      className="ghost pane-btn"
      title={AGENTS[k].assistantName + " で新規チャット"}
      onClick={() => void create(k)}
    >
      <Icon name="add" />
    </button>
  ));

  return (
    <Section id="assistant" icon="comment-discussion" title="アシスタント" count={convs.length} actions={actions}>
      {convs.length === 0 ? (
        <div className="section-empty muted">チャットはまだありません。＋ で開始できます。</div>
      ) : (
        <ul className="list">
          {convs.map((c) => (
            <li key={c.id} className="chat-row">
              <button
                type="button"
                className="chat-open"
                onClick={() => openChat(c.id)}
                title={c.title}
              >
                <span className="chat-open-title">{c.title}</span>
                {c.message_count > 0 && <span className="chat-open-meta muted">{c.message_count}</span>}
              </button>
              <button
                type="button"
                className="ghost pane-btn chat-del"
                title="このチャットを削除"
                onClick={() => void remove(c.id)}
              >
                <Icon name="trash" />
              </button>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
