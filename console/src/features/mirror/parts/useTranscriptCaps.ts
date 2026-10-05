import { useMemo, useRef } from "react";
import { raw, displayURL, downloadURL } from "../../../core/api/client.ts";
import type { Session } from "../../../types/session.ts";
import { expandThinking, type Settings } from "../../../lib/settings.ts";
// Used by the failure block's re-auth link to open Settings > Agents (ErrorBlock).
import { useSettingsUI } from "../../settings/store.ts";
import { previewEdge } from "../../viewer/previewEdge.ts";
import type { TranscriptCaps } from "../transcript/capabilities.ts";
import type { Group } from "../transcript/types.ts";
import type { useMarksController } from "../transcript/useMarks.ts";
import type { useTranslate } from "../useTranslate.ts";
import type { MirrorActions } from "./useMirrorActions.ts";
import type { MirrorState } from "./useMirrorState.ts";
import type { useMirrorTts } from "./useMirrorTts.tsx";
import type { usePlanActions } from "./usePlanActions.ts";

const q = encodeURIComponent;

/** useTranscriptCaps builds what the mirror's transcript may do (TranscriptCaps), memoized. */
export function useTranscriptCaps({
  session,
  sessionMeta,
  settings,
  managed,
  readOnly,
  agentName,
  st,
  actions,
  plan,
  openForkAt,
  canForkAt,
  tts,
  marks,
  translate,
  toolCard,
  maxSpend,
}: {
  session: string;
  sessionMeta?: Session | null;
  settings: Settings;
  managed: boolean;
  readOnly: boolean;
  agentName: string;
  st: MirrorState;
  actions: MirrorActions;
  plan: ReturnType<typeof usePlanActions>;
  openForkAt: (turn: Group) => void;
  canForkAt: boolean;
  tts: ReturnType<typeof useMirrorTts>;
  marks: ReturnType<typeof useMarksController>;
  translate: ReturnType<typeof useTranslate>;
  toolCard?: TranscriptCaps["toolCard"];
  maxSpend: number;
}) {
  const { setLightbox, rejectedPlansRef, rejectedGen } = st;
  const { takeQueued } = actions;
  const { openFile, openDiff, openPlan, sendPlanComments, planSendBlocked } = plan;
  const thinkingOpen = expandThinking(settings, sessionMeta?.kind);
  // Everything the transcript CALLS. These close over this render's state, so they cannot be
  // memoized — but a block only ever invokes them from a click, so they are routed through a ref
  // and `caps` below keeps its identity without any block ever holding a stale handler.
  const actsRef = useRef<TranscriptCaps>(null as unknown as TranscriptCaps);
  actsRef.current = {
    agentName,
    loadPastedImage: (name) =>
      raw(`api/sessions/${q(session)}/pasted/${encodeURIComponent(name)}`).then((r) => (r.ok ? r.blob() : null)),
    fileURL: downloadURL,
    // 512 is twice the card's 240 px cap, so it still looks right on a HiDPI screen.
    thumbURL: (p: string) => downloadURL(p, 512),
    // Read at the click, so the step follows the window as it is when somebody enlarges.
    previewURL: (p: string) => displayURL(p, previewEdge()),
    openFile,
    openImage: (url, path) => setLightbox({ src: url, path }),
    openDiff,
    openPlan,
    sendPlanComments: (plan: string) => void sendPlanComments(plan),
    forkAt: openForkAt,
    onReauth: () => useSettingsUI.getState().openSettings("agents"),
    isRejectedPlan: (p: string) => rejectedPlansRef.current.has(p.trim()),
    queue: { restore: (id) => void takeQueued(id, true), remove: (id) => void takeQueued(id, false) },
  };

  // What this reader may DO with the transcript. The mirror is the session's owner inside
  // its own Workspace, so it supplies every capability — the shared-session view supplies
  // almost none and the same blocks quietly drop those affordances (transcript/capabilities.ts).
  //
  // Memoized because every turn holds this object: a fresh one per render would re-render the
  // whole conversation on every keystroke in the composer, whatever TranscriptTurn does. So what
  // a block READS while rendering is a dependency here, and what it CALLS goes through actsRef.
  const caps: TranscriptCaps = useMemo(
    (): TranscriptCaps => ({
      agentName,
      repo: sessionMeta?.repo ?? null,
      workItemRefs: true,
      loadPastedImage: (name) => actsRef.current.loadPastedImage!(name),
      fileURL: (p) => actsRef.current.fileURL!(p),
      thumbURL: (p) => actsRef.current.thumbURL!(p),
      previewURL: (p) => actsRef.current.previewURL!(p),
      openFile: (p, line, column) => actsRef.current.openFile!(p, line, column),
      openImage: (url, path) => actsRef.current.openImage!(url, path),
      openDiff: (p) => actsRef.current.openDiff!(p),
      openPlan: (plan) => actsRef.current.openPlan!(plan),
      session,
      sendPlanComments: (plan) => actsRef.current.sendPlanComments!(plan),
      planSendDisabled: planSendBlocked,
      // Presence is what decides whether the affordance renders at all, so it stays reactive;
      // only the call behind it is routed.
      forkAt: canForkAt ? (turn) => actsRef.current.forkAt!(turn) : undefined,
      onReauth: () => actsRef.current.onReauth!(),
      // Lets an auth error block see that the login was renewed after the turn it killed, so it
      // reports that instead of asking for a re-authentication that has already happened. Polled
      // with the rest of the meta, so the card flips on its own once the user comes back from
      // Settings > Agents — no reload, and it survives one (docs/log/47 §4-11).
      authOkAt: sessionMeta?.authOkAt,
      tts: tts.wiring,
      expandThinking: thinkingOpen,
      // Read while a turn renders, so its backing set cannot change silently: every write goes
      // through markRejected, which bumps rejectedGen below.
      isRejectedPlan: (p) => actsRef.current.isRejectedPlan!(p),
      maxSpend,
      marks,
      // Read while a turn renders (which button, whose translation), so the wiring is a
      // dependency: it only changes identity on a press or the one fetch per open, which is
      // also the only time the conversation has to repaint for it.
      translate,
      // Read while a turn renders; the host hands a new one exactly when what it draws changed.
      toolCard,
      // Only the owner of a Managed session can edit its queue; a Terminal (CLI) session's
      // queue lives in the CLI (ADR 0105 decision 6).
      queue:
        managed && !readOnly
          ? { restore: (id) => actsRef.current.queue!.restore(id), remove: (id) => actsRef.current.queue!.remove(id) }
          : undefined,
    }),
    [
      rejectedGen,
      agentName,
      sessionMeta?.repo,
      sessionMeta?.authOkAt,
      session,
      planSendBlocked,
      canForkAt,
      tts.wiring,
      thinkingOpen,
      maxSpend,
      marks,
      translate,
      toolCard,
      managed,
      readOnly,
    ],
  );
  return caps;
}
