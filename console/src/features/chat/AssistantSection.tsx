// AssistantSection — left-rail home for headless-CLI chat (docs/19): a ＋新規
// assistant picker popover (templates stay out of the rail) over the
// conversation history list. Picking an assistant opens a DRAFT — nothing is
// persisted until the first message. Port onto the zustand stores.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { chatPanes, ordClass, paneCount } from "../../layout/badges.ts";
import { useChatStore } from "./store.ts";
import { openChat, openAssistantDraft, convTarget, draftTarget } from "./open.ts";
import { AssistantModal } from "./AssistantModal.tsx";
import {
  chatList,
  chatDelete,
  chatRename,
  assistantList,
  assistantCreate,
  assistantUpdate,
  assistantDelete,
} from "./api.ts";
import type { ConversationMeta } from "../../types/chat.ts";
import type { Assistant, AssistantInput } from "../../types/assistant.ts";

export function AssistantSection() {
  const layout = useLayoutStore((s) => s.layout);
  const setActive = useLayoutStore((s) => s.setActive);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const chatListTick = useChatStore((s) => s.listTick);
  const chatBusy = useChatStore((s) => s.busy);
  const multiPane = paneCount(layout) > 1;
  const cPanes = multiPane ? chatPanes(layout) : null;
  const toast = useToast();
  const askConfirm = useConfirm();
  const [convs, setConvs] = useState<ConversationMeta[]>([]);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [editing, setEditing] = useState<Assistant | null>(null);
  const [creating, setCreating] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const pickerRef = useRef<HTMLDivElement>(null);
  const pickerMenuRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; a: Assistant } | null>(null);
  const menuRef = useRef<HTMLUListElement>(null);

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
  }, [refresh, chatListTick]); // tick bumps when a draft becomes a real thread

  useDismiss(pickerRef, pickerOpen, () => setPickerOpen(false));
  // Anchor the popover below the ＋ button, viewport-clamped.
  useLayoutEffect(() => {
    const el = pickerMenuRef.current;
    const anchor = pickerRef.current;
    if (!pickerOpen || !el || !anchor) return;
    el.style.position = "fixed";
    el.style.right = "auto";
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 4);
  }, [pickerOpen]);

  const openMenu = (e: RMouseEvent, a: Assistant) => {
    e.preventDefault();
    e.stopPropagation();
    setPickerOpen(false);
    setMenu({ x: e.clientX, y: e.clientY, a });
  };
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    window.addEventListener("blur", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", close);
    };
  }, [menu]);
  // Clamped every render (no deps): the JSX re-applies the raw cursor coords as
  // inline style on re-renders, which would undo a one-shot clamp near the
  // viewport edge.
  useLayoutEffect(() => {
    if (menu && menuRef.current) placeFixed(menuRef.current, menu.x, menu.y);
  });
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };

  // Only started threads are listed — a draft is never persisted (docs/19).
  const startedConvs = useMemo(() => convs.filter((c) => c.message_count > 0), [convs]);

  const byId = useMemo(() => {
    const m: Record<string, Assistant> = {};
    for (const a of assistants) m[a.id] = a;
    return m;
  }, [assistants]);

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

  const deleteAssistant = async (a: Assistant) => {
    const ok = await askConfirm({
      title: "アシスタントを削除",
      body: `「${a.name}」を削除します。作成済みの会話は残ります。`,
      confirmLabel: "削除",
      danger: true,
    });
    if (!ok) return;
    try {
      await assistantDelete(a.id);
      refresh();
    } catch {
      toast("アシスタントの削除に失敗しました");
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

  const renameConv = async (c: ConversationMeta) => {
    const name = window.prompt("表示名を変更", c.title);
    if (name == null) return;
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
        className="ui-btn ui-btn-ghost ui-iconbtn"
        title="新規チャット"
        onClick={() => setPickerOpen((o) => !o)}
      >
        <Icon name="add" />
      </button>
      {pickerOpen && (
        <div className="ui-menu assistant-picker" ref={pickerMenuRef} role="menu">
          <div className="assistant-picker-label">新規チャット</div>
          {assistants.map((a) => (
            <div key={a.id} className="assistant-picker-row">
              <button
                type="button"
                className="ui-menu-item assistant-picker-open"
                title={a.description || a.name}
                onClick={(e) => {
                  setPickerOpen(false);
                  if (e.ctrlKey || e.metaKey) openTargetInNew(draftTarget(a.id));
                  else startChat(a);
                }}
                onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                onAuxClick={(e) => {
                  if (e.button === 1) {
                    setPickerOpen(false);
                    openTargetInNew(draftTarget(a.id));
                  }
                }}
                onContextMenu={(e) => openMenu(e, a)}
              >
                <Icon name={a.icon || "comment-discussion"} className="assistant-ic" />
                <span className="chat-open-title">{a.name}</span>
                {a.builtin && <span className="assistant-badge">常設</span>}
              </button>
              {!a.builtin && (
                <>
                  <button
                    type="button"
                    className="ui-btn ui-btn-ghost ui-iconbtn"
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
                    className="ui-btn ui-btn-ghost ui-iconbtn"
                    title="削除"
                    onClick={() => {
                      setPickerOpen(false);
                      void deleteAssistant(a);
                    }}
                  >
                    <Icon name="trash" />
                  </button>
                </>
              )}
            </div>
          ))}
          <div className="ui-menu-sep" aria-hidden="true" />
          <button
            type="button"
            className="ui-menu-item assistant-picker-open"
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
      <Section id="assistant" icon="comment-discussion" title="アシスタント" count={startedConvs.length} actions={actions}>
        {startedConvs.length === 0 ? (
          <div className="section-empty">チャットはまだありません。＋ から開始できます。</div>
        ) : (
          <ul className="sess-list">
            {startedConvs.map((c) => {
              const a = c.assistant_id ? byId[c.assistant_id] : undefined;
              return (
                <li key={c.id} className="chat-row">
                  <button
                    type="button"
                    className="chat-open"
                    title={c.title}
                    onClick={(e) => (e.ctrlKey || e.metaKey ? openTargetInNew(convTarget(c.id)) : openChat(c.id))}
                    onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                    onAuxClick={(e) => e.button === 1 && openTargetInNew(convTarget(c.id))}
                  >
                    {/* Same row language as the session rows: a tinted leading
                        icon square + an icon-only state chip (text in tooltip). */}
                    <span className="sess-kic chat-kic">
                      <Icon name={a?.icon || "comment"} />
                    </span>
                    <span className="chat-open-title">{c.title}</span>
                    {c.message_count > 0 && <span className="chat-open-meta">{c.message_count}</span>}
                    {chatBusy[c.id] ? (
                      <span className="session-state working mini" title="進行中">
                        <Icon name="loading" spin />
                      </span>
                    ) : (
                      <span className="session-state on mini" title="待機中">
                        <Icon name="check" />
                      </span>
                    )}
                  </button>
                  {cPanes?.get(c.id)?.length ? (
                    <span className="sess-ords">
                      {cPanes.get(c.id)!.map((o) => (
                        <button
                          key={o.id}
                          type="button"
                          className={"rail-ord " + ordClass(o.ordinal)}
                          title={`ペイン${o.ordinal}にフォーカス`}
                          onClick={(e) => {
                            e.stopPropagation();
                            setActive(o.id);
                          }}
                        >
                          {o.ordinal}
                        </button>
                      ))}
                    </span>
                  ) : null}
                  <span className="chat-actions">
                    <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn chat-del" title="表示名を変更" onClick={() => void renameConv(c)}>
                      <Icon name="edit" />
                    </button>
                    <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn chat-del" title="このチャットを削除" onClick={() => void removeConv(c.id)}>
                      <Icon name="trash" />
                    </button>
                  </span>
                </li>
              );
            })}
          </ul>
        )}
        {menu && (
          <ul className="ui-menu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => startChat(menu.a))}>
                <Icon name="comment" /> 新規チャット
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => openTargetInNew(draftTarget(menu.a.id), true))}>
                <Icon name="split-horizontal" /> 新しいペインで開く
              </button>
            </li>
            {!menu.a.builtin && (
              <>
                <li className="ui-menu-sep" aria-hidden="true" />
                <li>
                  <button type="button" className="ui-menu-item" onClick={() => runMenu(() => setEditing(menu.a))}>
                    <Icon name="edit" /> 編集
                  </button>
                </li>
                <li>
                  <button type="button" className="ui-menu-item danger" onClick={() => runMenu(() => void deleteAssistant(menu.a))}>
                    <Icon name="trash" /> 削除
                  </button>
                </li>
              </>
            )}
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
    </>
  );
}
