// Settings/Admin dialog UI store (zustand) — replaces the old God-context slice
// (settingsOpen/settingsSection/adminOpen + connKey).
//
// どのモーダルも close-on-back は共有の ui/Modal（useBackClose）が面倒を見るので、
// ここに独自の history エントリは無い（Modal 自身のガードの上にもう 1 つ積むと
// 二重になり、✕/Esc が閉じずに開き直す）。管理モーダルは全画面サーフェス＋独自の
// ドリルダウン history（tenants→tenant→member）だったため長らく例外だったが、
// レール化で ui/Modal に載ったので、その独自エントリ（openAdmin の pushState /
// adminDepthRef / wireSettingsHistory）は撤去した。ドリルの段は AdminTab が
// useBackClose の層として積む。
import { create } from "zustand";

// The settings modal remembers the last-opened section in localStorage, so reopening
// lands where you left off. First-ever open (nothing stored) defaults to 表示
// (個人設定 › display). Device-local UI state — deliberately NOT server-synced.
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
  /** テナント設定 modal（テナント管理者の面）。管理モーダル＝デプロイ全体、個人設定
   *  ＝自分、に対して「自分が管理しているテナント」の設定がここ。 */
  tenantOpen: boolean;
  /** 開くセクション（deep-link）。未指定なら既定の "signin"。 */
  tenantSection: string;
  /** はじめかたガイド modal (re-openable first-run checklist — GuideModal). */
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
    // "connections" is a legacy alias for the merged エージェント tab (the old 接続
    // tab was folded into it), so any caller asking for connections lands there.
    // No explicit section → restore the last-opened one (localStorage), else 表示.
    set({
      settingsSection: section === "connections" ? "agents" : section || lastSection() || "display",
      settingsOpen: true,
    });
  },
  closeSettings() {
    // Close-on-back is handled by ui/Modal (useBackClose); just drop the flag.
    set({ settingsOpen: false });
  },

  // 管理モーダルも個人設定と同じ ui/Modal に乗るので、戻るで閉じるのは useBackClose
  // が面倒を見る（ドリルの段は AdminTab がその上に積む）。
  openAdmin() {
    set({ adminOpen: true });
  },
  closeAdmin() {
    set({ adminOpen: false });
  },

  // テナント設定は個人設定と同じ ui/Modal に乗るので、戻るで閉じるのは useBackClose
  // が面倒を見る（管理モーダルのような独自 history エントリは持たない）。セクション
  // は呼び出し側の deep-link だけで決め、localStorage には覚えない（面が 2 つしか
  // なく、開くたびに「前回の続き」より「入口」が欲しい画面のため）。
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
