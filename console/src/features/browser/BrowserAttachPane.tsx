import { useEffect, useState } from "react";
import { errText } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Button, IconButton } from "../../ui/Button.tsx";
import { toast } from "../../ui/toast.ts";
import type { BrowserAttachmentSnapshot } from "./attachmentController.ts";
import { ensureBrowserAttachment } from "./attachmentService.ts";
import { BrowserConsoleDrawer, BrowserSurface } from "./BrowserSurface.tsx";

interface BrowserAttachPaneProps {
  paneId: string;
  attachmentId: string;
}

export function BrowserAttachPane({ paneId, attachmentId }: BrowserAttachPaneProps) {
  const tr = useT();
  const closePane = useLayoutStore((state) => state.closePane);
  const controller = ensureBrowserAttachment(paneId, attachmentId);
  const [snapshot, setSnapshot] = useState<BrowserAttachmentSnapshot>(controller.snapshot);
  const [consoleOpen, setConsoleOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  // Zoom-to-fit defaults ON for an ATTACHED page: it is somebody's desktop site,
  // not a responsive app being previewed, so a pane-narrow viewport just clips it.
  const [fit, setFit] = useState(controller.fit);

  const copySelection = async () => {
    if (await controller.copySelection()) toast(tr("browser.copied_selection"), { kind: "success" });
    else toast(tr("browser.copy_selection_empty"), { kind: "info" });
  };

  useEffect(() => controller.subscribe(setSnapshot), [controller]);

  const userControl = snapshot.controlMode === "user-control";
  const locked = snapshot.controlMode === "locked";
  const status = attachmentStatus(snapshot, tr);
  const origin = visibleOrigin(snapshot.url);
  const handoff = snapshot.handoff;

  const finish = async (result: "completed" | "cancelled") => {
    setBusy(true);
    try {
      await controller.finish(result);
    } catch (error) {
      toast(errText(error as { code?: string; message?: string }), { kind: "error" });
    } finally {
      setBusy(false);
    }
  };

  const detach = async () => {
    setBusy(true);
    try {
      await controller.detach();
      closePane(paneId, true);
    } catch (error) {
      toast(errText(error as { code?: string; message?: string }), { kind: "error" });
      setBusy(false);
    }
  };

  return (
    <div className="browser-pane browser-attach-pane">
      <div className="browser-toolbar browser-attach-toolbar">
        <IconButton
          icon="arrow-left"
          className="browser-nav"
          label={tr("browser.back")}
          disabled={!userControl || !snapshot.canBack}
          onClick={() => controller.history("back")}
        />
        <IconButton
          icon="arrow-right"
          className="browser-nav"
          label={tr("browser.forward")}
          disabled={!userControl || !snapshot.canForward}
          onClick={() => controller.history("forward")}
        />
        <IconButton icon="refresh" label={tr("browser.reload")} disabled={!userControl} onClick={() => controller.reload()} />
        <span className={`browser-attach-mode browser-attach-mode-${snapshot.controlMode}`}>
          {tr(`browser.attach.mode.${snapshot.controlMode}`)}
        </span>
        <span className="browser-attach-location" title={origin}>
          <strong>{snapshot.title || tr("pane.kind.browser_attach")}</strong>
          {origin && <span>{origin}</span>}
        </span>
        <IconButton icon="copy" label={tr("browser.copy_selection")} onClick={() => void copySelection()} />
        <IconButton
          icon={fit ? "zoom-out" : "screen-full"}
          label={fit ? tr("browser.zoom_fit_on") : tr("browser.zoom_fit_off")}
          onClick={() => {
            const next = !fit;
            setFit(next);
            controller.setFit(next);
          }}
        />
        <Button small variant="ghost" icon="terminal" className="browser-console-toggle" onClick={() => setConsoleOpen((open) => !open)}>
          {tr("browser.console")}{snapshot.console.length > 0 && <span className="browser-log-badge">{snapshot.console.length}</span>}
        </Button>
        <IconButton icon="debug-restart" label={tr("browser.reconnect")} disabled={locked || busy} onClick={() => void controller.reconnect()} />
        <Button
          small
          variant="ghost"
          icon="close"
          className="browser-attach-detach"
          title={tr("browser.attach.detach_tooltip")}
          disabled={busy}
          onClick={() => void detach()}
        >
          {tr("browser.attach.detach")}
        </Button>
      </div>

      {handoff && (
        <section className="browser-attach-handoff" aria-label={tr("browser.attach.request")}>
          <div className="browser-attach-message">
            {handoff.message || tr("browser.attach.default_message")}
            {userControl && <small>{tr("browser.attach.owner_paused_warning")}</small>}
          </div>
          {handoff.result === "pending" ? (
            <div className="browser-attach-actions">
              {handoff.allowCancel && (
                <Button small variant="ghost" disabled={busy} onClick={() => void finish("cancelled")}>
                  {tr("browser.attach.cancel")}
                </Button>
              )}
              <Button small variant="primary" disabled={busy} onClick={() => void finish("completed")}>
                {handoff.completionLabel || tr("browser.attach.complete")}
              </Button>
            </div>
          ) : (
            <span className={`browser-attach-result browser-attach-result-${handoff.result}`}>
              {tr(`browser.attach.result.${handoff.result}`)}
            </span>
          )}
        </section>
      )}

      <BrowserSurface
        controller={controller}
        snapshot={snapshot}
        canvasLabel={tr("browser.attach.canvas")}
        inputLabel={tr("browser.remote_input")}
        inputEnabled={userControl}
      >
        {status && (
          <div className={`browser-state browser-state-${snapshot.state}`}>
            <span>{status}</span>
            {(snapshot.state === "disconnected" || snapshot.state === "target-closed") && (
              <Button small icon="debug-restart" disabled={locked} onClick={() => void controller.reconnect()}>
                {tr("browser.reconnect")}
              </Button>
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

function visibleOrigin(url: string): string {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.origin : "";
  } catch {
    return "";
  }
}

function attachmentStatus(snapshot: BrowserAttachmentSnapshot, tr: ReturnType<typeof useT>): string {
  if (snapshot.state === "expired") return tr("browser.attach.expired");
  if (snapshot.state === "target-closed") return tr("browser.attach.target_closed");
  if (snapshot.state === "unsupported-target-url") return tr("browser.attach.unsupported_url");
  if (snapshot.state === "detached") return tr("browser.attach.detached");
  if (snapshot.controlMode === "locked") return tr("browser.attach.locked");
  if (snapshot.errorCode) return snapshot.errorMessage || tr("browser.attach.disconnected");
  if (snapshot.state === "loading") return tr("browser.loading");
  if (snapshot.state === "disconnected") return tr("browser.attach.disconnected");
  return "";
}
