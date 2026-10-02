// The workspace's boot phase, as the CP reports it (GET /api/workspace bootPhase), named for
// people. Kept free of React so the starting dialog and the notification of a stopped start
// (features/notifications/wording.ts) word the same phase the same way.
import type { MsgKey } from "./i18n/index.ts";

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
  // An administrator reserved the old slot for replacement (#1473): a new one is launched and
  // the home moves over — the "takes longer" the WS-bar notice promised.
  if (p.startsWith("slot: renewing")) return "wsstart.slot_renewing";
  if (p.startsWith("slot: waking")) return "wsstart.slot_waking";
  if (p.startsWith("slot: booting") || p.startsWith("slot: joining")) return "wsstart.slot_booting";
  if (p.startsWith("home: restoring")) return "wsstart.home_restoring";
  if (p.startsWith("home: creating")) return "wsstart.home_creating";
  if (p.startsWith("home: attaching") || p.startsWith("home: mounting")) return "wsstart.home_attaching";
  // A Recreate or Clean home on the pool removes the files here, after the mount, and a
  // large home can take minutes (ADR 0045 decision 32).
  if (p.startsWith("home: clearing")) return "wsstart.home_clearing";
  // Not a phase of a start that is progressing — a start that is NOT going to finish.
  // The CP sets this when ECS says it cannot place the task (docs/log/70 §70.14.6): it
  // stays `starting` until somebody changes something or the CP's start deadline stops the
  // workspace. The raw ECS
  // sentence printed below the headline is the useful half — it names the constraint.
  if (p.startsWith("blocked:")) return "wsstart.blocked";
  return "wsstart.generic";
}

