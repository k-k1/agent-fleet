// Command + group DATA for the keyboard system. The app-binding layer (imports
// stores), so it lives under features/, not lib/ (which stays pure). Types come from
// lib/keys/registry.ts.
//
// Each command may carry direct-accelerator `keys` (e.g. Alt+1) and/or a leader `seq`
// (e.g. "p r" = leader → p → r). The which-key overlay and command palette both render
// from this list. `title` is an i18n message key (lib/i18n, docs/28) — resolve for display
// with cmdLabel() and search across all locales with cmdSearch() (labels.ts), so the
// command palette matches whether the user types Japanese or English.
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
import { useMemoStore } from "../memo/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import { getSettings, setSetting } from "../../lib/settings.ts";
import { useKeysStore } from "./store.ts";
import { useUiOpen } from "../../core/store/uiOpen.ts";
import { toggleTtsPlayback } from "../../core/store/tts.ts";
import { paneViewActions } from "../viewer/paneViewActions.ts";
import { langFor, imageFormat } from "../../lib/filemeta.ts";
import { focusPaneContent, focusRegion } from "./focus.ts";
import { api, apiJSON } from "../../core/api/client.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";

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
// Cycle the active Markdown file's preview/source (Marp: slides too) toggle. Drives
// FileView's local mode via the pane-view action registry; no-ops on non-Markdown.
function toggleMarkdownMode(): void {
  const p = activePane(getLayout());
  if (p) paneViewActions(p.id)?.toggleMdMode?.();
}
// A markdown file is showing in the active pane (gates the preview/source toggle).
function activeIsMarkdown(): boolean {
  const c = activePane(getLayout())?.content;
  return !!c && c.kind === "file" && langFor(c.filePath) === "markdown";
}
// The active pane can switch between the normal file view and the read-aloud view
// (docs/24) — a text file, or one already in the reader.
function activeCanRead(): boolean {
  const c = activePane(getLayout())?.content;
  if (!c) return false;
  if (c.kind === "read") return true;
  return c.kind === "file" && !imageFormat(c.filePath);
}
// Toggle the active pane in place between the file view and the read-aloud view,
// keeping the same file (docs/24). Replaces the active pane's content (openTarget).
function toggleReader(): void {
  const c = activePane(getLayout())?.content;
  if (!c) return;
  if (c.kind === "read") layoutStore().openTarget({ content: { kind: "file", filePath: c.filePath } });
  else if (c.kind === "file") layoutStore().openTarget({ content: { kind: "read", filePath: c.filePath } });
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

// Toggle a chat-bridge service's notification master (Discord/Slack) from the keyboard —
// same effect as the OnOff in Settings › Notifications. There's no always-mounted store for
// the connection, so fetch it live to learn the current state, then PUT the flipped
// notifyOff, resending the other fields the backend PUT would otherwise overwrite (mirrors
// NotificationsTab.setNotify). No-ops with a hint when the service isn't connected.
async function toggleChatNotify(kind: "discord" | "slack"): Promise<void> {
  const name = kind === "slack" ? "Slack" : "Discord";
  const conns = await api("api/connections");
  const st = conns && !conns.error ? conns[kind] : null;
  if (!st?.connected) {
    toast(t("keys.toast.notifyNoConn", { name }), { kind: "info" });
    return;
  }
  const wasOn = st.notify !== false; // notifications currently enabled?
  const res = await apiJSON(`api/connections/${kind}`, "PUT", {
    // token + destination omitted → the backend reuses the stored connection.
    events: Array.isArray(st.events) ? st.events : [],
    threads: !!st.threads,
    receive: !!st.receive,
    fullText: !!st.fullText,
    mirrorInput: st.mirrorInput !== false,
    mentionUserId: st.mentionUserId || "",
    notifyOff: wasOn, // flip: on → off, off → on
  });
  if (res && res.error) {
    toast(t("common.save_failed_msg", { msg: String(res.error.message || res.error) }));
    return;
  }
  // Toast the resulting state so a keypress with no ambient indicator still tells the user
  // whether notifications are now on or off.
  toast(t(wasOn ? "keys.toast.notifyOff" : "keys.toast.notifyOn", { name }), { kind: "success" });
}

// Leader groups (Leader → group key → action). `title` is an i18n key (resolved for
// display by cmdLabel; see labels.ts). Titles show in the which-key overlay.
export const GROUPS: Group[] = [
  { id: "p", title: "keys.grp.pane" },
  { id: "s", title: "keys.grp.session" },
  { id: "w", title: "keys.grp.workspace" },
  { id: "g", title: "keys.grp.open" },
  { id: "n", title: "keys.grp.notify" },
  { id: "v", title: "keys.grp.view" },
];

// Alt+1..8 → focus pane N (also under leader: p 1..8). Matches the visible ordinal chip.
// title carries {n} via the "|n=" vars suffix labels.ts understands.
const paneOrdinalCommands: Command[] = Array.from({ length: PANE_ORD_COUNT }, (_, i) => {
  const n = i + 1;
  return {
    id: `pane.focus.${n}`,
    title: `keys.cmd.paneFocus|n=${n}`,
    keys: [`alt+${n}`],
    seq: `p ${n}`,
    // Only claim the direct key when that pane exists — else Alt+N flows to the terminal.
    when: () => paneByOrdinal(getLayout(), n) != null,
    run: () => focusPane(paneByOrdinal(getLayout(), n)),
  };
});

export const ALL_COMMANDS: Command[] = [
  // ---- Pane / layout (leader p, + Alt accelerators) ----
  { id: "pane.splitRight", title: "keys.cmd.splitRight", seq: "p r", run: () => layoutStore().splitRight() },
  { id: "pane.splitDown", title: "keys.cmd.splitDown", seq: "p d", run: () => layoutStore().splitDown(getLayout().activeId) },
  { id: "pane.close", title: "keys.cmd.close", seq: "p w", run: () => layoutStore().closePane(getLayout().activeId) },
  { id: "pane.closeAll", title: "keys.cmd.closeAll", seq: "p a", run: () => layoutStore().resetToTerminal() },
  { id: "pane.wrap", title: "keys.cmd.wrap", seq: "p \\", run: toggleWrap },
  { id: "pane.focusLeft", title: "keys.cmd.focusLeft", seq: "p h", run: () => focusDir("left") },
  { id: "pane.focusDown", title: "keys.cmd.focusDown", seq: "p j", run: () => focusDir("down") },
  { id: "pane.focusUp", title: "keys.cmd.focusUp", seq: "p k", run: () => focusDir("up") },
  { id: "pane.focusRight", title: "keys.cmd.focusRight", seq: "p l", run: () => focusDir("right") },
  ...paneOrdinalCommands,
  { id: "pane.next", title: "keys.cmd.next", keys: ["alt+]"], seq: "p ]", run: () => focusPane(cyclePane(getLayout(), 1)) },
  { id: "pane.prev", title: "keys.cmd.prev", keys: ["alt+["], seq: "p [", run: () => focusPane(cyclePane(getLayout(), -1)) },

  // ---- Region focus (direct only) ----
  { id: "region.next", title: "keys.cmd.regionNext", keys: ["f6"], run: () => cycleRegion(1) },
  { id: "region.prev", title: "keys.cmd.regionPrev", keys: ["shift+f6"], run: () => cycleRegion(-1) },

  // ---- Session (leader s) ----
  { id: "session.new", title: "keys.cmd.sessionNew", seq: "s n", run: () => useSessionsStore.getState().openStart() },

  // ---- Memo (leader m = memo) ----
  // The most-used quick action gets the top-level single key "m" (m = memo). The leader "n"
  // group is now notifications (mute + Slack/Discord), so memo no longer needs its own group.
  {
    id: "memo.add",
    title: "keys.cmd.memoAdd",
    seq: "m",
    run: () => {
      if (!useLeftRail.getState().open) useLeftRail.getState().toggle();
      useMemoStore.getState().requestCompose();
    },
  },

  // ---- Workspace (leader w) ----
  { id: "workspace.toggle", title: "keys.cmd.workspaceToggle", seq: "w s", run: toggleWorkspace },
  { id: "workspace.toggleRail", title: "keys.cmd.toggleRail", keys: ["mod+b"], seq: "w b", run: () => useLeftRail.getState().toggle() },
  { id: "workspace.railMode", title: "keys.cmd.railMode", seq: "w m", run: () => useLeftRail.getState().toggleMode() },
  { id: "workspace.fullscreen", title: "keys.cmd.fullscreen", seq: "w f", run: toggleFullscreen },
  { id: "workspace.theme", title: "keys.cmd.theme", seq: "w t", run: toggleTheme },

  // ---- Top-level leader actions ----
  { id: "settings.open", title: "keys.cmd.settingsOpen", seq: ",", run: () => useSettingsUI.getState().openSettings() },
  // Palette is normally on its own accelerator (Ctrl/⌘+P), but a leader path keeps it
  // reachable when terminal-input priority suppresses that accelerator in the terminal —
  // the leader is the one chord that still escapes. Also makes it discoverable in which-key.
  { id: "palette.open", title: "keys.cmd.paletteOpen", seq: ";", run: () => useKeysStore.getState().openPalette() },
  { id: "guide.open", title: "keys.cmd.guideOpen", seq: "w g", run: () => useSettingsUI.getState().openGuide() },
  // The "?" cheat-sheet. Also opens on a bare "?" when not typing (handled directly in
  // the dispatcher so it stays out of the terminal/inputs); this leader entry makes it
  // discoverable in which-key.
  { id: "help.cheatsheet", title: "keys.cmd.cheatsheet", seq: "shift+/", run: () => useKeysStore.getState().openCheat() },

  // ---- Open status surfaces (leader g) — toggle the always-mounted popovers that own
  // their state via the uiOpen signal. Each no-ops gracefully when its chip is hidden
  // (e.g. an agent with no usage reading, or the resource tiles on mobile). ----
  { id: "open.notifications", title: "keys.cmd.openNotifications", seq: "g n", run: () => useUiOpen.getState().toggle("notifications") },
  { id: "open.usageClaude", title: "keys.cmd.openUsageClaude", seq: "g c", run: () => useUiOpen.getState().toggle("usage-claude") },
  { id: "open.usageCodex", title: "keys.cmd.openUsageCodex", seq: "g x", run: () => useUiOpen.getState().toggle("usage-codex") },
  { id: "open.usageAgy", title: "keys.cmd.openUsageAgy", seq: "g a", run: () => useUiOpen.getState().toggle("usage-agy") },
  { id: "open.resources", title: "keys.cmd.openResources", seq: "g r", run: () => useUiOpen.getState().toggle("resources") },

  // ---- Notifications (leader n) — mute the voice read-aloud, or toggle a chat-bridge
  // service's notification master. n m = mute (shares TopBar's stop+OFF logic); n s / n d
  // flip Slack / Discord notifications (same effect as Settings › Notifications). ----
  { id: "tts.toggle", title: "keys.cmd.ttsToggle", seq: "n m", run: toggleTtsPlayback },
  { id: "notify.slack", title: "keys.cmd.slackToggle", seq: "n s", run: () => void toggleChatNotify("slack") },
  { id: "notify.discord", title: "keys.cmd.discordToggle", seq: "n d", run: () => void toggleChatNotify("discord") },

  // ---- View / viewer (leader v, + Alt accelerators) — act on the active pane's
  // read-oriented view. Direct keys use Alt (not Ctrl): Ctrl+<letter> are the terminal's
  // control codes and the browser's own reserved chords (Ctrl+W/R/S/P…), so Alt is the safe
  // accelerator namespace here (matches Alt+1..8 / Alt+[ ] and VS Code's Alt+Z for wrap). ----
  // Markdown preview ⇄ source (Marp cycles slides too); only active on a Markdown file.
  { id: "viewer.mdMode", title: "keys.cmd.mdMode", keys: ["alt+m"], seq: "v p", when: activeIsMarkdown, run: toggleMarkdownMode },
  // 朗読モード（縦書き閲覧＋読み上げ）へ切替／解除。テキストファイルまたは朗読ビューで有効。
  { id: "viewer.reader", title: "keys.cmd.reader", keys: ["alt+r"], seq: "v r", when: activeCanRead, run: toggleReader },
  // Line-wrap toggle — the same action as `p \`, mirrored into the view group (so every
  // per-view toggle lives under one leader) with a direct Alt+Z (VS Code parity). Unified
  // across every text view.
  { id: "viewer.wrap", title: "keys.cmd.wrap", keys: ["alt+z"], seq: "v w", run: toggleWrap },
];
