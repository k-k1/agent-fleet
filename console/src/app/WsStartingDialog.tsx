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

// Map a raw phase ("boot-install (pinned): …", "install-go 1.26.4", "slot: creating",
// …) to a friendly localized line. The raw phase is still shown beneath as a technical
// detail, so an unmapped phase is fine.
//
// The fallback names no cause. It used to say the first start installs agent CLIs,
// which is a guess: it is wrong on every restart (nothing is installed), and on the EC2
// pool it was wrong about which wait the user was in. Where the cause IS known a phase
// says so; where it is not, "starting" is the whole truth.
export function phaseKey(phase: string): MsgKey {
  const p = phase.toLowerCase();
  if (p.startsWith("boot-install rtk") || p.startsWith("boot-install agy")) return "wsstart.fetching_tool";
  if (p.startsWith("install-go") || p.startsWith("install-jdk")) return "wsstart.toolchain";
  if (p.startsWith("boot-install") || p.startsWith("lean variant")) return "wsstart.installing_clis";
  // EC2 pool runtime (ADR 0045): the first minutes are infrastructure, not CLIs — a
  // new slot, a new/restored home disk, an SSM mount. Saying "installing agent CLIs"
  // there names the wrong wait, which is what an operator judges "stuck" against.
  // The slowest path there is, and the one most likely to be judged "stuck": the pool is
  // at its cap holding only boxes of a size this member cannot run on, so one is being
  // taken out before theirs can be built. Falling through to the generic "starting" here
  // would name no cause for the longest wait the product has.
  if (p.startsWith("slot: making room")) return "wsstart.slot_making_room";
  if (p.startsWith("slot: creating")) return "wsstart.slot_creating";
  if (p.startsWith("slot: waking")) return "wsstart.slot_waking";
  if (p.startsWith("slot: booting") || p.startsWith("slot: joining")) return "wsstart.slot_booting";
  if (p.startsWith("home: restoring")) return "wsstart.home_restoring";
  if (p.startsWith("home: creating")) return "wsstart.home_creating";
  if (p.startsWith("home: attaching") || p.startsWith("home: mounting")) return "wsstart.home_attaching";
  // Not a phase of a start that is progressing — a start that is NOT going to finish.
  // The CP sets this when ECS says it cannot place the task (docs/70 §70.14.6), which
  // has no timeout: it stays `starting` until somebody changes something. The raw ECS
  // sentence printed below the headline is the useful half — it names the constraint.
  if (p.startsWith("blocked:")) return "wsstart.blocked";
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
      {/* ★ 共有の ui-modal-body に載せる。ui-modal 自身に padding は無く（見出しが自分で
          持つ形）、直に子を置くと本文だけが枠に貼りつく —— 進捗の 1 行と `slot: …` の
          コード枠が左右の縁に密着していた。 */}
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
