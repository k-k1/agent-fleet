// Command + group DATA for the keyboard system. The app-binding layer (imports
// stores), so it lives under features/, not lib/ (which stays pure). Types come from
// lib/keys/registry.ts.
//
// Each command may carry direct-accelerator `keys` (e.g. Alt+1) and/or a leader `seq`
// (e.g. "p r" = leader → p → r). The which-key overlay and command palette both render
// from this list. User-facing strings are Japanese literals for now; when
// console-i18n's lib/i18n lands on main, route them through it (one central place).
//
// Scope note: only GLOBALLY-invocable actions live here (pane/layout, workspace,
// new-session hub). Row/pane-target actions (git stage, session archive, open a
// specific chat) become keyboard-invocable in P3 when rails/menus get roving focus;
// until then the palette surfaces what exists.
import type { Command, Group, Region } from "../../lib/keys/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { paneByOrdinal, neighborPane, cyclePane } from "../../layout/nav.ts";
import { PANE_ORD_COUNT } from "../../layout/badges.ts";
import type { Dir } from "../../layout/nav.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import { getSettings, setSetting } from "../../lib/settings.ts";
import { useKeysStore } from "./store.ts";
import { focusPaneContent, focusRegion } from "./focus.ts";

const getLayout = () => useLayoutStore.getState().layout;
const layoutStore = () => useLayoutStore.getState();

/** Activate a pane by id (no-op if undefined), mark the region "main", and move
 * keyboard focus to its content once React has committed the new active pane. */
function focusPane(id: string | undefined): void {
  if (!id) return;
  layoutStore().setActive(id);
  useKeysStore.getState().setRegion("main");
  requestAnimationFrame(() => focusPaneContent(id));
}
const focusDir = (dir: Dir) => focusPane(neighborPane(getLayout(), dir));

const REGION_CYCLE: Region[] = ["rail", "main", "bars"];
function cycleRegion(delta: number): void {
  const cur = useKeysStore.getState().activeRegion;
  const i = REGION_CYCLE.indexOf(cur);
  const next = REGION_CYCLE[(Math.max(0, i) + delta + REGION_CYCLE.length) % REGION_CYCLE.length];
  useKeysStore.getState().setRegion(next);
  focusRegion(next);
}

function toggleWrap(): void {
  const p = activePane(getLayout());
  if (!p) return;
  const on = p.wrap ?? getSettings().wrap;
  layoutStore().setPaneWrap(p.id, !on);
}
function toggleWorkspace(): void {
  const ws = useWorkspaceStore.getState();
  if (ws.state === "running") void ws.stop();
  else void ws.start();
}
function toggleFullscreen(): void {
  if (document.fullscreenElement) void document.exitFullscreen?.();
  else void document.documentElement.requestFullscreen?.().catch(() => {});
}
function toggleTheme(): void {
  setSetting("theme", getSettings().theme === "light" ? "dark" : "light");
}

// Leader groups (Leader → group key → action). Titles show in the which-key overlay.
export const GROUPS: Group[] = [
  { id: "p", title: "ペイン / レイアウト" },
  { id: "s", title: "セッション" },
  { id: "w", title: "ワークスペース" },
];

// Alt+1..8 → focus pane N (also under leader: p 1..8). Matches the visible ordinal chip.
const paneOrdinalCommands: Command[] = Array.from({ length: PANE_ORD_COUNT }, (_, i) => {
  const n = i + 1;
  return {
    id: `pane.focus.${n}`,
    title: `ペイン ${n} へフォーカス`,
    keys: [`alt+${n}`],
    seq: `p ${n}`,
    // Only claim the direct key when that pane exists — else Alt+N flows to the terminal.
    when: () => paneByOrdinal(getLayout(), n) != null,
    run: () => focusPane(paneByOrdinal(getLayout(), n)),
  };
});

export const ALL_COMMANDS: Command[] = [
  // ---- Pane / layout (leader p, + Alt accelerators) ----
  { id: "pane.splitRight", title: "右に分割", seq: "p r", run: () => layoutStore().splitRight() },
  { id: "pane.splitDown", title: "下に分割", seq: "p d", run: () => layoutStore().splitDown(getLayout().activeId) },
  { id: "pane.close", title: "ペインを閉じる", seq: "p w", run: () => layoutStore().closePane(getLayout().activeId) },
  { id: "pane.closeAll", title: "全ペインを閉じる", seq: "p a", run: () => layoutStore().resetToTerminal() },
  { id: "pane.wrap", title: "行の折り返しを切替", seq: "p \\", run: toggleWrap },
  { id: "pane.focusLeft", title: "左のペインへ", seq: "p h", run: () => focusDir("left") },
  { id: "pane.focusDown", title: "下のペインへ", seq: "p j", run: () => focusDir("down") },
  { id: "pane.focusUp", title: "上のペインへ", seq: "p k", run: () => focusDir("up") },
  { id: "pane.focusRight", title: "右のペインへ", seq: "p l", run: () => focusDir("right") },
  ...paneOrdinalCommands,
  { id: "pane.next", title: "次のペインへ", keys: ["alt+]"], seq: "p ]", run: () => focusPane(cyclePane(getLayout(), 1)) },
  { id: "pane.prev", title: "前のペインへ", keys: ["alt+["], seq: "p [", run: () => focusPane(cyclePane(getLayout(), -1)) },

  // ---- Region focus (direct only) ----
  { id: "region.next", title: "次の領域へ（レール / メイン / バー）", keys: ["f6"], run: () => cycleRegion(1) },
  { id: "region.prev", title: "前の領域へ", keys: ["shift+f6"], run: () => cycleRegion(-1) },

  // ---- Session (leader s) ----
  { id: "session.new", title: "新規セッション（起動）", seq: "s n", run: () => useSessionsStore.getState().openStart() },

  // ---- Workspace (leader w) ----
  { id: "workspace.toggle", title: "ワークスペース 起動 / 停止", seq: "w s", run: toggleWorkspace },
  { id: "workspace.toggleRail", title: "左レールの表示切替", keys: ["mod+b"], seq: "w b", run: () => useLeftRail.getState().toggle() },
  { id: "workspace.railMode", title: "左レールの表示モード切替", seq: "w m", run: () => useLeftRail.getState().toggleMode() },
  { id: "workspace.fullscreen", title: "アプリ全画面の切替", seq: "w f", run: toggleFullscreen },
  { id: "workspace.theme", title: "テーマ切替（ダーク / ライト）", seq: "w t", run: toggleTheme },

  // ---- Top-level leader actions ----
  { id: "settings.open", title: "設定を開く", seq: ",", run: () => useSettingsUI.getState().openSettings() },
  { id: "guide.open", title: "はじめかたガイド", seq: "w g", run: () => useSettingsUI.getState().openGuide() },
];
