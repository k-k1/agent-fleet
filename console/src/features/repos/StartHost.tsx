// StartHost — app-level host for the はじめる hub (起動導線 Ph2). Watches the
// sessions store's startTick (WS bar はじめる / onboarding) and owns the hand-off
// from the hub to the per-repo 作業を始める dialog (existing copy picked, or a
// clone-and-continue), so both stages share the LaunchModal + useStartWork pair
// the repo rows already use.
import { useEffect, useRef, useState } from "react";
import { agentOf } from "../../agents/registry.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useRepoRailContext } from "./useRepoRail.ts";
import { useStartWork } from "./useStartWork.ts";
import { StartModal } from "./StartModal.tsx";
import { LaunchModal } from "./LaunchModal.tsx";
import type { Repo } from "./store.ts";

export function StartHost() {
  const startTick = useSessionsStore((s) => s.startTick);
  const [show, setShow] = useState(false);
  const [launch, setLaunch] = useState<Repo | null>(null);
  // Open the hub whenever the global tick changes (skip the mount value).
  const lastTickRef = useRef(startTick);
  useEffect(() => {
    if (startTick !== lastTickRef.current) {
      lastTickRef.current = startTick;
      setLaunch(null);
      setShow(true);
    }
  }, [startTick]);

  const ctx = useRepoRailContext(); // connection-gated kinds, like the repo rows
  const startWork = useStartWork();
  const agentKinds = ctx.launchKinds.filter((k) => agentOf(k).caps.chat);

  // The per-repo stage STACKS on the hub (like NewSessionModal → SsmLoginModal)
  // instead of replacing it: swapping modals in one commit trips useBackClose —
  // the outgoing modal's cleanup history.back() lands AFTER the incoming modal
  // pushed its guard entry and immediately closes it. Stacked, dismissing the
  // top stage (場所を変更 / Esc / ✕ / browser back) peels back to the hub; only
  // a successful launch closes the whole stack.
  return (
    <>
      {show && <StartModal kinds={agentKinds} onClose={() => setShow(false)} onPickRepo={setLaunch} />}
      {show && launch && (
        <LaunchModal
          repo={launch.name}
          branch={launch.branch}
          path={launch.path}
          kinds={agentKinds}
          allowWorktree={!launch.worktree}
          onClose={() => setLaunch(null)}
          onBack={() => setLaunch(null)}
          onLaunch={async (o) => {
            const r = await startWork({ dir: launch.path || "", repo: launch.name }, o);
            if (r.ok) setShow(false); // launched — drop the hub underneath too
            return r;
          }}
        />
      )}
    </>
  );
}
