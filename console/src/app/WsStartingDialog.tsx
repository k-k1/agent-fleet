// WsStartingDialog — a "workspace is starting" progress dialog shown while the
// workspace is coming up (docs/35 §35.9-9). It exists because a native rootfs
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
import type { MsgKey } from "../lib/i18n/index.ts";

// Map a raw entrypoint boot phase ("boot-install (pinned): …", "boot-install rtk …",
// "install-go 1.26.4", …) to a friendly localized line. The raw phase is still shown
// beneath as a technical detail, so an unmapped phase is fine.
export function phaseKey(phase: string): MsgKey {
  const p = phase.toLowerCase();
  if (p.startsWith("boot-install rtk") || p.startsWith("boot-install agy")) return "wsstart.fetching_tool";
  if (p.startsWith("install-go") || p.startsWith("install-jdk")) return "wsstart.toolchain";
  if (p.startsWith("boot-install") || p.startsWith("lean variant")) return "wsstart.installing_clis";
  // EC2 pool runtime (ADR 0045): the first minutes are infrastructure, not CLIs — a
  // new slot, a new/restored home disk, an SSM mount. Saying "installing agent CLIs"
  // there names the wrong wait, which is what an operator judges "stuck" against.
  if (p.startsWith("slot: creating")) return "wsstart.slot_creating";
  if (p.startsWith("slot: waking")) return "wsstart.slot_waking";
  if (p.startsWith("slot: booting") || p.startsWith("slot: joining")) return "wsstart.slot_booting";
  if (p.startsWith("home: restoring")) return "wsstart.home_restoring";
  if (p.startsWith("home: creating")) return "wsstart.home_creating";
  if (p.startsWith("home: attaching") || p.startsWith("home: mounting")) return "wsstart.home_attaching";
  return "wsstart.generic";
}

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
      <div className="ws-starting-body">
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
