// The click-time half of the Markdown preview links (viewer/parts/mdPreviewLinks.ts). Loaded on
// demand from there; see the note on loadPreviewRuntime.
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { t } from "../../lib/i18n/index.ts";
import { openPort } from "./PortOpen.tsx";
import { currentPreviewInfo, usePreviewStore } from "./previewState.ts";
import { portTargets, type PortTarget } from "./portTargets.ts";

/** What a click does, decided now: the stores are read at click time and nothing is awaited before
 *  the window opens (popup blockers). A stopped workspace is reported instead of opening a dead tab. */
export function activate(port: number, path: string, inNew: boolean, onError: (message: string) => void) {
  const ws = useWorkspaceStore.getState();
  const running = ws.state === "running";
  if (!running) {
    onError(t("view.preview_link.not_running"));
    return;
  }
  const targets = portTargets({ port, running, browserUnavailable: ws.browserUnavailable, info: currentPreviewInfo() });
  if (!targets.def) return;
  // Ctrl/⌘/middle-click keeps the pane one gesture away, as on the chip; where the pane is
  // unavailable the same gesture opens the default.
  const target: PortTarget = inNew ? (targets.state.pane === "" ? "pane" : targets.def) : targets.def;
  openPort(target, port, path, inNew);
}

/** First preview link in a document and nothing held for this scope → read the preview state so
 *  the first click can already use the subdomain. */
export function ensurePreviewInfo() {
  if (useWorkspaceStore.getState().state === "running" && !currentPreviewInfo()) {
    void usePreviewStore.getState().refresh();
  }
}
