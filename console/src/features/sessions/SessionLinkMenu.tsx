// SessionLinkMenuHost — the session context menu for the session slugs a Markdown body
// auto-links (mdRefLinks.ts). A reply that names a child or a peer is where you decide what to
// do with it next, so the same SessionMenu as the rail row opens right there.
//
// Opt-in by surface: only a subtree under this host gets the menu. The mirror and the assistant
// chat mount one; the shared-session view must not (a recipient gets no owner actions) and the
// file viewer has no reason to, so their links keep the browser's own menu.
import { createContext, useCallback, useContext, useState } from "react";
import type { ReactNode } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { SessionMenu } from "./SessionMenu.tsx";
import { useSessionActions } from "./useSessionActions.tsx";
import { useSessionsStore } from "./store.ts";
import type { Session } from "../../types/session.ts";

/** Opens the menu for session `name` at viewport coordinates (x, y). */
export type OpenSessionLinkMenu = (name: string, x: number, y: number) => void;

const SessionLinkMenuContext = createContext<OpenSessionLinkMenu | null>(null);

/** The opener of the nearest host, or null where links keep the native menu. */
export function useSessionLinkMenu(): OpenSessionLinkMenu | null {
  return useContext(SessionLinkMenuContext);
}

type MenuState = { name: string; x: number; y: number; open: boolean };

export function SessionLinkMenuHost({ children }: { children: ReactNode }) {
  const tr = useT();
  const toast = useToast();
  const [menu, setMenu] = useState<MenuState | null>(null);
  // Looked up live, not from the menu state: a rename or a stop while the menu is open must show
  // the session as it is now. Gone (deleted) → the menu goes with it.
  const s = useSessionsStore((st) => (menu ? st.sessions.find((x) => x.name === menu.name) : undefined));

  // The link was built when the document rendered; the session may have been deleted since.
  // Answer that the same way a click on a stale commit / conversation link does.
  const open = useCallback<OpenSessionLinkMenu>(
    (name, x, y) => {
      if (!useSessionsStore.getState().sessions.some((x2) => x2.name === name)) {
        toast(tr("view.session_not_found", { name }));
        return;
      }
      setMenu({ name, x, y, open: true });
    },
    [toast, tr],
  );

  return (
    <SessionLinkMenuContext.Provider value={open}>
      {children}
      {/* Kept mounted while closed: SessionMenu owns the handoff / share dialogs, which
          outlive the menu itself. */}
      {menu && s && (
        <LinkMenu s={s} menu={menu} onClose={() => setMenu((m) => (m ? { ...m, open: false } : m))} />
      )}
    </SessionLinkMenuContext.Provider>
  );
}

// Split out so the session actions (and the confirm dialog they need) are only set up once a
// menu has actually been opened, not in every mirror and chat pane that never opens one.
function LinkMenu({ s, menu, onClose }: { s: Session; menu: MenuState; onClose: () => void }) {
  const running = useWorkspaceStore((st) => st.state) === "running";
  const actions = useSessionActions();
  return (
    <SessionMenu
      s={s}
      actions={actions}
      running={running}
      open={menu.open}
      place={(el) => placeFixed(el, menu.x, menu.y)}
      onClose={onClose}
    />
  );
}
