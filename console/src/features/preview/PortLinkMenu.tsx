// PortLinkMenuHost — the port menu for the loopback URLs a Markdown body carries
// (`http://localhost:5173/` in prose or inline code, wired by viewer/parts/mdPreviewLinks.ts).
// It is the same PortMenu the session row's port chip opens: every target, Copy URL, Expose.
//
// Opt-in by surface, like PathLinkMenuHost: the links are built imperatively, so the host hands
// them an opener through context. A subtree without a host keeps the browser's own menu, and the
// shared-session view mounts none.
import { lazy, Suspense, useCallback, useState } from "react";
import type { ReactNode } from "react";
import { PortLinkMenuContext, type OpenPortLinkMenu } from "./portLinkContext.ts";

// Lazy: every surface that renders Markdown mounts a host, and the menu pulls in the preview and
// workspace stores.
const LinkPortMenu = lazy(() => import("./LinkPortMenu.tsx"));

type MenuState = { port: number; path: string; x: number; y: number };

export function PortLinkMenuHost({ children }: { children: ReactNode }) {
  const [menu, setMenu] = useState<MenuState | null>(null);
  const open = useCallback<OpenPortLinkMenu>((port, path, x, y) => setMenu({ port, path, x, y }), []);
  return (
    <PortLinkMenuContext.Provider value={open}>
      {children}
      {menu && (
        <Suspense fallback={null}>
          <LinkPortMenu menu={menu} onClose={() => setMenu(null)} />
        </Suspense>
      )}
    </PortLinkMenuContext.Provider>
  );
}
