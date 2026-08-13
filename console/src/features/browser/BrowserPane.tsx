import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { useLayoutStore } from "../../layout/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Button, IconButton } from "../../ui/Button.tsx";
import type { BrowserSnapshot } from "./protocol.ts";
import { ensureBrowser } from "./service.ts";
import { browserTarget, targetFromURL } from "./target.ts";
import { BrowserConsoleDrawer, BrowserSurface } from "./BrowserSurface.tsx";
import { toast } from "../../ui/toast.ts";

interface BrowserPaneProps {
  paneId: string;
  port: number;
  path: string;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}

export function BrowserPane({ paneId, port, path, headerActions }: BrowserPaneProps) {
  const tr = useT();
  const setPaneTarget = useLayoutStore((state) => state.setPaneTarget);
  // Resolve on every render so a tenant-scope registry reset can replace a
  // same-named pane's disposed controller without persisting another identity.
  const controller = ensureBrowser(paneId, { port, path });
  const [snapshot, setSnapshot] = useState<BrowserSnapshot>(controller.snapshot);
  const [portDraft, setPortDraft] = useState(String(port));
  const [pathDraft, setPathDraft] = useState(path);
  // port / path を別々に検証するので赤枠も別々（port の失敗でパス欄だけ赤くなる誤誘導を避ける）。
  const [portError, setPortError] = useState(false);
  const [pathError, setPathError] = useState(false);
  const [consoleOpen, setConsoleOpen] = useState(false);

  const copySelection = async () => {
    if (await controller.copySelection()) toast(tr("browser.copied_selection"), { kind: "success" });
    else toast(tr("browser.copy_selection_empty"), { kind: "info" });
  };

  useEffect(() => {
    const target = browserTarget(port, path);
    if (target && (controller.target.port !== target.port || controller.target.path !== target.path)) controller.changeTarget(target);
    setPortDraft(String(port));
    setPathDraft(path);
  }, [controller, port, path]);

  useEffect(() => controller.subscribe((next) => {
    setSnapshot(next);
    const target = targetFromURL(next.url);
    if (!target || (target.port === controller.target.port && target.path === controller.target.path)) return;
    controller.adoptTarget(target);
    setPaneTarget(paneId, { content: { kind: "browser", ...target } });
  }), [controller, paneId, setPaneTarget]);

  const submitTarget = (event: FormEvent) => {
    event.preventDefault();
    const port = Number(portDraft);
    const portOk = !!browserTarget(port, "/");
    // パス単独の妥当性は既知の有効ポートで判定する（port が悪くてもパス欄を巻き込まない）。
    const pathOk = !!browserTarget(portOk ? port : 3000, pathDraft);
    setPortError(!portOk);
    setPathError(!pathOk);
    const target = portOk && pathOk ? browserTarget(port, pathDraft) : null;
    if (!target) return;
    controller.changeTarget(target);
    setPaneTarget(paneId, { content: { kind: "browser", ...target } });
  };

  const status = browserStatus(snapshot, tr);

  return (
    <div className="browser-pane">
      <form className="browser-toolbar" onSubmit={submitTarget}>
        <IconButton icon="arrow-left" className="browser-nav" label={tr("browser.back")} disabled={!snapshot.canBack} onClick={() => controller.history("back")} />
        <IconButton icon="arrow-right" className="browser-nav" label={tr("browser.forward")} disabled={!snapshot.canForward} onClick={() => controller.history("forward")} />
        <IconButton icon="refresh" label={tr("browser.reload")} onClick={() => controller.reload()} />
        <span className="browser-host">127.0.0.1:</span>
        <input
          className={"browser-port" + (portError ? " invalid" : "")}
          aria-label={tr("browser.port")}
          aria-invalid={portError}
          inputMode="numeric"
          value={portDraft}
          onChange={(event) => setPortDraft(event.target.value)}
        />
        <input
          className={"browser-path" + (pathError ? " invalid" : "")}
          aria-label={tr("browser.path")}
          aria-invalid={pathError}
          value={pathDraft}
          onChange={(event) => setPathDraft(event.target.value)}
        />
        <IconButton icon="copy" label={tr("browser.copy_selection")} onClick={() => void copySelection()} />
        <Button small variant="ghost" icon="terminal" className="browser-console-toggle" onClick={() => setConsoleOpen((open) => !open)}>
          {tr("browser.console")}{snapshot.console.length > 0 && <span className="browser-log-badge">{snapshot.console.length}</span>}
        </Button>
        <IconButton icon="debug-restart" label={tr("browser.reconnect")} onClick={() => void controller.reconnect()} />
        {headerActions && <span className="view-head-actions">{headerActions}</span>}
      </form>
      <BrowserSurface
        controller={controller}
        snapshot={snapshot}
        canvasLabel={tr("browser.canvas")}
        inputLabel={tr("browser.remote_input")}
        keyboardLabel={tr("browser.keyboard_toggle")}
      >
        {status && (
          <div className={"browser-state browser-state-" + snapshot.state}>
            <span>{status}</span>
            {(snapshot.state === "crashed" || snapshot.state === "disconnected" || snapshot.state === "target-unreachable") && (
              <Button small icon="debug-restart" onClick={() => void controller.reconnect()}>{tr("browser.reconnect")}</Button>
            )}
          </div>
        )}
        {consoleOpen && <BrowserConsoleDrawer
          entries={snapshot.console}
          emptyLabel={tr("browser.console_empty")}
          copyLabel={tr("browser.copy_console")}
          closeLabel={tr("common.close")}
          title={tr("browser.console")}
          onClose={() => setConsoleOpen(false)}
        />}
      </BrowserSurface>
    </div>
  );
}

function browserStatus(snapshot: BrowserSnapshot, tr: ReturnType<typeof useT>): string {
  if (snapshot.errorCode) {
    const known: Record<string, Parameters<typeof tr>[0]> = {
      workspace_stopped: "browser.workspace_stopped",
      workspace_starting: "browser.workspace_starting",
      browser_page_limit: "browser.page_limit",
      browser_protocol_mismatch: "browser.protocol_mismatch",
      browser_installing: "browser.installing",
    };
    const key = known[snapshot.errorCode];
    return key ? tr(key) : snapshot.errorMessage || tr("browser.disconnected");
  }
  if (snapshot.state === "loading") return tr("browser.loading");
  if (snapshot.state === "target-unreachable") return tr("browser.target_unreachable");
  if (snapshot.state === "crashed") return tr("browser.crashed");
  if (snapshot.state === "disconnected") return tr("browser.disconnected");
  return "";
}
