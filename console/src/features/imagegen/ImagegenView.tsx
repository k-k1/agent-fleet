// ImagegenView — the image-generation studio (ADR 0081, ADR 0100). A pane like the others: a
// ViewHead, the tabbed grid's header actions, and no state of its own that a tab switch could
// lose — the queue is the Agent's (decision 6), and the draft is the Agent's studio. Every pane
// edits one (decision 10, revised); a pane saved before that revision, with no studio, moves
// this browser's localStorage draft into a new studio on mount.
//
// Three columns (ADR 0100 §4): the bound session's mirror, the draft, and the results with the
// studio's picture history. A narrow pane folds them into tabs.
//
// The polling rule is decision 2's and is the whole reason this screen is affordable: **2 s
// while any job is unfinished, nothing otherwise, and nothing while the tab is hidden.** A
// resident poller here would be a per-second cost on every phone with the pane open — the
// mirror's battery measurements are the precedent. The status is read once per mount and on
// the manual refresh; it does not change on its own except for `warm`, which the job phases
// already report.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { downloadURL, errText, isTransientErr } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { dismissToast } from "../../ui/toast.ts";
import { openGeneratedGallery } from "../gallery/open.ts";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { ImageLightbox } from "../viewer/ImageLightbox.tsx";
import {
  deleteStudio,
  fleetProviders,
  imageProperties,
  imagegenCancelJob,
  imagegenHistory,
  studioDraftLog,
  imagegenGroupOp,
  imagegenJobs,
  imagegenQueueOp,
  imagegenStatus,
  resolveFleetProvider,
  type GroupOp,
  type ImagegenStatus,
  type Job,
  type JobGroup,
  type HistoryItem,
  type PressMode,
} from "./api.ts";
import { anyLive, engineState, foldGroups, studioJobs } from "./jobs.ts";
import { draftFromProperties, emptyDraft, remappedOp } from "./draft.ts";
import { noteImagegenStatus } from "./available.ts";
import { GenerateForm } from "./parts/GenerateForm.tsx";
import { StudioEngineBar } from "./parts/StudioEngineBar.tsx";
import { JobList } from "./parts/JobList.tsx";
import { ResultCards, TrialSlot, resultsOf, type ResultItem } from "./parts/ResultCards.tsx";
import { DraftBar } from "./parts/DraftBar.tsx";
import { pressBlocked, useArrivals, useChatMark, useUnseenCount } from "./loop.ts";
import { AttachAgentModal, type AttachOpts } from "./parts/AttachAgentModal.tsx";
import { DraftLog } from "./parts/DraftLog.tsx";
import { KnowledgeMemo } from "./parts/KnowledgeMemo.tsx";
import { StudioAgent } from "./parts/StudioAgent.tsx";
import { StudioHistory, type PictureActions } from "./parts/StudioHistory.tsx";
import { StudioPicker } from "./parts/StudioPicker.tsx";
import { attachAgent } from "./attach.ts";
import { forgetStudio, migrateLegacyPane, newStudio, once, openImagegen, rememberStudio } from "./open.ts";
import { STUDIO_TITLE_MAX, studioName } from "./studios.ts";
import { studiosChanged } from "./studioBus.ts";
import { useStudioList } from "./useStudioList.ts";
import { historyByPath, pressSeqOf, studioFromForm } from "./studioSync.ts";
import { forgetStudioState, useStudio } from "./useStudio.ts";
import "./imagegen.css";

/** Decision 2's cadence. Only ever runs while something is unfinished AND the tab is shown. */
const POLL_MS = 2000;

/** The pane width at which the columns fold into tabs — imagegen.css's `@container paneview`. */
const NARROW_PX = 720;

/** The picture history's page size. */
const HISTORY_PAGE = 24;

export function ImagegenView({
  paneId = "",
  studioId = null,
  active = true,
  headerActions,
}: {
  paneId?: string;
  /** The Agent's studio this pane edits; null only on a pane saved before every pane had one. */
  studioId?: string | null;
  active?: boolean;
  headerActions?: ReactNode;
}) {
  if (!studioId) return <LegacyPane paneId={paneId} headerActions={headerActions} />;
  return <StudioPane paneId={paneId} studioId={studioId} active={active} headerActions={headerActions} />;
}

// A pane with no studio, from a layout saved before decision 10's revision. It moves the draft
// this browser kept into a new studio (empty when there was none), points the pane at it, and
// drops the local copy — once: the create is keyed by the pane, so a remount while it is in
// flight joins it; several such panes share out the one draft (migrateLegacyPane).
function LegacyPane({ paneId, headerActions }: { paneId: string; headerActions?: ReactNode }) {
  const tr = useT();
  const running = useWorkspaceStore((s) => wsRunning(s.state));
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!running) return;
    let alive = true;
    void migrateLegacyPane(paneId).then((r) => {
      if (!r.studioId) {
        if (alive) setError(r.error || "");
        return;
      }
      // Only the mount still here moves the pane (StrictMode's first mount has been cleaned up).
      if (alive) setPaneTarget(paneId, { content: { kind: "imagegen", studioId: r.studioId } });
    });
    return () => {
      alive = false;
    };
  }, [running, paneId, attempt, setPaneTarget]);
  return (
    <div className="igen">
      <ViewHead actions={headerActions}>
        <span className="view-title">
          <Icon name="wand" /> {tr("imggen.title")}
        </span>
      </ViewHead>
      {error != null ? (
        <EmptyState icon="warning" title={tr("imggen.studio_create_failed")} hint={error}>
          <button
            type="button"
            className="ui-btn"
            onClick={() => {
              setError(null);
              setAttempt((n) => n + 1);
            }}
          >
            {tr("imggen.studio_retry")}
          </button>
        </EmptyState>
      ) : (
        <EmptyState icon="wand" title={running ? tr("imggen.studio_preparing") : tr("imggen.studio_waiting_ws")} />
      )}
    </div>
  );
}

function StudioPane({
  paneId,
  studioId,
  active,
  headerActions,
}: {
  paneId: string;
  studioId: string;
  active: boolean;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const running = useWorkspaceStore((s) => wsRunning(s.state));
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  const studio = useStudio(studioId, { running });
  const draft = studio.form;
  const [status, setStatus] = useState<ImagegenStatus | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [groups, setGroups] = useState<JobGroup[]>([]);
  const [queuePaused, setQueuePaused] = useState(false);
  // The last observed cold start, as the Agent's moving average. It rides on the JOB list,
  // not the status, so a workspace that has never woken the box reports 0 — which is "not
  // measured", not "instant" (lane A, deviation 3).
  const [wakeMs, setWakeMs] = useState(0);
  // The Agent's own caps, reported on every job list so the form can say "full" instead of
  // letting the person press a button that answers 429 (lane A, deviation 3).
  const [caps, setCaps] = useState({ queued: 0, queueMax: 0, trialPending: 0, trialMax: 0 });
  const [failed, setFailed] = useState(false);
  // The job list has been read once: before that an empty list is "not known", not "none".
  const [jobsRead, setJobsRead] = useState(false);
  const [busy, setBusy] = useState(false);
  // The enlarged picture, and which list its ←/→ walk: the results column (trial + results) or
  // the studio's history.
  const [zoom, setZoom] = useState<{ path: string; from: "out" | "history" } | null>(null);
  const zoomPath = zoom?.path ?? null;
  const [attachOpen, setAttachOpen] = useState<"attach" | "replace" | null>(null);
  const [logOpen, setLogOpen] = useState(false);
  // The narrow pane's tab (ADR 0100 §4); ignored while the three columns fit.
  const [tab, setTab] = useState<"chat" | "form" | "out">("form");
  const [studios, readStudios] = useStudioList();
  const [history, setHistory] = useState<HistoryItem[]>([]);
  const [historyBefore, setHistoryBefore] = useState("");
  // The bar's running segment fills against the clock, so the row needs a tick of its own.
  // It advances only alongside a poll, never on a timer of its own.
  const [now, setNow] = useState(() => Date.now());

  const patch = studio.patchForm;

  // Remember the studio this browser has open, for the next plain "open image generation".
  useEffect(() => {
    rememberStudio(studioId);
  }, [studioId]);


  const readHistory = useCallback(
    async (more = false) => {
      try {
        const r = await imagegenHistory({ studio: studioId, limit: HISTORY_PAGE, ...(more && historyBefore ? { before: historyBefore } : {}) });
        if (!r || r.error) return;
        setHistory((h) => (more ? [...h, ...(r.items || [])] : r.items || []));
        setHistoryBefore(r.before || "");
      } catch {
        /* a transient 502 keeps the list */
      }
    },
    [studioId, historyBefore],
  );

  const readStatus = useCallback(async () => {
    try {
      const s = await imagegenStatus();
      if (!s || isTransientErr(s)) return;
      setStatus(s);
      noteImagegenStatus(s); // the bar's button follows the same answer
      setFailed(false);
    } catch {
      setFailed(true);
    }
  }, []);

  const readJobs = useCallback(async () => {
    try {
      const r = await imagegenJobs();
      if (!r || isTransientErr(r)) return;
      setJobs(Array.isArray(r.jobs) ? r.jobs : []);
      setGroups(Array.isArray(r.groups) ? r.groups : []);
      setQueuePaused(!!r.paused);
      setWakeMs(r.wake_ms || 0);
      setCaps({
        queued: r.queued || 0,
        queueMax: r.queue_max || 0,
        trialPending: r.trial_pending || 0,
        trialMax: r.trial_max || 0,
      });
      setNow(Date.now());
      setJobsRead(true);
    } catch {
      /* a transient 502 while the agent restarts keeps the list on screen */
    }
  }, []);

  useEffect(() => {
    if (!running) return;
    void readStatus();
    void readJobs();
    void readStudios();
    // readHistory's identity follows its cursor; the first page is read once per studio.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, readStatus, readJobs, readStudios]);
  useEffect(() => {
    if (running) void readHistory(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, studioId]);

  // The poller. Its whole contract is in the guard: something unfinished, the tab visible,
  // the workspace up. `live` is recomputed from the list every render, so the last finished
  // job stops the timer on the next tick rather than leaving it resident.
  const live = anyLive(jobs);
  const jobsRef = useRef(readJobs);
  jobsRef.current = readJobs;
  useEffect(() => {
    if (!live || !running) return;
    let id = 0;
    const tick = () => {
      if (!document.hidden) void jobsRef.current();
    };
    id = window.setInterval(tick, POLL_MS);
    const onVis = () => {
      if (!document.hidden) tick();
    };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      window.clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [live, running]);

  // ADR 0082 unresolved question 2: with N fleet rows, each carries its OWN Studio answer —
  // two comfy rows can have overlapping model ids with different checkpoints behind them — so
  // silently driving the pane off "the first ready one" is right only while there is just one.
  // fleetProviderList is every ready fleet row; resolveFleetProvider picks the member's own
  // choice among them (draft.providerId), defaulting to the first when unset or stale.
  const fleetProviderList = useMemo(() => fleetProviders(status), [status]);
  const provider = resolveFleetProvider(fleetProviderList, draft.providerId);
  const models = provider?.models || [];
  const loras = provider?.loras || [];
  const model = models.find((m) => m.id === draft.model) || null;
  // Engine-level fields live on the PROVIDER, not the status root: a fleet with both comfy
  // and openai-compat has two answers, and reading the root would silently mix them.
  const samplers = provider?.samplers || [];
  const schedulers = provider?.schedulers || [];
  const loraWeightMax = provider?.lora_weight_max || 2;
  const alwaysNegative = provider?.negative_always || "";
  const state = engineState(status, jobs, model);
  // 0 on either side means "nothing measured", so the hint falls back to "several minutes"
  // rather than claiming the engine starts instantly.
  const coldMs = wakeMs || provider?.wake_ms || 0;
  // A cap of 0 means the Agent did not report one (an older build): never block on it.
  const trialFull = caps.trialMax > 0 && caps.trialPending >= caps.trialMax;
  const queueFull = caps.queueMax > 0 && caps.queued + draft.jobs > caps.queueMax;

  // ADR 0094 decision 12: a model whose family does not offer the draft's current op (most
  // commonly switching TO an edit-only checkpoint while "generate" was still selected) is
  // remapped to that model's OWN first op rather than left pointed at a choice not on offer —
  // pressing enqueue would hit decision 2's 400 and then fall through to a provider that spends
  // a member's own plan (decision 11). Absent `model.ops` (an Agent old enough to predate the
  // ADR) leaves the draft untouched, the same "no signal, no change" rule `knobs` follows.
  const modelId = model?.id;
  useEffect(() => {
    const next = remappedOp(draft.op, model?.ops);
    if (next == null) return;
    patch({ op: next });
    toast(tr("imggen.op_remapped", { family: model?.family || "", op: tr(`imggen.op_${next}` as "imggen.op_generate") }), {
      kind: "info",
    });
    // Only when the MODEL changes: re-running this on every draft.op edit would fight a member's
    // own manual choice the instant they picked something the current model happens to offer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [modelId]);

  const rows = useMemo(() => foldGroups(jobs, groups), [jobs, groups]);
  // Results and the trial slot are this studio's presses only; the queue above stays shared.
  const ownJobs = useMemo(() => studioJobs(jobs, studioId), [jobs, studioId]);
  const results = useMemo(() => resultsOf(ownJobs.filter((j) => !j.trial)), [ownJobs]);
  const latestTrial = useMemo(() => resultsOf(ownJobs.filter((j) => j.trial))[0] ?? null, [ownJobs]);
  // The results column's pictures in its own order — the trial slot, then the grid — which is
  // what the lightbox walks and what the badge and the "ready" notice count.
  const outItems = useMemo<ResultItem[]>(() => (latestTrial ? [latestTrial, ...results] : results), [latestTrial, results]);
  const outPaths = useMemo(() => outItems.map((r) => r.file.path), [outItems]);
  // What the badge and the "ready" notice count: every finished picture of this studio,
  // including trials the slot does not show — two trials finishing between two reads are two.
  const madePaths = useMemo(() => resultsOf(ownJobs).map((r) => r.file.path), [ownJobs]);

  // Whether the columns are folded into tabs. The CSS decides the layout by container query;
  // this only tells the badges and the notice whether a tab other than the results hides them.
  const rootRef = useRef<HTMLDivElement>(null);
  const [narrow, setNarrow] = useState(false);
  useEffect(() => {
    const el = rootRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setNarrow(el.clientWidth <= NARROW_PX));
    ro.observe(el);
    setNarrow(el.clientWidth <= NARROW_PX);
    return () => ro.disconnect();
  }, []);
  const sees = (k: "chat" | "form" | "out") => active && (!narrow || tab === k);
  const newResults = useUnseenCount(madePaths, jobsRead, sees("out"));

  const submit = useCallback(
    async (trial: boolean) => {
      if (!draft.prompt.trim()) {
        toast(tr("imggen.no_prompt"), { kind: "info" });
        return;
      }
      setBusy(true);
      // ADR 0100 decision 9: a press is one POST that records the version and enqueues the
      // studio's own draft — the form's pending edit is flushed first.
      const mode: PressMode = trial ? "trial" : "enqueue";
      const r = await studio.press(mode);
      setBusy(false);
      if (!r) return;
      toast(
        r.recorded === false
          ? tr("imggen.press_record_pending", { n: r.jobs?.length ?? 1 })
          : tr("imggen.enqueued", { n: r.jobs?.length ?? (trial ? 1 : draft.jobs) }),
        { kind: "info" },
      );
      await readJobs();
    },
    [draft, readJobs, toast, tr, studio],
  );

  // The finished pictures of this studio arrive through the job list; re-read the history page
  // when the count of finished jobs moves, never on a timer of its own.
  const doneCount = useMemo(() => ownJobs.filter((j) => j.state === "done").length, [ownJobs]);
  useEffect(() => {
    if (doneCount) void readHistory(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doneCount]);

  // Switch this pane to another studio — unless another pane already has it, which is then
  // focused instead: two panes on one studio are what decision 10's sameTarget exists to stop
  // (their debounced saves would race each other on every keystroke).
  const openStudio = useCallback(
    (id: string) => {
      if (id === studioId) return;
      const st = useLayoutStore.getState();
      const other = allViews(st.layout).find(
        (v) => v.id !== paneId && v.content.kind === "imagegen" && v.content.studioId === id,
      );
      if (other) st.selectTab(other.id);
      else setPaneTarget(paneId, { content: { kind: "imagegen", studioId: id } });
    },
    [paneId, studioId, setPaneTarget],
  );

  // A studio the Agent says is gone is not the one to reopen next time.
  useEffect(() => {
    if (studio.missing) forgetStudio(studioId);
  }, [studioId, studio.missing]);

  // Put a new, empty studio in THIS pane — after a delete, or when the studio is gone.
  const replaceWithNew = useCallback(async () => {
    const r = await once(`replace:${paneId}`, () => newStudio(emptyDraft()));
    if (!r.studioId) {
      toast(r.error, { kind: "error" });
      return;
    }
    setPaneTarget(paneId, { content: { kind: "imagegen", studioId: r.studioId } });
  }, [paneId, setPaneTarget, toast]);

  // "＋ New studio" opens beside this one; every pane's list hears of it (studiosChanged).
  const createStudio = useCallback(async () => {
    await openImagegen({ fresh: true, newPane: true });
  }, []);

  const attach = useCallback(
    async (o: AttachOpts): Promise<boolean> => {
      // Prompts are written for a model (revision 9): the dialog picks one and will not start
      // without it, and attachAgent writes it into the studio before the persona is read.
      const cur = studio.studio;
      const r = await attachAgent({
        studioId,
        // The pane's resolved row, not the draft's possibly-stale pick (decision 2: a trial never
        // falls back to another provider, so the studio must name the one on screen).
        draft: () => studioFromForm({ ...draft, providerId: provider?.id || "" }),
        opts: o,
        ...(studioId && cur ? { existing: { title: cur.title || "", updatedAt: cur.updated_at, draft: cur.draft || {} } } : {}),
        replacing: attachOpen === "replace" ? studio.studio?.session : undefined,
      });
      if (r.error) toast(r.error, { kind: "error" });
      if (r.session) void refreshSessions();
      if (r.studioId) {
        studiosChanged();
        if (r.studioId !== studioId) openStudio(r.studioId);
        else void studio.reload();
      }
      return !!r.session;
    },
    [studioId, draft, provider, attachOpen, studio, toast, tr, refreshSessions, openStudio],
  );

  const removeStudio = useCallback(async () => {
    const ok = await confirm({
      title: tr("imggen.studio_delete_title"),
      body: tr("imggen.studio_delete_body"),
      confirmLabel: tr("imggen.studio_delete"),
      danger: true,
    });
    if (!ok) return;
    const r = await deleteStudio(studioId).catch(() => null);
    if (!r || !r.ok) {
      toast(tr("imggen.studio_delete_failed"), { kind: "error" });
      return;
    }
    forgetStudio(studioId);
    forgetStudioState(studioId);
    studiosChanged();
    // The bound session's meta loses its studio on the Agent; the rail's wand follows.
    void refreshSessions();
    // The pane goes on with the most recent studio no other pane shows, else a new one.
    const shown = new Set(
      allViews(useLayoutStore.getState().layout).flatMap((v) => (v.content.kind === "imagegen" && v.content.studioId ? [v.content.studioId] : [])),
    );
    const next = studios.find((s) => s.id !== studioId && !shown.has(s.id));
    if (next) setPaneTarget(paneId, { content: { kind: "imagegen", studioId: next.id } });
    else void replaceWithNew();
  }, [studioId, studios, paneId, confirm, tr, toast, refreshSessions, setPaneTarget, replaceWithNew]);

  // "Back to this picture's settings": the press its version names, through the same rewind
  // as the edit history. The log page on screen may not reach that far back, so older pages
  // are read until it does. A picture with no version (made before the studio, or elsewhere)
  // falls back to its recovered properties, written as the member's own edit.
  const restorePicture = useCallback(
    async (path: string, version?: string) => {
      const v = version || historyByPath(history).get(path)?.version;
      if (v) {
        let seq = pressSeqOf(studio.log, v);
        let before = studio.log.length ? Math.min(...studio.log.map((e) => e.seq)) : 0;
        for (let i = 0; seq == null && before > 0 && i < 10; i++) {
          const page = await studioDraftLog(studioId, before, 200).catch(() => null);
          if (!page || page.error) break;
          seq = pressSeqOf(page.entries, v);
          before = page.before || 0;
        }
        if (seq != null) {
          await studio.rewind(seq);
          return;
        }
      }
      try {
        const props = await imageProperties(path);
        if (!props || props.source === "none") {
          toast(tr("imggen.pic_restore_none"), { kind: "info" });
          return;
        }
        patch(draftFromProperties(draft, props));
      } catch {
        toast(tr("imggen.pic_restore_none"), { kind: "info" });
      }
    },
    [studioId, history, studio, draft, patch, toast, tr],
  );

  const pictureActions: PictureActions = {
    onRestore: (path, version) => void restorePicture(path, version),
    onReference: (path) => {
      const ops = model?.ops?.length ? model.ops : ["edit"];
      patch({ op: ops.includes("edit") ? "edit" : ops.find((o) => o !== "generate") || "edit", inputs: [path] });
      toast(tr("imggen.pic_reference_done"), { kind: "info" });
    },
    // P0: the mask is the existing path field; painting it is P1 (decision 11).
    onFix: (path) => {
      patch({ op: "inpaint", inputs: [path], mask: "" });
      toast(tr("imggen.pic_fix_done"), { kind: "info" });
    },
  };

  const groupOp = useCallback(
    async (id: string, op: GroupOp) => {
      const r = await imagegenGroupOp(id, op).catch(() => ({ error: { code: "unknown" } }));
      if (r?.error) toast(errText(r.error) || tr("imggen.op_failed"), { kind: "error" });
      await readJobs();
    },
    [readJobs, toast, tr],
  );

  const queueOp = useCallback(
    async (op: "pause" | "resume") => {
      const r = await imagegenQueueOp(op).catch(() => ({ error: { code: "unknown" } }));
      if (r?.error) toast(errText(r.error) || tr("imggen.op_failed"), { kind: "error" });
      await readJobs();
    },
    [readJobs, toast, tr],
  );

  const cancelJob = useCallback(
    async (id: string) => {
      await imagegenCancelJob(id).catch(() => undefined);
      await readJobs();
    },
    [readJobs],
  );

  const useSeed = useCallback(
    (seed: number) => {
      patch({ seed: String(seed), seedPolicy: "fixed" });
      toast(tr("imggen.seed_taken", { seed }), { kind: "info" });
    },
    [patch, toast, tr],
  );

  const trialAt = useCallback(
    (seed: number | null) => {
      if (seed != null) patch({ seed: String(seed), seedPolicy: "fixed" });
      else patch({ seedPolicy: "random" });
      void submit(true);
    },
    [patch, submit],
  );
  const again = useCallback(
    (item: ResultItem, sameSeed: boolean) => trialAt(sameSeed ? (item.file.seed ?? item.job.seed ?? null) : null),
    [trialAt],
  );

  const close = useCallback(() => setZoom(null), []);
  const zoomOut = useCallback((path: string) => setZoom({ path, from: "out" }), []);
  const zoomHistory = useCallback((path: string) => setZoom({ path, from: "history" }), []);
  // A history picture older than the job list carries no seed (a history row has none), so its
  // seed is read from the picture's own properties; unreadable, and the seeded trial is hidden.
  const [zoomPropsSeed, setZoomPropsSeed] = useState<{ path: string; seed: number | null } | null>(null);
  const zoomInJobs = !!zoom && outItems.some((r) => r.file.path === zoom.path);
  useEffect(() => {
    if (!zoom || zoomInJobs) return;
    let live = true;
    const path = zoom.path;
    imageProperties(path)
      .then((p) => live && setZoomPropsSeed({ path, seed: p && p.source !== "none" && typeof p.seed === "number" ? p.seed : null }))
      .catch(() => live && setZoomPropsSeed({ path, seed: null }));
    return () => {
      live = false;
    };
  }, [zoom, zoomInJobs]);
  // Back closes the lightbox instead of the pane; the host owns that entry, not the
  // lightbox (the same rule the gallery and the mirror follow).
  useBackClose(zoomPath ? close : undefined, !!zoomPath);

  // ←/→ through the list the picture was opened from. A picture that has left that list (the
  // job was cleared under it) simply has no neighbours.
  const zoomList = zoom?.from === "history" ? history.map((h) => h.path) : outPaths;
  const zoomAt = zoomPath ? zoomList.indexOf(zoomPath) : -1;
  const lightboxPaging =
    zoom && zoomAt >= 0 && zoomList.length > 1
      ? {
          index: zoomAt + 1,
          total: zoomList.length,
          onPrev: zoomAt > 0 ? () => setZoom({ ...zoom, path: zoomList[zoomAt - 1] }) : undefined,
          onNext: zoomAt < zoomList.length - 1 ? () => setZoom({ ...zoom, path: zoomList[zoomAt + 1] }) : undefined,
        }
      : {};
  // The studio's verbs on the enlarged picture — the cards' own, so the lightbox is where the
  // loop can continue. Each closes the lightbox: what it changed is on the form or in the queue.
  const zoomItem = zoomPath ? outItems.find((r) => r.file.path === zoomPath) : undefined;
  const zoomSeed = zoomItem ? (zoomItem.file.seed ?? zoomItem.job.seed ?? null) : zoomPropsSeed?.path === zoomPath ? zoomPropsSeed.seed : null;
  const zoomVersion = zoom?.from === "history" ? history.find((h) => h.path === zoomPath)?.version : undefined;
  const lightboxActions =
    zoomPath && pictureActions ? (
      <>
        <button
          type="button"
          onClick={() => {
            pictureActions.onReference(zoomPath);
            close();
          }}
        >
          <Icon name="file-media" /> {tr("imggen.lb_reference")}
        </button>
        {zoomSeed != null && (
          <button
            type="button"
            disabled={pressBlocked(draft, { busy: busy || state === "unavailable", trialFull, queueFull }).trial}
            onClick={() => {
              trialAt(zoomSeed);
              close();
            }}
          >
            <Icon name="beaker" /> {tr("imggen.lb_again")}
          </button>
        )}
        <button
          type="button"
          onClick={() => {
            pictureActions.onRestore(zoomPath, zoomVersion);
            close();
          }}
        >
          <Icon name="history" /> {tr("imggen.lb_restore")}
        </button>
      </>
    ) : undefined;

  const engineLine =
    state === "unavailable"
      ? status?.error
        ? errText(status.error)
        : tr("imggen.engine_unavailable")
      : state === "starting"
        ? tr("imggen.engine_starting")
        : state === "cold"
          ? coldMs
            ? tr("imggen.engine_cold_hint", { min: Math.max(1, Math.round(coldMs / 60_000)) })
            : tr("imggen.engine_cold_hint_unknown")
          : tr("imggen.engine_ready");

  const refresh = () => {
    void readStatus();
    void readJobs();
  };
  const engineBar = (variant: "head" | "band") => (
    <StudioEngineBar
      variant={variant}
      draft={draft}
      patch={patch}
      fleetProviders={fleetProviderList}
      provider={provider}
      models={models}
      state={state}
      engineLine={engineLine}
      onRefresh={refresh}
    />
  );

  const session = studio.studio?.session || "";
  const sessionState = useSessionsStore((s) => (session ? s.sessions.find((x) => x.name === session)?.state : undefined));
  const chatMark = useChatMark(sessionState, sees("chat"));

  // "N pictures are ready", only when the member is not already looking at the results: a
  // narrow pane on another tab, or this pane not the active one. The pictures are the
  // difference between two job lists the poller read anyway — nothing polls for this.
  const noticeKey = "igen-done-" + (paneId || "pane");
  // Pictures announced since the member last saw the results: a second batch while the first
  // notice is still up says the total, not only the latest batch.
  const announced = useRef(0);
  const viewResults = useCallback(() => {
    announced.current = 0;
    dismissToast(noticeKey);
    setTab("out");
    if (paneId) useLayoutStore.getState().selectTab(paneId);
  }, [noticeKey, paneId]);
  const seesOut = sees("out");
  // Looking at the results by any route (the tab, the three columns, the pane made active)
  // settles the notice as View does.
  useEffect(() => {
    if (!seesOut) return;
    announced.current = 0;
    dismissToast(noticeKey);
  }, [seesOut, noticeKey]);
  // The notice's View closes over THIS mount's tab state; switching the pane to another studio
  // remounts the view, and a View left behind would drive a pane that no longer exists.
  useEffect(() => () => dismissToast(noticeKey), [noticeKey, studioId]);
  useArrivals(madePaths, jobsRead, (n) => {
    if (seesOut) return;
    announced.current += n;
    toast(
      <span className="igen-done-toast">
        {tr("imggen.done_toast", { n: announced.current })}{" "}
        <button type="button" className="ui-btn ui-btn-sm" onClick={viewResults}>
          {tr("imggen.done_view")}
        </button>
      </span>,
      // Top: at the bottom it covered the draft bar and the composer on a phone.
      { kind: "success", key: noticeKey, duration: 8000, placement: "top" },
    );
  });
  const datedName = studio.studio ? studioName({ ...studio.studio, title: "" }, (stamp) => tr("imggen.studio_dated", { when: stamp })) : "";
  const form = (
    <GenerateForm
      draft={draft}
      patch={patch}
      fleetProviders={fleetProviderList}
      provider={provider}
      models={models}
      loras={loras}
      model={model}
      samplers={samplers}
      schedulers={schedulers}
      loraWeightMax={loraWeightMax}
      alwaysNegative={alwaysNegative}
      busy={busy || state === "unavailable"}
      trialFull={trialFull}
      queueFull={queueFull}
      onTrial={() => void submit(true)}
      onEnqueue={() => void submit(false)}
      modelInHead
      locks={studio.locks}
      onToggleLock={studio.toggleLock}
      highlight={studio.highlight as ReadonlySet<string>}
      familyExtra={model ? <KnowledgeMemo family={model.family} model={model.id} /> : null}
    />
  );

  return (
    <div className="igen" ref={rootRef}>
      <ViewHead
        actions={
          <>
            {/* Where the pictures land. The gallery opens the generated ROOT rather than
                this pane's out_dir: the folder cards there reach console/ in one click and
                also the sessions' own folders, which is what "show me what we made" means. */}
            <IconButton
              icon="file-media"
              label={tr("pane.open_generated")}
              onClick={() => openGeneratedGallery()}
            />
            {/* A narrow pane draws this in the engine band instead (imagegen.css). */}
            <IconButton icon="refresh" label={tr("imggen.refresh")} className="igen-head-refresh" onClick={refresh} />
            {headerActions}
          </>
        }
      >
        <span className="view-title">
          <Icon name="wand" /> <span className="igen-title-text">{tr("imggen.title")}</span>
        </span>
        {/* The studio this pane edits (ADR 0100 decision 10), and "＋ New studio" beside it. */}
        <StudioPicker
          studioId={studioId}
          studios={studios}
          current={studio.studio}
          onOpen={openStudio}
          onNew={() => void createStudio()}
        />
        {/* The model is the member's (decision 4); its place in the head says so. A narrow pane
            draws the same bar at the top of the settings tab instead (imagegen.css). */}
        {engineBar("head")}
        {studio.studio && (
          <details className="igen-studio-menu">
            <summary title={tr("imggen.studio_settings")}>
              <Icon name="gear" />
            </summary>
            <div className="igen-studio-menu-body">
              <label className="igen-studio-rename">
                {tr("imggen.studio_rename")}
                {/* Keyed by the stored title so an answer from the Agent (or another pane)
                    replaces what is shown; saved on blur or Enter, "" goes back to the date. */}
                <input
                  key={studio.studio.title}
                  className="ds-input"
                  defaultValue={studio.studio.title}
                  placeholder={datedName}
                  maxLength={STUDIO_TITLE_MAX}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") e.currentTarget.blur();
                  }}
                  onBlur={(e) => {
                    const v = e.currentTarget.value.trim();
                    if (v !== (studio.studio?.title || "")) void studio.setTitle(v).then(studiosChanged);
                  }}
                />
              </label>
              <label className="igen-fullsteps">
                <input
                  type="checkbox"
                  checked={studio.studio.agent_trial}
                  onChange={(e) => studio.setAgentTrial(e.target.checked)}
                />
                {tr("imggen.studio_agent_trial")}
              </label>
              <span className="igen-hint">{tr("imggen.studio_agent_trial_hint")}</span>
              <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => void removeStudio()}>
                <Icon name="trash" /> {tr("imggen.studio_delete")}
              </button>
            </div>
          </details>
        )}
      </ViewHead>
      {studio.failed && !studio.studio ? (
        <EmptyState icon="warning" title={tr("imggen.studio_read_failed")} hint={studio.failed}>
          <button type="button" className="ui-btn" onClick={() => void replaceWithNew()}>
            {tr("imggen.studio_new_here")}
          </button>
        </EmptyState>
      ) : failed && !status ? (
        <EmptyState icon="warning" title={tr("imggen.engine_unavailable")} />
      ) : (
        <>
          <div className="igen-tabs" role="tablist">
            {(["chat", "form", "out"] as const).map((k) => (
              <button
                key={k}
                type="button"
                role="tab"
                aria-selected={tab === k}
                className={"igen-tab" + (tab === k ? " active" : "")}
                onClick={() => setTab(k)}
              >
                {tr(`imggen.tab_${k}` as "imggen.tab_chat")}
                {k === "out" && newResults > 0 && (
                  <span className="igen-tab-badge" aria-label={tr("imggen.tab_new_results", { n: newResults })}>
                    +{newResults}
                  </span>
                )}
                {k === "form" && studio.highlight.size > 0 && (
                  <span className="igen-tab-dot" aria-label={tr("imggen.tab_agent_changed")} />
                )}
                {k === "chat" && chatMark && (
                  <span
                    className={"igen-tab-dot igen-tab-dot-" + chatMark}
                    aria-label={tr(`imggen.tab_chat_${chatMark}` as "imggen.tab_chat_working")}
                  />
                )}
              </button>
            ))}
          </div>
          <div className="igen-body igen-studio" data-tab={tab}>
            <div className="igen-col-agent">
              <StudioAgent
                paneId={paneId}
                studioId={studioId}
                session={session}
                active={active}
                signal={studio.signal}
                log={studio.log}
                onRewind={studio.rewind}
                needsModel={!draft.model.trim()}
                attachPicksModel
                onAttach={() => setAttachOpen("attach")}
                onReplace={() => setAttachOpen("replace")}
                aboveComposer={
                  <DraftBar
                    draft={draft}
                    modelName={model?.label || draft.model}
                    highlight={studio.highlight.size > 0}
                    trial={latestTrial}
                    gate={{ busy: busy || state === "unavailable", trialFull, queueFull }}
                    onTrial={() => void submit(true)}
                    onEnqueue={() => void submit(false)}
                    onOpenForm={() => setTab("form")}
                    onZoom={zoomOut}
                  />
                }
              />
            </div>
            <div className="igen-col-form">
              {engineBar("band")}
              <div className="igen-form-head">
                <button
                  type="button"
                  className={"ui-btn ui-btn-ghost ui-btn-sm" + (logOpen ? " active" : "")}
                  aria-expanded={logOpen}
                  onClick={() => setLogOpen((v) => !v)}
                >
                  <Icon name="history" /> {tr("imggen.log_title")}
                </button>
                {studio.highlight.size > 0 && <span className="igen-hl-note">{tr("imggen.hl_note")}</span>}
              </div>
              {logOpen && (
                <DraftLog
                  log={studio.log}
                  recordPending={studio.recordPending}
                  hasOlder={studio.hasOlder}
                  onOlder={() => void studio.loadOlder()}
                  onRewind={(seq) => void studio.rewind(seq)}
                />
              )}
              {form}
            </div>
            <div className="igen-col-out">
              <TrialSlot item={latestTrial} onZoom={zoomOut} onUseSeed={useSeed} />
              <JobList
                rows={rows}
                queuePaused={queuePaused}
                queued={caps.queued}
                queueMax={caps.queueMax}
                now={now}
                onGroupOp={(id, op) => void groupOp(id, op)}
                onQueueOp={(op) => void queueOp(op)}
                onCancelJob={(id) => void cancelJob(id)}
                studioId={studioId}
              />
              <ResultCards
                items={results}
                onZoom={zoomOut}
                actions={pictureActions}
                onAgain={again}
              />
              <StudioHistory
                items={history}
                hasMore={!!historyBefore}
                onMore={() => void readHistory(true)}
                onZoom={zoomHistory}
                actions={pictureActions}
              />
            </div>
          </div>
        </>
      )}
      {attachOpen && (
        <AttachAgentModal
          replacing={attachOpen === "replace" ? session : undefined}
          initialImage={{ providerId: provider?.id || "", model: draft.model }}
          onClose={() => setAttachOpen(null)}
          onAttach={attach}
        />
      )}
      {zoomPath &&
        createPortal(
          <ImageLightbox
            src={downloadURL(zoomPath)}
            path={zoomPath}
            onClose={close}
            {...lightboxPaging}
            actions={lightboxActions}
          />,
          document.body,
        )}
    </div>
  );
}
