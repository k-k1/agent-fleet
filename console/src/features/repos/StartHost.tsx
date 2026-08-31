// StartHost — app-level host for the はじめる hub (起動導線 Ph2). Watches the
// sessions store's startTick (WS bar はじめる / onboarding) and owns the hand-off
// from the hub to the per-repo 作業を始める dialog (existing copy picked, or a
// clone-and-continue), so both stages share the LaunchModal + useStartWork pair
// the repo rows already use. The launch target lives in a store (useLaunchTarget)
// so the clone-only toast's このまま はじめる (Ph3) can land here without the hub.
import { useEffect, useRef, useState } from "react";
import { agentOf } from "../../agents/registry.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useRepoRailContext } from "./useRepoRail.ts";
import { useStartWork } from "./useStartWork.ts";
import { StartModal } from "./StartModal.tsx";
import { LaunchModal } from "./LaunchModal.tsx";
import { useLaunchTarget, useLaunchSeed } from "./store.ts";
import { markHandoffLaunched } from "../mirror/HandoffProposal.tsx";
import { acceptHandoffOffer } from "../sharing/acceptHandoff.ts";
import { recordWorkItemLaunch } from "../workitems/launch.ts";

export function StartHost() {
  const startTick = useSessionsStore((s) => s.startTick);
  const [show, setShow] = useState(false);
  const launch = useLaunchTarget((s) => s.target);
  const launchExisting = useLaunchTarget((s) => s.existingBranch);
  const launchInPlace = useLaunchTarget((s) => s.inPlace);
  const openLaunch = useLaunchTarget((s) => s.open);
  const clearLaunch = useLaunchTarget((s) => s.clear);
  // First-prompt seed (memo send modal → 新規セッションを起動). Read once into the
  // LaunchModal below; cleared when the whole launch stack closes.
  const seedPrompt = useLaunchSeed((s) => s.prompt);
  const seedTitle = useLaunchSeed((s) => s.title);
  const seedHandoff = useLaunchSeed((s) => s.handoffSession);
  const seedHandoffId = useLaunchSeed((s) => s.handoffId);
  const seedOfferId = useLaunchSeed((s) => s.handoffOfferId);
  // 作業項目（docs/log/80）から来た起動。台帳への記帳は「起動できたあと」だけ。
  const seedWorkItem = useLaunchSeed((s) => s.workItem);
  const clearSeed = useLaunchSeed((s) => s.clear);
  // Open the hub whenever the global tick changes (skip the mount value).
  const lastTickRef = useRef(startTick);
  useEffect(() => {
    if (startTick !== lastTickRef.current) {
      lastTickRef.current = startTick;
      clearLaunch();
      setShow(true);
    }
  }, [startTick, clearLaunch]);

  const ctx = useRepoRailContext(); // connection-gated kinds, like the repo rows
  const startWork = useStartWork();
  // Coding agents only (runsInDir) — shell/ssm have their own rows in the hub.
  // Not caps.chat: agy is terminal-only (no chat mirror) but still launches here.
  const agentKinds = ctx.launchKinds.filter((k) => agentOf(k).caps.runsInDir);

  // The per-repo stage STACKS on the hub instead of replacing it: swapping
  // modals in one commit trips useBackClose — the outgoing modal's cleanup
  // history.back() lands AFTER the incoming modal pushed its guard entry and
  // immediately closes it. Stacked, dismissing the top stage (はじめる に戻る / Esc /
  // ✕ / browser back) peels back to the hub; only a successful launch closes
  // the whole stack. Via the clone-toast (hub closed) there is nothing to peel
  // to, so はじめる に戻る is offered only while the hub is open.
  return (
    <>
      {show && (
        <StartModal
          kinds={agentKinds}
          onClose={() => {
            setShow(false);
            clearSeed();
          }}
          onPickRepo={openLaunch}
        />
      )}
      {launch && (
        <LaunchModal
          repo={launch.name}
          branch={launch.branch}
          path={launch.path}
          kinds={agentKinds}
          settling={ctx.connsSettling}
          allowWorktree={!launch.worktree && launch.vcs !== "svn" && !launch.unborn}
          isSvn={launch.vcs === "svn"}
          isUnborn={!!launch.unborn}
          initialPrompt={seedPrompt || undefined}
          initialTitle={seedTitle || undefined}
          initialNewBranch={seedWorkItem?.branch || undefined}
          initialWorktree={launchInPlace ? false : undefined}
          initialExistingBranch={launchExisting || undefined}
          onClose={() => {
            clearLaunch();
            clearSeed();
          }}
          onBack={show ? clearLaunch : undefined}
          onLaunch={async (o) => {
            const r = await startWork({ dir: launch.path || "", repo: launch.name }, o);
            if (r.ok) {
              // Seeded by a handoff proposal: badge it 起動済み now that a session
              // really exists. The proposal itself stays — discarding is the user's call.
              if (seedHandoff && seedHandoffId) void markHandoffLaunched(seedHandoff, seedHandoffId);
              // メンバーから受け取った引き継ぎ（docs/log/77）は、起動できた**あと**に受諾を
              // 申告する。ここが唯一「本当にセッションができた」と分かる地点である。
              if (seedOfferId) void acceptHandoffOffer(seedOfferId, r.name || "");
              // 作業項目の台帳（docs/log/80 §80.8）。ここが「本当にセッションができた」と
              // 分かる唯一の地点で、次の人に「着手済み」と見せられるようになる。
              if (seedWorkItem && r.name) {
                // 既存の作業コピーで始めたときは新しいブランチができないので、その
                // コピーが今いるブランチを記録する（報告の下書きが空のブランチ行を
                // 出さないため）。
                void recordWorkItemLaunch(seedWorkItem, r.name, launch.name, o.newBranch || launch.branch || "");
              }
              setShow(false); // launched — drop the hub underneath too
              clearSeed();
            }
            return r;
          }}
        />
      )}
    </>
  );
}
