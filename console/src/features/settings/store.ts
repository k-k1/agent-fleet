// Settings/Admin dialog UI store (zustand) — replaces the old God-context slice
// (settingsOpen/settingsSection/adminOpen + connKey).
//
// No modal here owns a history entry: close-on-back is handled by the shared ui/Modal
// (useBackClose). Stacking one more guard on top of Modal's own doubles it, and then ✕/Esc
// does not close but reopens. The admin modal's drill-down levels (tenants→tenant→member)
// are pushed by AdminTab as layers of useBackClose.
import { create } from "zustand";

// Each modal (personal / tenant / admin) remembers where it was left in localStorage, so
// reopening lands there. First-ever open (nothing stored) uses the modal's entrance: display for
// personal settings, sign-in for tenant settings, the tenant list for admin. Device-local UI
// state — deliberately NOT server-synced. Readers validate what comes back: a key from an older
// build, or a tenant that has since gone, falls back to the entrance.
const SETTINGS_SECTION_KEY = "af-settings-section";
const TENANT_SECTION_KEY = "af-tenant-section";
const TENANT_SLUG_KEY = "af-tenant-slug";
const ADMIN_PLACE_KEY = "af-admin-place";

function readKey(key: string): string {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}
function writeKey(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable — non-fatal */
  }
}

const lastSection = (): string => readKey(SETTINGS_SECTION_KEY);
export function rememberSettingsSection(section: string): void {
  writeKey(SETTINGS_SECTION_KEY, section);
}

const lastTenantSection = (): string => readKey(TENANT_SECTION_KEY);
export function rememberTenantSection(section: string): void {
  writeKey(TENANT_SECTION_KEY, section);
}
/** The tenant last picked in the tenant settings modal ("" = none stored). */
export const lastTenantSlug = (): string => readKey(TENANT_SLUG_KEY);
export function rememberTenantSlug(slug: string): void {
  writeKey(TENANT_SLUG_KEY, slug);
}

/** Where the admin modal was left: the root rail item, the open tenant (null = root) and the
 *  rail item inside that tenant. The member drill-down is transient and never stored. */
export interface AdminPlace {
  root: string;
  scope: string | null;
  scopeSection: string;
}
export const ADMIN_PLACE_DEFAULT: AdminPlace = { root: "tenants", scope: null, scopeSection: "limits" };
export function lastAdminPlace(): AdminPlace {
  try {
    const v = JSON.parse(readKey(ADMIN_PLACE_KEY) || "null");
    if (!v || typeof v !== "object") return ADMIN_PLACE_DEFAULT;
    const str = (x: unknown) => (typeof x === "string" ? x : "");
    return {
      root: str(v.root) || ADMIN_PLACE_DEFAULT.root,
      scope: str(v.scope) || null,
      scopeSection: str(v.scopeSection) || ADMIN_PLACE_DEFAULT.scopeSection,
    };
  } catch {
    return ADMIN_PLACE_DEFAULT;
  }
}
export function rememberAdminPlace(place: AdminPlace): void {
  writeKey(ADMIN_PLACE_KEY, JSON.stringify(place));
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
  /** Section to open: the caller's requested section (deep-link), else the last-opened one
   *  (localStorage), else "signin". */
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
  tenantSection: lastTenantSection() || "signin",
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
  // handled by useBackClose (AdminTab stacks the drill-down levels on top of it). Where it was
  // left is restored by AdminTab itself (lastAdminPlace), since it has no deep-link to honour.
  openAdmin() {
    set({ adminOpen: true });
  },
  closeAdmin() {
    set({ adminOpen: false });
  },

  // Tenant settings ride on the same ui/Modal as the personal settings, so close-on-back is
  // handled by useBackClose. Like personal settings, no explicit section → restore the
  // last-opened one (localStorage), else sign-in (#1100).
  openTenantSettings(section?: string) {
    set({ tenantSection: section || lastTenantSection() || "signin", tenantOpen: true });
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
