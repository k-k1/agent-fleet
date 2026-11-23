// ConversationMenu — the assistant-conversation context menu (copy ID, rename, working-set
// membership, lock/unlock, delete) and the rename dialog it opens, lifted out of
// AssistantSection so the SAME items appear wherever a conversation is shown: the left-pane
// chat row's ⋯ / right-click, and a right-click on that conversation's tab in the tabbed grid.
// The caller owns the open state and the placement (a row anchors under its ⋯ button, a tab
// opens at the cursor) — the only two things that differ.
//
// It stays mounted while closed on purpose: the rename dialog it owns outlives the menu, and
// unmounting on close would take it down with the menu.
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { copyText } from "../../lib/clipboard.ts";
import { getTenant } from "../../core/api/client.ts";
import { useSettings } from "../../lib/settings.ts";
import { convInSet, workingSetList, toggleWorkingSetMember } from "../../lib/workingSetsStore.ts";
import { useT } from "../../lib/i18n/index.ts";
import { chatDelete, chatList, chatSetLock } from "./api.ts";
import { useChatStore } from "./store.ts";
import { ChatTitleModal } from "./ChatTitleModal.tsx";
import type { ConversationMeta } from "../../types/chat.ts";

/** Re-reads the conversation list into the store. An answer asked under a previous tenant
 *  must not repopulate what the switch emptied. */
export function refreshConversations(): void {
  const started = getTenant();
  chatList()
    .then((r) => {
      if (getTenant() === started) useChatStore.getState().setConvs(r.conversations || []);
    })
    .catch(() => {});
}

interface ConversationMenuProps {
  c: ConversationMeta;
  open: boolean;
  /** Position the (position:fixed) menu element; runs on every render while open. */
  place: (el: HTMLElement) => void;
  onClose: () => void;
}

export function ConversationMenu({ c, open, place, onClose }: ConversationMenuProps) {
  const tr = useT();
  const toast = useToast();
  const wsets = workingSetList(useSettings());
  const menuRef = useRef<HTMLUListElement>(null);
  const [renaming, setRenaming] = useState<ConversationMeta | null>(null);

  useDismiss(menuRef, open, onClose);
  useMenuRoving(menuRef, open);
  // Clamped every render (no deps): a re-render must not undo the placement near a viewport edge.
  useLayoutEffect(() => {
    const el = menuRef.current;
    if (open && el) place(el);
  });
  const run = (fn: () => void) => {
    onClose();
    fn();
  };

  // Copy the conversation's short slug ("a…", docs/log/38 §assistant triggering) — the id humans
  // and schedules address a conversation by. UUID fallback only for a conversation the
  // agent hasn't backfilled yet.
  const copyId = () => {
    const ref = c.slug || c.id;
    void copyText(ref).then((ok) =>
      ok ? toast(tr("asst.id_copied", { id: ref }), { kind: "success" }) : toast(tr("common.copy_failed")),
    );
  };

  // Deletion lock (docs/log/45): pin or release this conversation against deletion. The
  // protection itself is on the Agent (DELETE answers 403); this only toggles it and greys
  // out the affordance.
  const toggleLock = async () => {
    const locked = !c.locked;
    const res = await chatSetLock(c.id, locked);
    if (res?.error) {
      toast(tr("asst.lock_failed"));
      return;
    }
    // A list refresh can race this mutation. Keep the menu and badge in sync
    // with the value the server just accepted.
    const all = useChatStore.getState().convs ?? [];
    useChatStore.getState().setConvs(all.map((item) => (item.id === c.id ? { ...item, locked: res?.locked ?? locked } : item)));
    toast(locked ? tr("asst.locked_on") : tr("asst.locked_off"), { kind: "success" });
    refreshConversations();
  };

  const remove = async () => {
    try {
      const res = await chatDelete(c.id);
      if (!res.ok) {
        toast(tr("asst.remove_failed"));
        return;
      }
      refreshConversations();
    } catch {
      toast(tr("asst.remove_failed"));
    }
  };

  return (
    <>
      {open &&
        createPortal(
          <ul className="ui-menu" ref={menuRef} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => run(copyId)}>
                <Icon name="copy" /> {tr("asst.copy_id", { id: c.slug || c.id })}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => run(() => setRenaming(c))}>
                <Icon name="edit" /> {tr("asst.rename")}
              </button>
            </li>
            {/* Working sets (docs/log/52): toggle this conversation's membership. */}
            {wsets.length > 0 && (
              <>
                <li className="ui-menu-sep" aria-hidden="true" />
                <li className="ui-menu-caption">{tr("wset.menu_caption")}</li>
                {wsets.map((w) => (
                  <li key={w.id}>
                    <button
                      type="button"
                      className="ui-menu-item"
                      onClick={() => run(() => toggleWorkingSetMember(w.id, "convs", c.id))}
                    >
                      <Icon name="check" className={convInSet(w, c.id) ? "wset-check" : "wset-check wset-check--off"} /> {w.name}
                    </button>
                  </li>
                ))}
              </>
            )}
            <li className="ui-menu-sep" aria-hidden="true" />
            <li>
              <button type="button" className="ui-menu-item" onClick={() => run(() => void toggleLock())}>
                <Icon name={c.locked ? "unlock" : "lock"} /> {c.locked ? tr("asst.unlock") : tr("asst.lock")}
              </button>
            </li>
            <li>
              <button
                type="button"
                className="ui-menu-item danger"
                disabled={!!c.locked}
                title={c.locked ? tr("asst.locked_hint") : undefined}
                onClick={() => run(() => void remove())}
              >
                <Icon name="trash" /> {tr("asst.delete_chat")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
      {renaming && (
        <ChatTitleModal
          id={renaming.id}
          title={renaming.title}
          onClose={() => setRenaming(null)}
          onSaved={refreshConversations}
        />
      )}
    </>
  );
}
