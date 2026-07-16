// A tiny per-pane action registry so GLOBAL keyboard commands (features/keys/commands.ts)
// can drive a view's LOCAL state that isn't lifted into the layout descriptor. Keyed by
// paneId (the pane's stable identity — see layout/types.ts), mirroring how the terminal
// service keys xterm instances. A view registers its handlers on mount and clears them on
// unmount; a command looks up the active pane's handlers and calls what exists.
//
// Only genuinely component-local, non-persistent toggles belong here (e.g. FileView's
// Markdown preview/source mode). Anything that should survive reloads or be addressable
// from the layout (wrap, pane content) stays in the pane descriptor instead.
export interface PaneViewActions {
  /** Cycle the Markdown preview/source (and slides, for Marp) toggle. Absent on
   * non-Markdown files, so the command no-ops there. */
  toggleMdMode?: () => void;
}

const registry = new Map<string, PaneViewActions>();

/** Register a pane's view actions; returns a cleanup that removes them (identity-guarded
 * so a late unmount can't clobber a remount that already re-registered). */
export function registerPaneViewActions(paneId: string, actions: PaneViewActions): () => void {
  registry.set(paneId, actions);
  return () => {
    if (registry.get(paneId) === actions) registry.delete(paneId);
  };
}

export function paneViewActions(paneId: string): PaneViewActions | undefined {
  return registry.get(paneId);
}
