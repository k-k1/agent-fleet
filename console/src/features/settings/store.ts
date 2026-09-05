// Settings/Admin dialog UI store (zustand) — replaces the old God-context slice
// (settingsOpen/settingsSection/adminOpen + connKey).
//
// No modal here owns a history entry: close-on-back is handled by the shared ui/Modal
// (useBackClose). Stacking one more guard on top of Modal's own doubles it, and then ✕/Esc
// does not close but reopens. The admin modal's drill-down levels (tenants→tenant→member)
// are pushed by AdminTab as layers of useBackClose.
import { create } from "zustand";

// The settings modal remembers the last-opened section in localStorage, so reopening
// lands where you left off. First-ever open (nothing stored) defaults to the display
// section of the personal settings. Device-local UI state — deliberately NOT server-synced.
const SETTINGS_SECTION_KEY = "af-settings-section";
const lastSection = (): string => {
  try {
    return localStorage.getItem(SETTINGS_SECTION_KEY) || "";
  } catch {
    return "";
  }
};
export function rememberSettingsSection(section: string): void {
  try {
    localStorage.setItem(SETTINGS_SECTION_KEY, section);
  } catch {
    /* storage unavailable — non-fatal */
  }
}

interface SettingsUIStore {
  settingsOpen: boolean;
  /** Section to open: the caller's requested section, else the last-opened one
   * (localStorage), else "display". */
  settingsSection: string;
  adminOpen: boolean;
  /** Tenant settings modal (the tenant administrator's surface). The admin modal covers the
   *  whole deployment and personal settings cover yourself; this covers the tenant you
   *  administer. */
  tenantOpen: boolean;
  /** Section to open (deep-link). Defaults to "signin" when unspecified. */
  tenantSection: string;
  /** Getting-started guide modal (re-openable first-run checklist — GuideModal). */
  guideOpen: boolean;
  openSettings(section?: string): void;
  closeSettings(): void;
  openAdmin(): void;
  closeAdmin(): void;
  openTenantSettings(section?: string): void;
  closeTenantSettings(): void;
  openGuide(): void;
  closeGuide(): void;
  /** Connections change tick (old connKey): bump after a connect/disconnect so
   * consumers (OnboardingCard, useConnections) refetch. */
  connTick: number;
  bumpConn(): void;
}

export const useSettingsUI = create<SettingsUIStore>((set) => ({
  settingsOpen: false,
  settingsSection: lastSection() || "display",
  adminOpen: false,
  tenantOpen: false,
  tenantSection: "signin",
  guideOpen: false,

  openSettings(section?: string) {
    // "connections" is a legacy alias for the merged agents tab (the old connections
    // tab was folded into it), so any caller asking for connections lands there.
    // No explicit section → restore the last-opened one (localStorage), else display.
    set({
      settingsSection: section === "connections" ? "agents" : section || lastSection() || "display",
      settingsOpen: true,
    });
  },
  closeSettings() {
    // Close-on-back is handled by ui/Modal (useBackClose); just drop the flag.
    set({ settingsOpen: false });
  },

  // The admin modal rides on the same ui/Modal as the personal settings, so close-on-back is
  // handled by useBackClose (AdminTab stacks the drill-down levels on top of it).
  openAdmin() {
    set({ adminOpen: true });
  },
  closeAdmin() {
    set({ adminOpen: false });
  },

  // Tenant settings ride on the same ui/Modal as the personal settings, so close-on-back is
  // handled by useBackClose (no history entry of its own, unlike the admin modal). The section
  // comes only from the caller's deep-link and is not remembered in localStorage: there are
  // only two surfaces, and on each open you want the entrance rather than where you left off.
  openTenantSettings(section?: string) {
    set({ tenantSection: section || "signin", tenantOpen: true });
  },
  closeTenantSettings() {
    set({ tenantOpen: false });
  },

  // Close-on-back comes from ui/Modal (useBackClose), like the settings dialog.
  openGuide: () => set({ guideOpen: true }),
  closeGuide: () => set({ guideOpen: false }),

  connTick: 0,
  bumpConn: () => set((s) => ({ connTick: s.connTick + 1 })),
}));
