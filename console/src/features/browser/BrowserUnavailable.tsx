// BrowserUnavailable — what a browser pane shows on a workspace whose runtime offers no
// browser features (availability.ts). It replaces the pane outright, so no browser
// controller is created and nothing asks the Agent for a Chromium that cannot start; a pane
// restored from a saved layout explains itself instead of sitting on a broken canvas.
import type { ReactNode } from "react";
import { previewURL } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Button } from "../../ui/Button.tsx";

interface BrowserUnavailableProps {
  /** The runtime id from the workspace payload. */
  runtime: string;
  /** The page a browser pane was pointed at, offered in the lightweight preview instead.
   *  Absent for an attachment pane, which has no local page to open. */
  target?: { port: number; path: string };
  headerActions?: ReactNode;
}

export function BrowserUnavailable({ runtime, target, headerActions }: BrowserUnavailableProps) {
  const tr = useT();
  return (
    <div className="browser-pane browser-unavailable-pane">
      {headerActions && (
        <div className="browser-toolbar">
          <span className="view-head-actions">{headerActions}</span>
        </div>
      )}
      <div className="browser-unavailable" role="status">
        <strong>{tr("browser.unavailable.title")}</strong>
        <p>{tr("browser.unavailable.body", { runtime })}</p>
        {target && (
          <Button
            small
            icon="link-external"
            onClick={() => window.open(previewURL(target.port, target.path), "_blank", "noopener")}
          >
            {tr("browser.unavailable.open_light", { port: target.port })}
          </Button>
        )}
      </div>
    </div>
  );
}
