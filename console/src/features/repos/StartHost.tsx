// StartHost — app-level host for the Start hub (launch flow Ph2). Watches the
// sessions store's startTick (WS bar start button / onboarding) and owns the hand-off
// from the hub to the per-repo start-work dialog (existing copy picked, or a
// clone-and-continue), so both stages share the LaunchModal + useStartWork pair
// the repo rows already use. The launch target lives in a store (useLaunchTarget)
// so the clone-only toast's "start from here" (Ph3) can land here without the hub.
import { useEffect, useRef, useState } from "react";
import { agentOf } from "../../agents/registry.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useRepoRailContext } from "./useRepoRail.ts";
import { useStartWork, type HandoffTake } from "./useStartWork.ts";
import { StartModal } from "./StartModal.tsx";
import { branchItemOf } from "./branchRule.ts";
import { LaunchModal } from "./LaunchModal.tsx";
import { useLaunchTarget, useLaunchSeed } from "./store.ts";
import { markHandoffLaunched } from "../mirror/HandoffProposal.tsx";
import { launchFromHandoffOffer } from "../sharing/acceptHandoff.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { recordWorkItemLaunch } from "../workitems/launch.ts";
import { workItemLaunchBranch } from "../workitems/read.ts";
import { stashStoredDraft } from "../memo/launchStash.ts";
import { t } from "../../lib/i18n/index.ts";

export function StartHost() {
  const startTick = useSessionsStore((s) => s.startTick);
  const [show, setShow] = useState(false);
  const launch = useLaunchTarget((s) => s.target);
  const launchExisting = useLaunchTarget((s) => s.existingBranch);
  const launchInPlace = useLaunchTarget((s) => s.inPlace);
  const openLaunch = useLaunchTarget((s) => s.open);
  const clearLaunch = useLaunchTarget((s) => s.clear);
  // First-prompt seed (memo send modal -> launch a new session). Read once into the
  // LaunchModal below; cleared when the whole launch stack closes.
  const seedPrompt = useLaunchSeed((s) => s.prompt);
  const seedTitle = useLaunchSeed((s) => s.title);
  const seedAttachments = useLaunchSeed((s) => s.attachments);
  const seedHandoff = useLaunchSeed((s) => s.handoffSession);
  const seedHandoffId = useLaunchSeed((s) => s.handoffId);
  const seedOfferId = useLaunchSeed((s) => s.handoffOfferId);
  // A launch that came from a work item (docs/log/80). The ledger is written only AFTER the
  // launch succeeded.
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

  const toast = useToast();
  // Hub picks that restore a memo await a draft save before opening (onPickRepo below).
  // picking stops a second click from saving twice; hubGen tells a save that finished
  // after the hub closed not to reopen it.
  const picking = useRef(false);
  const hubGen = useRef(0);
  const ctx = useRepoRailContext(); // connection-gated kinds, like the repo rows
  const startWork = useStartWork();
  // Coding agents only (runsInDir) — shell/ssm have their own rows in the hub.
  // Not caps.chat: agy is terminal-only (no chat mirror) but still launches here.
  const agentKinds = ctx.launchKinds.filter((k) => agentOf(k).caps.runsInDir);

  // The per-repo stage STACKS on the hub instead of replacing it: swapping
  // modals in one commit trips useBackClose — the outgoing modal's cleanup
  // history.back() lands AFTER the incoming modal pushed its guard entry and
  // immediately closes it. Stacked, dismissing the top stage (back to the hub / Esc /
  // ✕ / browser back) peels back to the hub; only a successful launch closes
  // the whole stack. Via the clone-toast (hub closed) there is nothing to peel
  // to, so the back-to-hub action is offered only while the hub is open.
  return (
    <>
      {show && (
        <StartModal
          kinds={agentKinds}
          claudeExpired={ctx.claudeExpired}
          onClose={() => {
            hubGen.current++;
            setShow(false);
            clearSeed();
          }}
          onPickRepo={async (r) => {
            // A memo being restored (#3289) outranks the picked repo's stored draft: set that
            // draft aside first, and open nothing when it could not be saved.
            const { memoId, prompt, attachments } = useLaunchSeed.getState();
            if (!memoId) return openLaunch(r);
            if (picking.current) return; // one save at a time; a second click would save it twice
            picking.current = true;
            const gen = hubGen.current;
            const s = await stashStoredDraft(r.name, { body: prompt, attachments }).finally(() => {
              picking.current = false;
            });
            // The hub was closed (or another memo seeded) while saving: do not reopen anything.
            if (gen !== hubGen.current || useLaunchSeed.getState().memoId !== memoId) return;
            if (!s.ok) {
              toast(t(s.reason === "changed" ? "memo.start_draft_changed" : "memo.start_draft_failed"));
              return;
            }
            if (s.saved) toast(t("memo.start_draft_saved"), { kind: "success" });
            openLaunch(r);
          }}
        />
      )}
      {launch && (
        <LaunchModal
          repo={launch.name}
          branch={launch.branch}
          path={launch.path}
          kinds={agentKinds}
          claudeExpired={ctx.claudeExpired}
          settling={ctx.connsSettling}
          allowWorktree={!launch.worktree && launch.vcs !== "svn" && !launch.unborn}
          isSvn={launch.vcs === "svn"}
          isUnborn={!!launch.unborn}
          initialPrompt={seedPrompt || undefined}
          initialAttachments={seedAttachments.length ? seedAttachments : undefined}
          initialTitle={seedTitle || undefined}
          initialNewBranch={seedWorkItem?.branch || undefined}
          workItem={seedWorkItem && !launchExisting ? branchItemOf(seedWorkItem) : undefined}
          initialWorktree={launchInPlace ? false : undefined}
          initialExistingBranch={launchExisting || undefined}
          onClose={() => {
            clearLaunch();
            clearSeed();
          }}
          onBack={show ? clearLaunch : undefined}
          onLaunch={async (o) => {
            const run = (take?: HandoffTake) => startWork({ dir: launch.path || "", repo: launch.name }, o, take);
            // A handoff received from another member (docs/log/77) is taken BEFORE the session
            // is created and given back if the launch fails, so a just-withdrawn offer is never
            // launched (#2020).
            const r = seedOfferId ? await launchFromHandoffOffer(seedOfferId, run, toast) : await run();
            if (r.ok) {
              // Seeded by a handoff proposal: badge it as launched now that a session
              // really exists. The proposal itself stays — discarding is the user's call.
              if (seedHandoff && seedHandoffId) void markHandoffLaunched(seedHandoff, seedHandoffId);
              // The work-item ledger (docs/log/80 §80.8). Same reason: this is the only point
              // where a session is known to exist, so it is where the next person can be shown
              // that the item has been started.
              if (seedWorkItem && r.name) {
                void recordWorkItemLaunch(seedWorkItem, r.name, launch.name, workItemLaunchBranch(o, launch.branch || ""));
              }
              useLaunchSeed.getState().onLaunched?.(r.name || "");
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
