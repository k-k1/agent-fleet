// Command + group DATA for the keyboard system. The app-binding layer (imports
// stores), so it lives under features/, not lib/ (which stays pure). Types come from
// lib/keys/registry.ts.
//
// Each command may carry direct-accelerator `keys` (e.g. Alt+1) and/or a leader `seq`
// (e.g. "p r" = leader → p → r). The which-key overlay and command palette both render
// from this list. `title` is an i18n message key (lib/i18n, docs/log/28) — resolve for display
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
import { paneByOrdinal, neighborPane, cyclePane, cycleTab } from "../../layout/nav.ts";
import { PANE_ORD_COUNT } from "../../layout/badges.ts";
import type { Dir } from "../../layout/nav.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useMemoStore } from "../memo/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import { getSettings, setSetting, defaultSetting } from "../../lib/settings.ts";
import { fontSettingFor, stepFontSize } from "../../lib/viewFont.ts";
import type { FontSetting } from "../../lib/viewFont.ts";
import { cycleActiveWorkingSet, workingSetList } from "../../lib/workingSetsStore.ts";
import { useKeysStore } from "./store.ts";
import { useUiOpen } from "../../core/store/uiOpen.ts";
import { toggleTtsPlayback } from "../../core/store/tts.ts";
import { paneViewActions } from "../viewer/paneViewActions.ts";
import { langFor, imageFormat, isDrawioFile } from "../../lib/filemeta.ts";
import { focusPaneContent, focusRegion, focusRailFilter } from "./focus.ts";
import { api, apiJSON } from "../../core/api/client.ts";
import { toast } from "../../ui/toast.ts";
import { t, getLocale } from "../../lib/i18n/index.ts";
import { popoutMode } from "../../lib/popoutMode.ts";
import { canPopout, openPanePopout } from "../panes/popout.ts";

const getLayout = () => useLayoutStore.getState().layout;
const layoutStore = () => useLayoutStore.getState();

// Minimal pop-out tabs run a reduced command set: pane splitting/navigation,
// rail and new-session commands are gated off (the tab has one pane and no
// rail by design — expand (「展開」) restores everything). In-pane commands stay.
const notMinimalPopout = () => popoutMode() !== "popout";
// The active pane can be torn off into its own tab (leader p t / p f).
const canPopoutActive = () => {
  const p = activePane(getLayout());
  return !!p && canPopout(p) && notMinimalPopout();
};
const popoutActive = (ui: "popout" | "full") => {
  const p = activePane(getLayout());
  if (p) openPanePopout(p, ui);
};

/** Activate a pane by id (no-op if undefined), mark the region "main", and move
 * keyboard focus to its content once React has committed the new active pane. */
function focusPane(id: string | undefined): void {
  if (!id) return;
  layoutStore().setActive(id);
  useKeysStore.getState().setRegion("main");
  requestAnimationFrame(() => focusPaneContent(id));
}
const focusDir = (dir: Dir) => focusPane(neighborPane(getLayout(), dir));

const tabbedMode = () => getLayout().mode === "tabs";

/** Close whatever is currently visible. What closes is the view (i.e. the tab), never the
 * cell: passing activeCellId in tabs mode sends `ops.closePane` down its closeCell branch
 * and every tab in that cell silently disappears, which also disagrees with the tab's own
 * close button (it passes a view id). Split mode has one view per cell, so going through
 * the view id still empties the pane as before. Only an empty cell with no view is closed
 * by cell id, folding the cell away. */
function closeActiveView(): void {
  const l = getLayout();
  const view = activePane(l);
  layoutStore().closePane(view ? view.id : l.activeCellId);
}

/** Select the tab `delta` steps away inside the active cell (tabs mode only) and put
 * keyboard focus on the newly shown content. The cell doesn't change, so focus targets
 * the active cell itself. */
function focusTab(delta: number): void {
  const id = cycleTab(getLayout(), delta);
  if (!id) return;
  layoutStore().selectTab(id);
  useKeysStore.getState().setRegion("main");
  const cellId = getLayout().activeCellId;
  requestAnimationFrame(() => focusPaneContent(cellId));
}
// Only claim Alt+PageUp/PageDown when there is more than one tab to cycle — otherwise
// the key flows to the terminal (same rule as Alt+N for a missing pane).
const canCycleTabs = () => notMinimalPopout() && cycleTab(getLayout(), 1) != null;

/** Focus the rail's filter field, opening a collapsed rail first and aiming on the frame
 * after it renders. */
function openRailFilter(): void {
  useLeftRail.getState().ensureOpen();
  requestAnimationFrame(focusRailFilter);
}
// ProjectTree, and with it the filter field, only renders while the workspace is running,
// so do not claim the key while it is stopped.
const canFilterRail = () => notMinimalPopout() && useWorkspaceStore.getState().state === "running";

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
// Walk the active Markdown file's display modes: edit, preview and (when the file
// can be edited) split, with the Marp renderer as the inner step so a deck reaches
// both of its previews (docs/log/44 §1.1). Drives FileView's local mode via the
// pane-view action registry; no-ops on non-Markdown and on the plain fallback.
function toggleMarkdownMode(): void {
  const p = activePane(getLayout());
  if (p) paneViewActions(p.id)?.toggleMdMode?.();
}
// A markdown file is showing in the active pane (gates the preview/source toggle).
// drawio diagrams use the same command to move between diagram and source (docs/log/65
// §65.4). The decision is made on the extension alone: `.xml`, which cannot be classified
// without reading the content, is deliberately out of the command's reach and is switched
// with the in-pane button instead.
function activeIsMarkdown(): boolean {
  const c = activePane(getLayout())?.content;
  if (!c || c.kind !== "file") return false;
  return langFor(c.filePath) === "markdown" || isDrawioFile(c.filePath);
}
// The active pane can switch between the normal file view and the read-aloud view
// (docs/log/24) — a text file, or one already in the reader.
function activeCanRead(): boolean {
  const c = activePane(getLayout())?.content;
  if (!c) return false;
  if (c.kind === "read") return true;
  // A diagram (.drawio) has no body to read aloud, so it is excluded like an image.
  return c.kind === "file" && !imageFormat(c.filePath) && !isDrawioFile(c.filePath);
}
// Toggle the active pane in place between the file view and the read-aloud view,
// keeping the same file (docs/log/24). Replaces the active pane's content (openTarget).
function toggleReader(): void {
  const c = activePane(getLayout())?.content;
  if (!c) return;
  if (c.kind === "read") layoutStore().openTarget({ content: { kind: "file", filePath: c.filePath } });
  else if (c.kind === "file") layoutStore().openTarget({ content: { kind: "read", filePath: c.filePath } });
}
// Font size bigger / smaller / back to default. The target is the setting for the surface
// the active pane belongs to (terminal = termSize, mirror/chat = chatSize, read-aloud =
// readerSize, any other viewer = viewerSize). Surfaces with no text layout (browser,
// image) return null, which closes `when` and lets the key pass through to the terminal.
// docs/log/29 §5.7.
const fontTarget = (): FontSetting | null => fontSettingFor(activePane(getLayout())?.content);
const bumpFont = (delta: number) => {
  const key = fontTarget();
  if (!key) return;
  setSetting(key, stepFontSize(getSettings()[key], delta));
};
const resetFont = () => {
  const key = fontTarget();
  if (key) setSetting(key, defaultSetting(key));
};

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

// Working sets (docs/log/52): cycle all -> each group -> all. The destination is toasted
// so the command still means something with the rail collapsed. With no group created,
// only that fact is reported.
function cycleWorkingSet(): void {
  if (workingSetList(getSettings()).length === 0) {
    toast(t("wset.none_hint"), { kind: "info" });
    return;
  }
  const next = cycleActiveWorkingSet();
  toast(t("keys.toast.wset", { name: next ? next.name : t("wset.all") }), { kind: "success" });
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
    // lang is treated as a non-pointer: omitting it resets the stored value to the
    // default (Japanese). Like the connection card, send the current locale every time,
    // since notification language follows the Console's display language.
    lang: getLocale(),
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
// The entry point `run` calls fire-and-forget. api() rejects when the network is down, so
// without this catch the failure stays an unhandled rejection and nothing is shown; a
// toast reports it instead.
const runChatNotify = (kind: "discord" | "slack") =>
  void toggleChatNotify(kind).catch(() => toast(t("err.network")));

// Toggle the per-session voice notification (docs/log/24) from the keyboard — the same setting
// as the session voice notification in Settings › Notifications. Toasts the resulting
// on/off state.
function toggleTtsSessionNotify(): void {
  const next = !getSettings().ttsSessionNotify;
  setSetting("ttsSessionNotify", next);
  toast(t(next ? "keys.toast.ttsSessionOn" : "keys.toast.ttsSessionOff"), { kind: "success" });
}

// Toggle the limit-reset voice notification (Settings › Notifications).
function toggleUsageResetNotify(): void {
  const next = !getSettings().usageResetNotify;
  setSetting("usageResetNotify", next);
  toast(t(next ? "keys.toast.usageResetOn" : "keys.toast.usageResetOff"), { kind: "success" });
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
    when: () => notMinimalPopout() && paneByOrdinal(getLayout(), n) != null,
    run: () => focusPane(paneByOrdinal(getLayout(), n)),
  };
});

export const ALL_COMMANDS: Command[] = [
  // ---- Pane / layout (leader p, + Alt accelerators) ----
  { id: "pane.splitRight", title: "keys.cmd.splitRight", seq: "p r", when: notMinimalPopout, run: () => layoutStore().splitRight() },
  { id: "pane.splitDown", title: "keys.cmd.splitDown", seq: "p d", when: notMinimalPopout, run: () => layoutStore().splitDown(getLayout().activeCellId) },
  // Alt+W closes the visible tab/pane, matching the browser convention for closing a tab.
  { id: "pane.close", title: "keys.cmd.close", keys: ["alt+w"], seq: "p w", run: closeActiveView },
  // A tabs-mode-only escalation: close the whole cell, i.e. every tab in that pane. In
  // split mode it would be the same as pane.close, so it is not offered there. The tab UI
  // has no button for folding a cell away, so this is the only way to reduce cells in tabs
  // mode.
  { id: "pane.closeCell", title: "keys.cmd.closeCell", seq: "p c", when: () => notMinimalPopout() && tabbedMode(), run: () => layoutStore().closePane(getLayout().activeCellId) },
  { id: "pane.closeAll", title: "keys.cmd.closeAll", keys: ["alt+shift+w"], seq: "p a", when: notMinimalPopout, run: () => layoutStore().resetToTerminal() },
  { id: "pane.wrap", title: "keys.cmd.wrap", seq: "p \\", run: toggleWrap },
  { id: "pane.popout", title: "keys.cmd.popout", seq: "p t", when: canPopoutActive, run: () => popoutActive("popout") },
  { id: "pane.popoutFull", title: "keys.cmd.popoutFull", seq: "p f", when: canPopoutActive, run: () => popoutActive("full") },
  { id: "pane.focusLeft", title: "keys.cmd.focusLeft", seq: "p h", when: notMinimalPopout, run: () => focusDir("left") },
  { id: "pane.focusDown", title: "keys.cmd.focusDown", seq: "p j", when: notMinimalPopout, run: () => focusDir("down") },
  { id: "pane.focusUp", title: "keys.cmd.focusUp", seq: "p k", when: notMinimalPopout, run: () => focusDir("up") },
  { id: "pane.focusRight", title: "keys.cmd.focusRight", seq: "p l", when: notMinimalPopout, run: () => focusDir("right") },
  ...paneOrdinalCommands,
  { id: "pane.next", title: "keys.cmd.next", keys: ["alt+]"], seq: "p ]", when: notMinimalPopout, run: () => focusPane(cyclePane(getLayout(), 1)) },
  { id: "pane.prev", title: "keys.cmd.prev", keys: ["alt+["], seq: "p [", when: notMinimalPopout, run: () => focusPane(cyclePane(getLayout(), -1)) },
  // Cycling tabs is a different axis from cycling panes (Alt+[ ]), so it gets its own keys.
  // Alt+PageUp/PageDown sits close to the browser's own tab switching (Ctrl+PageUp/Down)
  // and is reserved by no browser.
  { id: "tab.next", title: "keys.cmd.tabNext", keys: ["alt+pagedown"], seq: "p n", when: canCycleTabs, run: () => focusTab(1) },
  { id: "tab.prev", title: "keys.cmd.tabPrev", keys: ["alt+pageup"], seq: "p p", when: canCycleTabs, run: () => focusTab(-1) },

  // ---- Region focus (direct only) ----
  { id: "region.next", title: "keys.cmd.regionNext", keys: ["f6"], when: notMinimalPopout, run: () => cycleRegion(1) },
  { id: "region.prev", title: "keys.cmd.regionPrev", keys: ["shift+f6"], when: notMinimalPopout, run: () => cycleRegion(-1) },

  // ---- Session (leader s) ----
  { id: "session.new", title: "keys.cmd.sessionNew", keys: ["alt+n"], seq: "s n", when: notMinimalPopout, run: () => useSessionsStore.getState().openStart() },

  // ---- Memo (leader m = memo) ----
  // The most-used quick action gets the top-level single key "m" (m = memo). The leader "n"
  // group is now notifications (mute + Slack/Discord), so memo no longer needs its own group.
  {
    id: "memo.add",
    title: "keys.cmd.memoAdd",
    // Alt+M belongs to the Markdown view toggle, so the direct key is A (for Add).
    keys: ["alt+a"],
    seq: "m",
    when: notMinimalPopout,
    run: () => {
      if (!useLeftRail.getState().open) useLeftRail.getState().toggle();
      useMemoStore.getState().requestCompose();
    },
  },

  // ---- Workspace (leader w) ----
  { id: "workspace.toggle", title: "keys.cmd.workspaceToggle", seq: "w s", run: toggleWorkspace },
  { id: "workspace.toggleRail", title: "keys.cmd.toggleRail", keys: ["mod+b"], seq: "w b", when: notMinimalPopout, run: () => useLeftRail.getState().toggle() },
  { id: "workspace.railMode", title: "keys.cmd.railMode", seq: "w m", when: notMinimalPopout, run: () => useLeftRail.getState().toggleMode() },
  { id: "workspace.fullscreen", title: "keys.cmd.fullscreen", seq: "w f", run: toggleFullscreen },
  { id: "workspace.theme", title: "keys.cmd.theme", seq: "w t", run: toggleTheme },
  { id: "workspace.workingSet", title: "keys.cmd.wsetCycle", keys: ["alt+g"], seq: "w w", when: notMinimalPopout, run: cycleWorkingSet },
  // Jump to the rail's filter field, opening the rail first if it is collapsed.
  { id: "rail.filter", title: "keys.cmd.railFilter", keys: ["alt+/"], seq: "w /", when: canFilterRail, run: openRailFilter },

  // ---- Top-level leader actions ----
  // Alt+, is the equivalent of VS Code's Ctrl+,. Ctrl collides with the terminal's control
  // codes, so it lives on Alt.
  { id: "settings.open", title: "keys.cmd.settingsOpen", keys: ["alt+,"], seq: ",", run: () => useSettingsUI.getState().openSettings() },
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

  // ---- Notifications (leader n) — mute the voice read-aloud, toggle the per-session voice
  // notification / limit-reset notification, or toggle a chat-bridge service's notification
  // master. n m = mute (shares TopBar's stop+OFF logic); n a = session voice notification;
  // n r = limit-reset notification; n s / n d flip Slack / Discord notifications. All match
  // Settings › Notifications and toast their result. ----
  // Alt+Q (Quiet) silences an in-progress read-aloud at once; Alt+M belongs to Markdown.
  { id: "tts.toggle", title: "keys.cmd.ttsToggle", keys: ["alt+q"], seq: "n m", run: toggleTtsPlayback },
  { id: "notify.ttsSession", title: "keys.cmd.ttsSessionToggle", seq: "n a", run: toggleTtsSessionNotify },
  { id: "notify.usageReset", title: "keys.cmd.usageResetToggle", seq: "n r", run: toggleUsageResetNotify },
  { id: "notify.slack", title: "keys.cmd.slackToggle", seq: "n s", run: () => runChatNotify("slack") },
  { id: "notify.discord", title: "keys.cmd.discordToggle", seq: "n d", run: () => runChatNotify("discord") },

  // ---- View / viewer (leader v, + Alt accelerators) — act on the active pane's
  // read-oriented view. Direct keys use Alt (not Ctrl): Ctrl+<letter> are the terminal's
  // control codes and the browser's own reserved chords (Ctrl+W/R/S/P…), so Alt is the safe
  // accelerator namespace here (matches Alt+1..8 / Alt+[ ] and VS Code's Alt+Z for wrap). ----
  // Markdown preview ⇄ source (Marp cycles slides too); only active on a Markdown file.
  { id: "viewer.mdMode", title: "keys.cmd.mdMode", keys: ["alt+m"], seq: "v p", when: activeIsMarkdown, run: toggleMarkdownMode },
  // Enter or leave read-aloud mode (vertical reading plus speech). Active on a text file
  // or when already in the reader view.
  { id: "viewer.reader", title: "keys.cmd.reader", keys: ["alt+r"], seq: "v r", when: activeCanRead, run: toggleReader },
  // Line-wrap toggle — the same action as `p \`, mirrored into the view group (so every
  // per-view toggle lives under one leader) with a direct Alt+Z (VS Code parity). Unified
  // across every text view.
  { id: "viewer.wrap", title: "keys.cmd.wrap", keys: ["alt+z"], seq: "v w", run: toggleWrap },
  // Font size. On a US layout "+" is Shift+=, so both `alt+=` and `alt+shift+=` are bound
  // and either intent enlarges; the numpad is treated the same way. On a JIS layout `=` is
  // physically the `^` key, but the dispatcher's candidate order (e.key first, e.code as a
  // fallback) still lands on alt+=, the same path that makes Alt+[ ] work on JIS. Ctrl is
  // unusable here because the terminal passes it through to browser zoom (NO_GRAB in
  // terminal/term.ts).
  {
    id: "viewer.fontBigger",
    title: "keys.cmd.fontBigger",
    keys: ["alt+=", "alt+shift+=", "alt+numpadadd"],
    seq: "v =",
    when: () => fontTarget() != null,
    run: () => bumpFont(1),
  },
  {
    id: "viewer.fontSmaller",
    title: "keys.cmd.fontSmaller",
    keys: ["alt+-", "alt+numpadsubtract"],
    seq: "v -",
    when: () => fontTarget() != null,
    run: () => bumpFont(-1),
  },
  {
    id: "viewer.fontReset",
    title: "keys.cmd.fontReset",
    keys: ["alt+0"],
    seq: "v 0",
    when: () => fontTarget() != null,
    run: resetFont,
  },
];
