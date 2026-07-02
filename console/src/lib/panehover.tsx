import { createContext, useContext, useMemo, useState } from "react";
import type { ReactNode } from "react";

// PaneHover is a tiny standalone context for the "which pane/session is the pointer
// over" signal that cross-highlights the session list, the layout mini-map, and the
// panes. It's deliberately separate from the main AppContext so hover churn (enter/
// leave events) only re-renders the handful of components that opt in here — not the
// whole app (terminals included).
//
// The value is `{ session, paneId }` (either field may be null) or null when nothing
// is hovered. Matchers highlight on a paneId match OR a shared session name, so a
// session open in two panes lights both, and a session-less pane (file/scm) still
// pairs with its own map cell.

export interface PaneHover {
  session: string | null;
  paneId: string | null;
}

interface PaneHoverCtx {
  hover: PaneHover | null;
  setHover: (h: PaneHover | null) => void;
}

const Ctx = createContext<PaneHoverCtx | null>(null);

export function PaneHoverProvider({ children }: { children: ReactNode }) {
  const [hover, setHover] = useState<PaneHover | null>(null);
  const value = useMemo<PaneHoverCtx>(() => ({ hover, setHover }), [hover]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function usePaneHover(): PaneHoverCtx {
  return useContext(Ctx) || { hover: null, setHover: () => {} };
}

// hoverMatches decides if a pane (by id + session) is the current hover target.
export function hoverMatches(
  hover: PaneHover | null,
  paneId: string,
  session: string | null,
): boolean {
  if (!hover) return false;
  if (hover.paneId && hover.paneId === paneId) return true;
  return !!(hover.session && session && hover.session === session);
}
