// Settings/Admin dialog UI store (zustand) — replaces the old God-context slice
// (settingsOpen/settingsSection/adminOpen + connKey).
//
// The settings modal gets close-on-back for free from the shared ui/Modal
// (useBackClose), like every other dialog, so it needs NO bespoke history entry
// here — pushing one on top of Modal's own guard double-stacked the history and
// made ✕/Esc reopen instead of close. The Admin modal does NOT use ui/Modal (it's
// a full-screen surface with its own drill-down history: tenants→tenant→member),
// so it keeps the bespoke entry: openAdmin pushes {modal:"admin"}, closeAdmin pops
// all drill levels at once, and wireSettingsHistory syncs adminOpen on popstate.
import { create } from "zustand";
import { useLayoutStore } from "../../layout/store.ts";

interface SettingsUIStore {
  settingsOpen: boolean;
  /** Initial tab when opened ("display" | "env" | "agents" | "git" | "ssm" | "tokens"). */
  settingsSection: string;
  adminOpen: boolean;
  openSettings(section?: string): void;
  closeSettings(): void;
  openAdmin(): void;
  closeAdmin(): void;
  /** Connections change tick (old connKey): bump after a connect/disconnect so
   * consumers (OnboardingCard, useConnections) refetch. */
  connTick: number;
  bumpConn(): void;
}

// adminDepth = drill-down depth (0=tenants, 1=tenant, 2=member). AdminTab pushes a
// history entry per level, so a browser "back" pops one level and only closes at the
// top; the X/backdrop pops all levels at once. A plain mutable ref (not store state —
// nothing renders from it).
export const adminDepthRef = { current: 0 };

const pushModalEntry = (modal: string) => {
  try {
    history.pushState({ __af: true, layout: useLayoutStore.getState().layout, modal }, "");
  } catch {}
};

export const useSettingsUI = create<SettingsUIStore>((set) => ({
  settingsOpen: false,
  settingsSection: "agents",
  adminOpen: false,

  openSettings(section?: string) {
    // "connections" is a legacy alias for the merged エージェント tab (the old 接続
    // tab was folded into it), so any caller asking for connections lands there.
    set({ settingsSection: section === "connections" ? "agents" : section || "agents", settingsOpen: true });
  },
  closeSettings() {
    // Close-on-back is handled by ui/Modal (useBackClose); just drop the flag.
    set({ settingsOpen: false });
  },

  openAdmin() {
    adminDepthRef.current = 0;
    set({ adminOpen: true });
    pushModalEntry("admin");
  },
  closeAdmin() {
    // Full close (X / backdrop): pop ALL admin entries (base + each drill level) so
    // one action closes the modal from any depth and a later back can't re-open it.
    if (typeof history !== "undefined" && history.state && history.state.modal === "admin") {
      history.go(-(adminDepthRef.current + 1));
    } else set({ adminOpen: false });
  },

  connTick: 0,
  bumpConn: () => set((s) => ({ connTick: s.connTick + 1 })),
}));

/** Browser back/forward closes (or re-opens) the ADMIN modal — the layout part of
 * the entry is handled by wireLayoutHistory; this syncs only the admin flag. (The
 * settings modal manages its own history through ui/Modal's useBackClose, so it is
 * deliberately NOT synced here.) Wired once from App boot; returns the cleanup
 * (StrictMode-safe). */
export function wireSettingsHistory(): () => void {
  const onPop = (e: PopStateEvent) => {
    useSettingsUI.setState({ adminOpen: !!(e.state && e.state.modal === "admin") });
  };
  window.addEventListener("popstate", onPop);
  return () => window.removeEventListener("popstate", onPop);
}
