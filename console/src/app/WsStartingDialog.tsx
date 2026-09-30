// WsStartingDialog — a "workspace is starting" progress dialog shown while the
// workspace is coming up (docs/log/35 §35.9-9). It exists because a native rootfs
// FIRST start runs the entrypoint boot-install (pinned agent CLIs, minutes) whose
// output only ever went to agent.log — so the operator saw a long silent wait and
// could wrongly conclude nothing was happening / the CLIs were baked in. The CP
// now surfaces the latest boot phase on GET /api/workspace (bootPhase), and the
// workspace store polls it during the start window; this dialog renders it live.
//
// It is dismissable (Esc / backdrop / close) — the start keeps running server-side
// regardless — and re-opens for the next start window.
import { useEffect, useRef, useState } from "react";
import { Modal } from "../ui/Modal.tsx";
import { Icon } from "../ui/Icon.tsx";
import { useT } from "../lib/i18n/index.ts";
import { useWorkspaceStore, wsPreparing } from "../core/store/workspace.ts";
import { phaseKey } from "../lib/bootPhase.ts";

export function WsStartingDialog() {
  const tr = useT();
  const state = useWorkspaceStore((s) => s.state);
  const bootPhase = useWorkspaceStore((s) => s.bootPhase);
  const preparing = wsPreparing(state, bootPhase);

  // Dismissable, but re-open for each new start window: reset the dismissal on the
  // false→true edge of `preparing`.
  const [dismissed, setDismissed] = useState(false);
  const prev = useRef(false);
  useEffect(() => {
    if (preparing && !prev.current) setDismissed(false);
    prev.current = preparing;
  }, [preparing]);

  if (!preparing || dismissed) return null;

  const headline = bootPhase ? tr(phaseKey(bootPhase)) : tr("wsstart.generic");

  return (
    <Modal title={tr("wsstart.title")} onClose={() => setDismissed(true)} className="ws-starting">
      {/* Wrap the body in the shared ui-modal-body: ui-modal itself has no padding (the
          heading carries its own), so a child placed directly inside sticks to the frame —
          the progress line and the `slot: …` code box touched the left and right edges. */}
      <div className="ui-modal-body">
        <div className="ws-starting-line">
          <Icon name="loading" spin />
          <span>{headline}</span>
        </div>
        {bootPhase && <div className="ws-starting-phase">{bootPhase}</div>}
        <div className="ws-starting-hint">{tr("wsstart.hint")}</div>
      </div>
    </Modal>
  );
}
