import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import AssistantModal from "../AssistantModal.jsx";
import ConfirmDialog from "../ConfirmDialog.jsx";
import { useApp } from "../../state.jsx";
import { useToast } from "../ToastProvider.jsx";
import { useDismiss } from "../../lib/useDismiss.js";
import { placeFixed } from "../../lib/placeFixed.js";
import {
  chatList,
  chatDelete,
  chatRename,
  assistantList,
  assistantCreate,
  assistantUpdate,
  assistantDelete,
} from "../../api.js";
import type { ConversationMeta } from "../../types/chat.ts";
import type { Assistant, AssistantInput } from "../../types/assistant.ts";

// AssistantSection is the left-rail home for headless-CLI chat (docs/19). To keep the
// rail short, the ASSISTANTS (templates) live behind a "＋新規" picker popover rather
// than a permanent list; the section body shows only the CONVERSATION history (the
// thing that grows and that the user returns to). Picking an assistant opens a draft —
// nothing is persisted until the first message is sent.
export default function AssistantSection() {
  const { openChat, openAssistantDraft, chatListKey } = useApp();
  const toast = useToast();
  const [convs, setConvs] = useState<ConversationMeta[]>([]);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [editing, setEditing] = useState<Assistant | null>(null); // target of the edit modal
  const [creating, setCreating] = useState(false); // create modal open
  const [deleting, setDeleting] = useState<Assistant | null>(null); // pending delete confirm
  const [delBusy, setDelBusy] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false); // ＋新規 assistant picker popover
  const pickerRef = useRef<HTMLDivElement>(null); // popover wrap (outside-click test + anchor)
  const pickerMenuRef = useRef<HTMLDivElement>(null); // popover (clamped into the viewport)

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

  useDismiss(pickerRef, pickerOpen, () => setPickerOpen(false));
  // Anchor the popover below the ＋ button as a viewport-clamped fixed menu so the pane's
  // overflow can't clip it (mirrors the Files ＋ dropdown).
  useLayoutEffect(() => {
    const el = pickerMenuRef.current;
    const anchor = pickerRef.current;
    if (!pickerOpen || !el || !anchor) return;
    el.style.position = "fixed";
    el.style.right = "auto";
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 4);
  }, [pickerOpen]);

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

  // Rename a conversation's list title — the auto-title from the first message often
  // isn't what the user wants once the thread has a topic (docs/19).
  const renameConv = async (c: ConversationMeta) => {
    const name = window.prompt("表示名を変更", c.title);
    if (name == null) return; // cancelled
    const t = name.trim();
    if (!t || t === c.title) return;
    try {
      await chatRename(c.id, t);
      refresh();
    } catch {
      toast("名前の変更に失敗しました");
    }
  };

  const actions = (
    <div className="assistant-picker-wrap" ref={pickerRef}>
      <button
        type="button"
        className="ghost pane-btn"
        title="新規チャット"
        onClick={() => setPickerOpen((o) => !o)}
      >
        <Icon name="add" />
      </button>
      {pickerOpen && (
        <div className="ctxmenu assistant-picker" ref={pickerMenuRef} role="menu">
          <div className="assistant-picker-label muted">新規チャット</div>
          {assistants.map((a) => (
            <div key={a.id} className="assistant-picker-row">
              <button
                type="button"
                className="assistant-picker-open"
                title={a.description || a.name}
                onClick={() => {
                  setPickerOpen(false);
                  startChat(a);
                }}
              >
                <Icon name={a.icon || "comment-discussion"} className="assistant-ic" />
                <span className="chat-open-title">{a.name}</span>
                {a.builtin && <span className="assistant-badge muted">常設</span>}
              </button>
              {!a.builtin && (
                <>
                  <button
                    type="button"
                    className="ghost pane-btn"
                    title="編集"
                    onClick={() => {
                      setPickerOpen(false);
                      setEditing(a);
                    }}
                  >
                    <Icon name="edit" />
                  </button>
                  <button
                    type="button"
                    className="ghost pane-btn"
                    title="削除"
                    onClick={() => {
                      setPickerOpen(false);
                      setDeleting(a);
                    }}
                  >
                    <Icon name="trash" />
                  </button>
                </>
              )}
            </div>
          ))}
          <div className="ctx-sep" aria-hidden="true" />
          <button
            type="button"
            className="assistant-picker-open"
            onClick={() => {
              setPickerOpen(false);
              setCreating(true);
            }}
          >
            <Icon name="add" className="assistant-ic" />
            <span className="chat-open-title">アシスタントを作成</span>
          </button>
        </div>
      )}
    </div>
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
        {startedConvs.length === 0 ? (
          <div className="section-empty muted">チャットはまだありません。＋新規 から開始できます。</div>
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
                    title="表示名を変更"
                    onClick={() => void renameConv(c)}
                  >
                    <Icon name="edit" />
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
