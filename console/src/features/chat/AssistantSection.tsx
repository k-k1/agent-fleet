// AssistantSection — left-rail home for headless-CLI chat (docs/19): a ＋新規
// assistant picker popover (templates stay out of the rail) over the
// conversation history list. Picking an assistant opens a DRAFT — nothing is
// persisted until the first message. Port onto the zustand stores.
import { createPortal } from "react-dom";
import { memo, useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { isTransientErr } from "../../core/api/client.ts";
import { copyText } from "../../lib/clipboard.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activeChatId, chatPanes, ordClass, paneCount } from "../../layout/badges.ts";
import { useChatStore } from "./store.ts";
import { openChat, openAssistantDraft, convTarget, draftTarget } from "./open.ts";
import { AssistantModal } from "./AssistantModal.tsx";
import { ChatTitleModal } from "./ChatTitleModal.tsx";
import { assistantName, assistantDesc } from "./assistantI18n.ts";
import { useSettings } from "../../lib/settings.ts";
import { useActiveWorkingSet, convInSet, workingSetList, toggleWorkingSetMember } from "../../lib/workingSetsStore.ts";
import { useT } from "../../lib/i18n/index.ts";
import {
  chatList,
  chatDelete,
  chatSetLock,
  assistantList,
  assistantCreate,
  assistantUpdate,
  assistantDelete,
} from "./api.ts";
import type { ConversationMeta } from "../../types/chat.ts";
import type { Assistant, AssistantInput } from "../../types/assistant.ts";

export const AssistantSection = memo(function AssistantSection() {
  const layout = useLayoutStore((s) => s.layout);
  const setActive = useLayoutStore((s) => s.setActive);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const chatListTick = useChatStore((s) => s.listTick);
  const chatBusy = useChatStore((s) => s.busy);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const multiPane = paneCount(layout) > 1;
  // Which chats are on screen, and which one the focused pane is showing. Ordinal badges
  // stay a split-only affordance, but the open/current marks apply with a single pane too
  // — that's the case where nothing else in the rail said which chat you were talking to.
  const cPanes = chatPanes(layout);
  const currentChat = activeChatId(layout);
  const toast = useToast();
  const askConfirm = useConfirm();
  const tr = useT(); // docs/28 P3: re-render builtin assistant names/descriptions on locale switch
  // Conversation metas live in the chat store (not local state) so other surfaces —
  // the markdown conv-slug linkifier — can resolve slugs without mounting this rail.
  const convs = useChatStore((s) => s.convs) ?? [];
  const setConvs = useChatStore((s) => s.setConvs);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [editing, setEditing] = useState<Assistant | null>(null);
  const [creating, setCreating] = useState(false);
  const [renaming, setRenaming] = useState<ConversationMeta | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);
  const pickerRef = useRef<HTMLDivElement>(null);
  const pickerMenuRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; a: Assistant } | null>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  // Right-click menu for a conversation row (title rename + copy id) — same
  // portal/roving/dismiss pattern as the assistant-template menu above.
  const [convMenu, setConvMenu] = useState<{ x: number; y: number; c: ConversationMeta; anchor?: DOMRect } | null>(null);
  const convMenuRef = useRef<HTMLUListElement>(null);

  const refresh = useCallback(() => {
    chatList()
      .then((r) => setConvs(r.conversations || []))
      .catch(() => {});
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, [setConvs]);
  // 一覧は agent へプロキシされるので、WS 起動直後は不通で {error: http_5xx} が返る（api() は
  // これを例外にせず解決するため上の .catch は拾わない）。この欄は他の左ペイン欄と違ってポーリングが
  // 無く、過渡応答を空と確定すると次の listTick まで「チャットはまだありません」のまま無期限に固着
  // していた。running 中の過渡的失敗はバックオフ再試行し、停止中（同じ 502 が返る）は空を確定する。
  // deps の chatListTick は、下書きが実スレッドになったときに再取得させるためのもの。
  useRetryLoad(
    async (signal) => {
      const [c, a] = await Promise.all([chatList().catch(() => null), assistantList().catch(() => null)]);
      if (signal.aborted) return true;
      const stalled = (r: unknown) => r === null || isTransientErr(r); // 例外＝通信断も過渡的
      if (running && (stalled(c) || stalled(a))) return false; // agent still booting — retry
      setConvs(c?.conversations || []);
      setAssistants(a?.assistants || []);
      return true;
    },
    [chatListTick, running],
  );

  useDismiss([pickerRef, pickerMenuRef], pickerOpen, () => setPickerOpen(false));
  // Anchor the popover below the ＋ button, viewport-clamped.
  useLayoutEffect(() => {
    const el = pickerMenuRef.current;
    const anchor = pickerRef.current;
    if (!pickerOpen || !el || !anchor) return;
    el.style.position = "fixed";
    el.style.right = "auto";
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 4, pickerRef.current?.closest<HTMLElement>(".app-rail"));
  }, [pickerOpen]);

  const openMenu = (e: RMouseEvent, a: Assistant) => {
    e.preventDefault();
    e.stopPropagation();
    setPickerOpen(false);
    setMenu({ x: e.clientX, y: e.clientY, a });
  };
  useDismiss(menuRef, !!menu, () => setMenu(null));
  useMenuRoving(menuRef, !!menu);
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

  const openConvMenu = (e: RMouseEvent, c: ConversationMeta) => {
    e.preventDefault();
    e.stopPropagation();
    setConvMenu({ x: e.clientX, y: e.clientY, c });
  };
  // ⋯ kebab: anchor the menu under the button (right-aligned) rather than at a
  // cursor point, so touch users get a two-step delete instead of a mis-tappable
  // trash button.
  const openConvMenuBtn = (e: RMouseEvent, c: ConversationMeta) => {
    e.preventDefault();
    e.stopPropagation();
    const r = e.currentTarget.getBoundingClientRect();
    setConvMenu({ x: r.left, y: r.bottom + 2, c, anchor: r });
  };
  useDismiss(convMenuRef, !!convMenu, () => setConvMenu(null));
  useMenuRoving(convMenuRef, !!convMenu);
  useLayoutEffect(() => {
    const el = convMenuRef.current;
    if (!convMenu || !el) return;
    const bounds = el.closest<HTMLElement>(".app-rail");
    if (convMenu.anchor) placeFixed(el, convMenu.anchor.right - el.offsetWidth, convMenu.anchor.bottom + 2, bounds);
    else placeFixed(el, convMenu.x, convMenu.y, bounds);
  });
  const runConvMenu = (fn: () => void) => {
    setConvMenu(null);
    fn();
  };

  // Only started threads are listed — a draft is never persisted (docs/19).
  // 作業グループ (docs/52): scope the list to the active group's conversations.
  const wsets = workingSetList(useSettings());
  const wset = useActiveWorkingSet();
  const startedConvs = useMemo(
    () => convs.filter((c) => c.message_count > 0 && (!wset || convInSet(wset, c.id))),
    [convs, wset],
  );

  const byId = useMemo(() => {
    const m: Record<string, Assistant> = {};
    for (const a of assistants) m[a.id] = a;
    return m;
  }, [assistants]);

  const startChat = (a: Assistant) => openAssistantDraft(a.id);

  const saveAssistant = async (input: AssistantInput) => {
    // apiJSON はサーバエラーを {error} で解決する（例外にならない）。黙って成功扱いにすると
    // モーダルが閉じて入力（persona 等）が消えるので、失敗はトーストして throw し、
    // AssistantModal 側は開いたままにする。
    const res = await (editing ? assistantUpdate(editing.id, input) : assistantCreate(input)).catch(() => null);
    if (!res || (res as { error?: unknown }).error) {
      toast(editing ? tr("asst.update_failed") : tr("asst.create_failed"));
      throw new Error("assistant save failed");
    }
    refresh();
  };

  const deleteAssistant = async (a: Assistant) => {
    const ok = await askConfirm({
      title: tr("asst.delete_assistant"),
      body: tr("asst.delete_confirm", { name: a.name }),
      confirmLabel: tr("asst.delete"),
      danger: true,
    });
    if (!ok) return;
    try {
      // raw() は HTTP エラーでも resolve する — res.ok を見ないと失敗が無通知になる。
      const res = await assistantDelete(a.id);
      if (!res.ok) {
        toast(tr("asst.delete_failed"));
        return;
      }
      refresh();
    } catch {
      toast(tr("asst.delete_failed"));
    }
  };

  const removeConv = async (id: string) => {
    try {
      const res = await chatDelete(id);
      if (!res.ok) {
        toast(tr("asst.remove_failed"));
        return;
      }
      refresh();
    } catch {
      toast(tr("asst.remove_failed"));
    }
  };

  // 削除ロック（docs/45）: この会話を削除保護に固定/解除する。保護そのものは Agent
  // 側（DELETE が 403）で、ここは切替と見た目の抑止だけ。
  const toggleConvLock = async (c: ConversationMeta) => {
    const locked = !c.locked;
    const res = await chatSetLock(c.id, locked);
    if (res?.error) {
      toast(tr("asst.lock_failed"));
      return;
    }
    // A list refresh can race this mutation. Keep the menu and badge in sync
    // with the value the server just accepted.
    const all = useChatStore.getState().convs ?? [];
    setConvs(all.map((item) => (item.id === c.id ? { ...item, locked: res?.locked ?? locked } : item)));
    toast(locked ? tr("asst.locked_on") : tr("asst.locked_off"), { kind: "success" });
    refresh();
  };

  // Copy the conversation's short slug ("a…", docs/38 アシスタント発火) — the id humans
  // and schedules address a conversation by. UUID fallback only for a conversation the
  // agent hasn't backfilled yet.
  const copyId = (c: ConversationMeta) => {
    const ref = c.slug || c.id;
    void copyText(ref).then((ok) =>
      ok ? toast(tr("asst.id_copied", { id: ref }), { kind: "success" }) : toast(tr("common.copy_failed")),
    );
  };

  const actions = (
    <div className="assistant-picker-wrap" ref={pickerRef}>
      <button
        type="button"
        className="ui-btn ui-btn-ghost ui-iconbtn"
        title={tr("asst.new_chat")}
        onClick={() => setPickerOpen((o) => !o)}
      >
        <Icon name="add" />
      </button>
      {pickerOpen &&
        createPortal(
          <div className="ui-menu assistant-picker" ref={pickerMenuRef} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <div className="assistant-picker-label">{tr("asst.new_chat")}</div>
            {assistants.map((a) => (
              <div key={a.id} className="assistant-picker-row">
                <button
                  type="button"
                  className="ui-menu-item assistant-picker-open"
                  title={assistantDesc(a) || assistantName(a)}
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
                  <span className="chat-open-title">{assistantName(a)}</span>
                  {a.builtin && <span className="assistant-badge">{tr("asst.builtin_badge")}</span>}
                </button>
                {!a.builtin && (
                  <>
                    <button
                      type="button"
                      className="ui-btn ui-btn-ghost ui-iconbtn"
                      title={tr("asst.edit")}
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
                      title={tr("asst.delete")}
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
              <span className="chat-open-title">{tr("asst.create_assistant")}</span>
            </button>
          </div>,
          document.body,
        )}
    </div>
  );

  return (
    <>
      <Section id="assistant" icon="comment-discussion" title={tr("asst.section_title")} count={startedConvs.length} actions={actions}>
        {startedConvs.length === 0 ? (
          <div className="section-empty">{tr("asst.empty")}</div>
        ) : (
          <ul className="sess-list">
            {startedConvs.map((c) => {
              const a = c.assistant_id ? byId[c.assistant_id] : undefined;
              const opens = cPanes.get(c.id);
              const current = currentChat === c.id;
              return (
                <li
                  key={c.id}
                  className={"chat-row" + (opens?.length ? " is-open" : "") + (current ? " active" : "")}
                >
                  <button
                    type="button"
                    className="chat-open"
                    aria-current={current ? "true" : undefined}
                    title={`${c.title}\n${c.slug ? `slug: ${c.slug}\n` : ""}ID: ${c.id}`}
                    onClick={(e) => (e.ctrlKey || e.metaKey ? openTargetInNew(convTarget(c.id)) : openChat(c.id))}
                    onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                    onAuxClick={(e) => e.button === 1 && openTargetInNew(convTarget(c.id))}
                    onContextMenu={(e) => openConvMenu(e, c)}
                  >
                    {/* Same row language as the session rows: a tinted leading
                        icon square + an icon-only state chip (text in tooltip). */}
                    <span className="sess-kic chat-kic">
                      <Icon name={a?.icon || "comment"} />
                    </span>
                    <span className="chat-open-title">{c.title}</span>
                    {c.message_count > 0 && <span className="chat-open-meta">{c.message_count}</span>}
                    {/* 削除ロック（docs/45）の鍵バッジ — セッション行と同じ語彙。 */}
                    {c.locked && <Icon name="lock" className="sess-lock" title={tr("asst.locked_hint")} />}
                    {chatBusy[c.id] ? (
                      <span className="session-state working mini" title={tr("asst.in_progress")}>
                        <Icon name="loading" spin />
                      </span>
                    ) : (
                      <span className="session-state on mini" title={tr("asst.waiting")}>
                        <Icon name="check" />
                      </span>
                    )}
                  </button>
                  {multiPane && opens?.length ? (
                    <span className="sess-ords">
                      {opens.map((o) => (
                        <button
                          key={o.id}
                          type="button"
                          className={"rail-ord " + ordClass(o.ordinal)}
                          title={tr("asst.focus_pane", { n: o.ordinal })}
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
                    <button type="button" className="chat-menu-btn" title={tr("srow.menu")} aria-haspopup="menu" onClick={(e) => openConvMenuBtn(e, c)}>
                      <Icon name="ellipsis" />
                    </button>
                  </span>
                </li>
              );
            })}
          </ul>
        )}
        {menu &&
          createPortal(
            <ul className="ui-menu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => startChat(menu.a))}>
                  <Icon name="comment" /> {tr("asst.new_chat")}
                </button>
              </li>
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => openTargetInNew(draftTarget(menu.a.id), true))}>
                  <Icon name="split-horizontal" /> {tr("asst.open_new_pane")}
                </button>
              </li>
              {!menu.a.builtin && (
                <>
                  <li className="ui-menu-sep" aria-hidden="true" />
                  <li>
                    <button type="button" className="ui-menu-item" onClick={() => runMenu(() => setEditing(menu.a))}>
                      <Icon name="edit" /> {tr("asst.edit")}
                    </button>
                  </li>
                  <li>
                    <button type="button" className="ui-menu-item danger" onClick={() => runMenu(() => void deleteAssistant(menu.a))}>
                      <Icon name="trash" /> {tr("asst.delete")}
                    </button>
                  </li>
                </>
              )}
            </ul>,
            document.body,
          )}
        {convMenu &&
          createPortal(
            <ul className="ui-menu" ref={convMenuRef} style={{ left: convMenu.x, top: convMenu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runConvMenu(() => copyId(convMenu.c))}>
                  <Icon name="copy" /> {tr("asst.copy_id", { id: convMenu.c.slug || convMenu.c.id })}
                </button>
              </li>
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runConvMenu(() => setRenaming(convMenu.c))}>
                  <Icon name="edit" /> {tr("asst.rename")}
                </button>
              </li>
              {/* 作業グループ (docs/52): toggle this conversation's membership. */}
              {wsets.length > 0 && (
                <>
                  <li className="ui-menu-sep" aria-hidden="true" />
                  <li className="ui-menu-caption">{tr("wset.menu_caption")}</li>
                  {wsets.map((w) => (
                    <li key={w.id}>
                      <button
                        type="button"
                        className="ui-menu-item"
                        onClick={() => runConvMenu(() => toggleWorkingSetMember(w.id, "convs", convMenu.c.id))}
                      >
                        <Icon name="check" className={convInSet(w, convMenu.c.id) ? "wset-check" : "wset-check off"} /> {w.name}
                      </button>
                    </li>
                  ))}
                </>
              )}
              <li className="ui-menu-sep" aria-hidden="true" />
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runConvMenu(() => void toggleConvLock(convMenu.c))}>
                  <Icon name={convMenu.c.locked ? "unlock" : "lock"} />{" "}
                  {convMenu.c.locked ? tr("asst.unlock") : tr("asst.lock")}
                </button>
              </li>
              <li>
                <button
                  type="button"
                  className="ui-menu-item danger"
                  disabled={!!convMenu.c.locked}
                  title={convMenu.c.locked ? tr("asst.locked_hint") : undefined}
                  onClick={() => runConvMenu(() => void removeConv(convMenu.c.id))}
                >
                  <Icon name="trash" /> {tr("asst.delete_chat")}
                </button>
              </li>
            </ul>,
            document.body,
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
      {renaming && (
        <ChatTitleModal
          id={renaming.id}
          title={renaming.title}
          onClose={() => setRenaming(null)}
          onSaved={refresh}
        />
      )}
    </>
  );
});
