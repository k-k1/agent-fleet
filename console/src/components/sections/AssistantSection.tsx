import { useCallback, useEffect, useMemo, useState } from "react";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import AssistantModal from "../AssistantModal.jsx";
import ConfirmDialog from "../ConfirmDialog.jsx";
import { useApp } from "../../state.jsx";
import { useToast } from "../ToastProvider.jsx";
import {
  chatList,
  chatDelete,
  assistantList,
  assistantCreate,
  assistantUpdate,
  assistantDelete,
} from "../../api.js";
import type { ConversationMeta } from "../../types/chat.ts";
import type { Assistant, AssistantInput } from "../../types/assistant.ts";

// AssistantSection is the left-rail home for headless-CLI chat (docs/19 Q2). It shows
// two groups: the configurable ASSISTANTS (builtin + user-defined; clicking one starts
// a new chat from that template) and the existing CONVERSATIONS. Assistants are custom-
// GPT-style templates — a new chat snapshots the assistant's persona/model/tools/
// knowledge, so later edits don't rewrite existing threads.
export default function AssistantSection() {
  const { openChat, openAssistantDraft, chatListKey } = useApp();
  const toast = useToast();
  const [convs, setConvs] = useState<ConversationMeta[]>([]);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [editing, setEditing] = useState<Assistant | null>(null); // target of the edit modal
  const [creating, setCreating] = useState(false); // create modal open
  const [deleting, setDeleting] = useState<Assistant | null>(null); // pending delete confirm
  const [delBusy, setDelBusy] = useState(false);

  const refresh = useCallback(() => {
    chatList()
      .then((r) => setConvs(r.conversations || []))
      .catch(() => {});
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, []);
  useEffect(() => {
    refresh();
  }, [refresh, chatListKey]); // chatListKey bumps when a draft becomes a real thread

  // Only started threads are listed — a draft (assistant opened but not yet messaged) is
  // never persisted, and an abandoned empty thread shouldn't clutter the list (docs/19).
  const startedConvs = useMemo(() => convs.filter((c) => c.message_count > 0), [convs]);

  const byId = useMemo(() => {
    const m: Record<string, Assistant> = {};
    for (const a of assistants) m[a.id] = a;
    return m;
  }, [assistants]);

  // Open a DRAFT for this assistant — nothing is persisted until the first message is
  // sent (docs/19). ChatView shows the assistant's greeting until then.
  const startChat = (a: Assistant) => openAssistantDraft(a.id);

  const saveAssistant = async (input: AssistantInput) => {
    try {
      if (editing) await assistantUpdate(editing.id, input);
      else await assistantCreate(input);
      refresh();
    } catch {
      toast(editing ? "アシスタントの更新に失敗しました" : "アシスタントの作成に失敗しました");
    }
  };

  const confirmDelete = async () => {
    if (!deleting) return;
    setDelBusy(true);
    try {
      await assistantDelete(deleting.id);
      setDeleting(null);
      refresh();
    } catch {
      toast("アシスタントの削除に失敗しました");
    } finally {
      setDelBusy(false);
    }
  };

  const removeConv = async (id: string) => {
    try {
      await chatDelete(id);
      refresh();
    } catch {
      toast("削除に失敗しました");
    }
  };

  const actions = (
    <button type="button" className="ghost pane-btn" title="アシスタントを作成" onClick={() => setCreating(true)}>
      <Icon name="add" />
    </button>
  );

  return (
    <>
      <Section
        id="assistant"
        icon="comment-discussion"
        title="アシスタント"
        count={startedConvs.length}
        actions={actions}
      >
        <div className="assistant-group-label muted">アシスタント</div>
        <ul className="list">
          {assistants.map((a) => (
            <li key={a.id} className="chat-row">
              <button
                type="button"
                className="chat-open"
                onClick={() => void startChat(a)}
                title={a.name + " で新規チャット"}
              >
                <Icon name={a.icon || "comment-discussion"} className="assistant-ic" />
                <span className="chat-open-title">{a.name}</span>
                {a.builtin && <span className="assistant-badge muted">常設</span>}
              </button>
              {!a.builtin && (
                <>
                  <button
                    type="button"
                    className="ghost pane-btn chat-del"
                    title="編集"
                    onClick={() => setEditing(a)}
                  >
                    <Icon name="edit" />
                  </button>
                  <button
                    type="button"
                    className="ghost pane-btn chat-del"
                    title="削除"
                    onClick={() => setDeleting(a)}
                  >
                    <Icon name="trash" />
                  </button>
                </>
              )}
            </li>
          ))}
        </ul>

        <div className="assistant-group-label muted">会話</div>
        {startedConvs.length === 0 ? (
          <div className="section-empty muted">チャットはまだありません。アシスタントを選んで開始できます。</div>
        ) : (
          <ul className="list">
            {startedConvs.map((c) => {
              const a = c.assistant_id ? byId[c.assistant_id] : undefined;
              return (
                <li key={c.id} className="chat-row">
                  <button type="button" className="chat-open" onClick={() => openChat(c.id)} title={c.title}>
                    <Icon name={a?.icon || "comment"} className="assistant-ic" />
                    <span className="chat-open-title">{c.title}</span>
                    {c.message_count > 0 && <span className="chat-open-meta muted">{c.message_count}</span>}
                  </button>
                  <button
                    type="button"
                    className="ghost pane-btn chat-del"
                    title="このチャットを削除"
                    onClick={() => void removeConv(c.id)}
                  >
                    <Icon name="trash" />
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </Section>

      {(creating || editing) && (
        <AssistantModal
          initial={editing}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSave={saveAssistant}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="アシスタントを削除"
          confirmLabel="削除"
          busy={delBusy}
          onCancel={() => setDeleting(null)}
          onConfirm={() => void confirmDelete()}
        >
          「{deleting.name}」を削除します。作成済みの会話は残ります。
        </ConfirmDialog>
      )}
    </>
  );
}
