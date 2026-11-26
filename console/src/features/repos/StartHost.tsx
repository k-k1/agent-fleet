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
import { draftHasContent, useHubDraft } from "./hubDraft.ts";

export function StartHost() {
  const startTick = useSessionsStore((s) => s.startTick);
  const [show, setShow] = useState(false);
  // The repo-less (home) start-work dialog, stacked on the hub like a picked repo's.
  const [home, setHome] = useState(false);
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
  const seedSpace = useLaunchSeed((s) => s.space);
  // A launch that came from a work item (docs/log/80). The ledger is written only AFTER the
  // launch succeeded.
  const seedWorkItem = useLaunchSeed((s) => s.workItem);
  const clearSeed = useLaunchSeed((s) => s.clear);
  // The hub's one draft (ADR 0142 decision 5). Every opening starts it from the seed, so a memo
  // being sent or restored is the text the hub opens with.
  const draft = useHubDraft();
  const draftRef = useRef(draft);
  draftRef.current = draft;
  // A repository picked in the hub with something in the draft takes the draft into its dialog;
  // with nothing in it, the dialog shows that repository's own stored draft as before.
  const [carry, setCarry] = useState(false);
  // A pick waiting for a memo restore to set the repository's draft aside: the hub takes no other
  // way out meanwhile, or the late save would open that dialog over a launch already under way.
  const [pickPending, setPickPending] = useState(false);
  // A memo whose files did not all come back is not finished by a launch: deleting it would lose
  // the ones that never reached the session.
  const finishSeed = (name: string) => {
    if (draft.missing === 0) useLaunchSeed.getState().onLaunched?.(name);
  };
  // Open the hub whenever the global tick changes (skip the mount value).
  const lastTickRef = useRef(startTick);
  useEffect(() => {
    if (startTick !== lastTickRef.current) {
      lastTickRef.current = startTick;
      clearLaunch();
      setHome(false);
      setCarry(false);
      const seed = useLaunchSeed.getState();
      draft.reset({ prompt: seed.prompt, attachments: seed.attachments });
      setShow(true);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- draft.reset is a fresh closure each render
  }, [startTick, clearLaunch]);

  const toast = useToast();
  // Hub picks that restore a memo await a draft save before opening (onPickRepo below).
  // picking stops a second click from saving twice; hubGen tells a save that finished
  // after the hub closed not to reopen it.
  const picking = useRef(false);
  const hubGen = useRef(0);
  const ctx = useRepoRailContext(); // connection-gated kinds, like the repo rows
  const startWork = useStartWork({ policyInline: true });
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
          // A space-seeded opening starts the hub over, also when one is already open: the form
          // reads its space and title only when it mounts.
          key={seedSpace ? `space:${startTick}` : "hub"}
          kinds={agentKinds}
          access={ctx.access}
          claudeExpired={ctx.claudeExpired}
          settling={ctx.connsSettling}
          draft={draft}
          onLaunchSpace={async (o) => {
            // The seed this launch carries, taken now: a hub opened for another seed while the
            // create is in flight must not have that seed finished by this launch's result.
            const sent = useLaunchSeed.getState();
            // dir "" with a space: the Agent makes ~/spaces/<folder> and starts the session there.
            const r = await startWork({ dir: "", repo: "" }, o);
            if (r.ok) {
              // Seeded by a handoff proposal (from a session in a space): badge it as launched now
              // that a session really exists, as the repository dialog's success path does.
              if (sent.handoffSession && sent.handoffId) void markHandoffLaunched(sent.handoffSession, sent.handoffId);
              // A memo that seeded the draft is done with once its text reached a session.
              if (draft.missing === 0) sent.onLaunched?.(r.name || "");
            }
            return r;
          }}
          seedSpace={seedSpace}
          seedTitle={seedTitle}
          onClose={() => {
            // A hub since replaced by a newer opening (this instance's launch finished late) must
            // not close the new one or drop its seed.
            if (lastTickRef.current !== startTick) {
              // The finished launch has just emptied the shared draft; the newer opening's text is
              // the seed's, so put it back.
              const seed = useLaunchSeed.getState();
              draftRef.current.reset({ prompt: seed.prompt, attachments: seed.attachments });
              return;
            }
            hubGen.current++;
            setShow(false);
            clearSeed();
          }}
          pickPending={pickPending}
          onPickHome={() => setHome(true)}
          onPickRepo={async (r, fromRepoSection) => {
            // A memo being restored (#3289) outranks the picked repo's stored draft: set that
            // draft aside first, and open nothing when it could not be saved.
            const { memoId, prompt, attachments } = useLaunchSeed.getState();
            // Only a repository picked in its own section takes the draft: Other's draft is the
            // home launch's, and a folder cloned or created there must not receive it.
            const take = fromRepoSection && draftHasContent(draft);
            setCarry(take);
            // A dialog that does not take the draft shows the repository's own stored draft and
            // restores nothing, so there is nothing to set aside.
            if (!memoId || !take) return openLaunch(r);
            if (picking.current) return; // one save at a time; a second click would save it twice
            picking.current = true;
            setPickPending(true);
            const gen = hubGen.current;
            const s = await stashStoredDraft(r.name, { body: prompt, attachments }).finally(() => {
              picking.current = false;
              setPickPending(false);
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
      {show && home && (
        <LaunchModal
          home
          repo=""
          kinds={agentKinds}
          access={ctx.access}
          claudeExpired={ctx.claudeExpired}
          settling={ctx.connsSettling}
          allowWorktree={false}
          shared={draft}
          onClose={() => setHome(false)}
          onBack={() => setHome(false)}
          onLaunch={async (o) => {
            // dir "" = the home directory, no working copy; useStartWork skips the repo-scoped
            // bookkeeping and adds the new session to the active working set.
            const r = await startWork({ dir: "", repo: "" }, o);
            if (r.ok) {
              finishSeed(r.name || "");
              setHome(false);
              setShow(false); // launched — drop the hub underneath too
              clearSeed();
            }
            return r;
          }}
        />
      )}
      {launch && (
        <LaunchModal
          repo={launch.name}
          branch={launch.branch}
          path={launch.path}
          kinds={agentKinds}
          access={ctx.access}
          claudeExpired={ctx.claudeExpired}
          settling={ctx.connsSettling}
          allowWorktree={!launch.worktree && launch.vcs !== "svn" && !launch.unborn}
          isSvn={launch.vcs === "svn"}
          isUnborn={!!launch.unborn}
          // From the hub the seed is already in the hub's draft (and may have been edited or
          // emptied there), so only a launch opened without the hub reads it.
          shared={show && carry ? draft : undefined}
          initialPrompt={!show && seedPrompt ? seedPrompt : undefined}
          initialAttachments={!show && seedAttachments.length ? seedAttachments : undefined}
          initialTitle={seedTitle || undefined}
          initialNewBranch={seedWorkItem?.branch || undefined}
          workItem={seedWorkItem && !launchExisting ? branchItemOf(seedWorkItem) : undefined}
          initialWorktree={launchInPlace ? false : undefined}
          initialExistingBranch={launchExisting || undefined}
          // Closed over the hub, it goes back to the hub like Back: the seed stays with the hub's
          // draft, so a launch from there still finishes the memo it holds.
          onClose={() => {
            clearLaunch();
            if (!show) clearSeed();
          }}
          onBack={show ? clearLaunch : undefined}
          onLaunch={async (o) => {
            // Over the hub without its draft, the seed (a memo) never reached this session.
            const run = (take?: HandoffTake) => startWork({ dir: launch.path || "", repo: launch.name, seedless: show && !carry }, o, take);
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
              // Over the hub the memo is finished only by a launch that took the hub's draft (the
              // seed is in it); without the hub the dialog was seeded directly, as before.
              if (!show) useLaunchSeed.getState().onLaunched?.(r.name || "");
              else if (carry) finishSeed(r.name || "");
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
