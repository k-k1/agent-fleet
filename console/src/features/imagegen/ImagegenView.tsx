// ImagegenView — the image-generation studio (ADR 0081, ADR 0100). A pane like the others: a
// ViewHead, the tabbed grid's header actions, and no state of its own that a tab switch could
// lose — the queue is the Agent's (decision 6), and the draft is either the Agent's studio
// (`studioId`) or, in the studio-less pane, a localStorage draft.
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
import { downloadURL, errText, getTenant, isTransientErr } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
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
  listStudios,
  studioDraftLog,
  imagegenEnqueue,
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
  type StudioSummary,
} from "./api.ts";
import { anyLive, buildRequest, engineState, foldGroups } from "./jobs.ts";
import { draftFromProperties, draftKey, loadDraft, remappedOp, saveDraft, type ImagegenDraft } from "./draft.ts";
import { noteImagegenStatus } from "./available.ts";
import { GenerateForm, ModelSelect } from "./parts/GenerateForm.tsx";
import { JobList } from "./parts/JobList.tsx";
import { ResultCards, TrialSlot, resultsOf } from "./parts/ResultCards.tsx";
import { AttachAgentModal, type AttachOpts } from "./parts/AttachAgentModal.tsx";
import { DraftLog } from "./parts/DraftLog.tsx";
import { KnowledgeMemo } from "./parts/KnowledgeMemo.tsx";
import { StudioAgent } from "./parts/StudioAgent.tsx";
import { StudioHistory, type PictureActions } from "./parts/StudioHistory.tsx";
import { attachAgent } from "./attach.ts";
import { rememberStudio } from "./open.ts";
import { historyByPath, pressSeqOf, studioFromForm } from "./studioSync.ts";
import { useStudio } from "./useStudio.ts";
import "./imagegen.css";

/** Decision 2's cadence. Only ever runs while something is unfinished AND the tab is shown. */
const POLL_MS = 2000;

/** The picture history's page size. */
const HISTORY_PAGE = 24;

export function ImagegenView({
  paneId = "",
  studioId = null,
  active = true,
  headerActions,
}: {
  paneId?: string;
  /** The Agent's studio this pane edits; null is the studio-less pane (ADR 0100 decision 10). */
  studioId?: string | null;
  active?: boolean;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const running = useWorkspaceStore((s) => wsRunning(s.state));
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  const key = useMemo(() => draftKey(getTenant()), []);
  const [localDraft, setDraft] = useState<ImagegenDraft>(() => loadDraft(key));
  const studio = useStudio(studioId ?? "", { running: running && !!studioId });
  const draft = studioId ? studio.form : localDraft;
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
  const [busy, setBusy] = useState(false);
  const [zoomPath, setZoomPath] = useState<string | null>(null);
  const [attachOpen, setAttachOpen] = useState<"attach" | "replace" | null>(null);
  const [logOpen, setLogOpen] = useState(false);
  // The narrow pane's tab (ADR 0100 §4); ignored while the three columns fit.
  const [tab, setTab] = useState<"chat" | "form" | "out">("form");
  const [studios, setStudios] = useState<StudioSummary[]>([]);
  const [history, setHistory] = useState<HistoryItem[]>([]);
  const [historyBefore, setHistoryBefore] = useState("");
  // The bar's running segment fills against the clock, so the row needs a tick of its own.
  // It advances only alongside a poll, never on a timer of its own.
  const [now, setNow] = useState(() => Date.now());

  const patchLocal = useCallback(
    (p: Partial<ImagegenDraft>) => {
      setDraft((d) => {
        const next = { ...d, ...p };
        saveDraft(key, next);
        return next;
      });
    },
    [key],
  );
  const patchStudioForm = studio.patchForm;
  const patch = studioId ? patchStudioForm : patchLocal;

  // Remember the studio this browser has open, for the next plain "open image generation".
  useEffect(() => {
    if (studioId) rememberStudio(studioId);
  }, [studioId]);

  const readStudios = useCallback(async () => {
    try {
      const r = await listStudios();
      if (r && !r.error && Array.isArray(r.studios)) setStudios(r.studios);
    } catch {
      /* the picker keeps what it had */
    }
  }, []);

  const readHistory = useCallback(
    async (more = false) => {
      if (!studioId) return;
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

  // A draft written by "open in image generation" (the lightbox's properties bar) landed in
  // localStorage before this pane mounted. Re-reading on mount is what picks it up; the
  // storage event covers the pop-out case, where the writer is a different window.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key === key) setDraft(loadDraft(key));
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [key]);

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
    if (running && studioId) void readHistory(false);
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
  const results = useMemo(() => resultsOf(jobs.filter((j) => !j.trial)), [jobs]);
  const latestTrial = useMemo(() => resultsOf(jobs.filter((j) => j.trial))[0] ?? null, [jobs]);

  const submit = useCallback(
    async (trial: boolean) => {
      if (!draft.prompt.trim()) {
        toast(tr("imggen.no_prompt"), { kind: "info" });
        return;
      }
      setBusy(true);
      // ADR 0100 decision 9: with a studio, a press is one POST that records the version and
      // enqueues the studio's own draft — the form's pending edit is flushed first.
      if (studioId) {
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
        return;
      }
      try {
        const r = await imagegenEnqueue(buildRequest(draft, { trial, provider: provider?.id, model }));
        if (r?.error) {
          toast(errText(r.error) || tr("imggen.enqueue_failed"), { kind: "error" });
          return;
        }
        toast(tr("imggen.enqueued", { n: r?.jobs?.length ?? (trial ? 1 : draft.jobs) }), { kind: "info" });
        await readJobs();
      } catch {
        toast(tr("imggen.enqueue_failed"), { kind: "error" });
      } finally {
        setBusy(false);
      }
    },
    [draft, provider, model, readJobs, toast, tr, studioId, studio],
  );

  // The finished pictures of this studio arrive through the job list; re-read the history page
  // when the count of finished jobs moves, never on a timer of its own.
  const doneCount = useMemo(() => jobs.filter((j) => j.state === "done").length, [jobs]);
  useEffect(() => {
    if (studioId && doneCount) void readHistory(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doneCount]);

  const openStudio = useCallback(
    (id: string | null) => {
      if (id !== studioId) setPaneTarget(paneId, { content: { kind: "imagegen", studioId: id } });
    },
    [paneId, studioId, setPaneTarget],
  );

  const attach = useCallback(
    async (o: AttachOpts): Promise<boolean> => {
      const r = await attachAgent({
        studioId,
        // The pane's resolved row, not the draft's possibly-stale pick (decision 2: a trial never
        // falls back to another provider, so the studio must name the one on screen).
        draft: () => studioFromForm({ ...draft, providerId: provider?.id || "" }),
        opts: o,
        replacing: attachOpen === "replace" ? studio.studio?.session : undefined,
      });
      if (r.error) toast(r.error, { kind: "error" });
      if (r.session) void refreshSessions();
      if (r.studioId) {
        void readStudios();
        if (r.studioId !== studioId) openStudio(r.studioId);
        else void studio.reload();
      }
      return !!r.session;
    },
    [studioId, draft, provider, attachOpen, studio, toast, refreshSessions, readStudios, openStudio],
  );

  const removeStudio = useCallback(async () => {
    if (!studioId) return;
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
    rememberStudio(null);
    void readStudios();
    openStudio(null);
  }, [studioId, confirm, tr, toast, readStudios, openStudio]);

  // "Back to this picture's settings": the press its version names, through the same rewind
  // as the edit history. The log page on screen may not reach that far back, so older pages
  // are read until it does. A picture with no version (made before the studio, or elsewhere)
  // falls back to its recovered properties, written as the member's own edit.
  const restorePicture = useCallback(
    async (path: string, version?: string) => {
      if (!studioId) return;
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

  const pictureActions: PictureActions | undefined = studioId
    ? {
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
      }
    : undefined;

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

  const close = useCallback(() => setZoomPath(null), []);
  // Back closes the lightbox instead of the pane; the host owns that entry, not the
  // lightbox (the same rule the gallery and the mirror follow).
  useBackClose(zoomPath ? close : undefined, !!zoomPath);

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

  const session = studio.studio?.session || "";
  const studioTitle = (s: { title?: string; id: string }) => s.title || tr("imggen.studio_untitled", { id: s.id.slice(0, 8) });
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
      {...(studioId
        ? { locks: studio.locks, onToggleLock: studio.toggleLock, highlight: studio.highlight as ReadonlySet<string> }
        : {})}
      familyExtra={model ? <KnowledgeMemo family={model.family} model={model.id} /> : null}
    />
  );

  return (
    <div className="igen">
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
            <IconButton
              icon="refresh"
              label={tr("imggen.refresh")}
              onClick={() => {
                void readStatus();
                void readJobs();
              }}
            />
            {headerActions}
          </>
        }
      >
        <span className="view-title">
          <Icon name="wand" /> {tr("imggen.title")}
        </span>
        {/* The studio this pane edits (ADR 0100 decision 10). "No studio" is the pane of old,
            its draft in this browser only. */}
        <select
          className="ds-select igen-studio-pick"
          aria-label={tr("imggen.studio_pick")}
          value={studioId ?? ""}
          onChange={(e) => openStudio(e.target.value || null)}
        >
          <option value="">{tr("imggen.studio_none")}</option>
          {studioId && !studios.some((s) => s.id === studioId) && studio.studio && (
            <option value={studioId}>{studioTitle(studio.studio)}</option>
          )}
          {studios.map((s) => (
            <option key={s.id} value={s.id}>
              {studioTitle(s)}
              {s.session ? " ●" : ""}
            </option>
          ))}
        </select>
        {/* The model is the member's (decision 4); its place in the head says so. */}
        <span className="igen-head-model">
          <ModelSelect draft={draft} patch={patch} fleetProviders={fleetProviderList} provider={provider} models={models} compact />
        </span>
        <span className={"igen-engine igen-engine-" + state} title={engineLine}>
          <span className="igen-dot" />
          {state === "ready"
            ? tr("imggen.engine_ready")
            : state === "cold"
              ? tr("imggen.engine_cold")
              : state === "starting"
                ? tr("imggen.engine_starting")
                : tr("imggen.engine_unavailable")}
        </span>
        {/* Only when it says something the chip does not: "ready" twice is noise, while the
            cold start's minutes and the unavailable code's reason are the point. */}
        {state !== "ready" && <span className="igen-engine-hint muted">{engineLine}</span>}
        {/* Never "$0.00": comfy's CostUSD is 0 by construction and the attribution lives in
            the administrator's hourly table (decision 10). */}
        <span className="igen-cost muted">{tr("imggen.cost_note")}</span>
        {studioId && studio.studio && (
          <details className="igen-studio-menu">
            <summary title={tr("imggen.studio_settings")}>
              <Icon name="gear" />
            </summary>
            <div className="igen-studio-menu-body">
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
      {studioId && studio.failed && !studio.studio ? (
        <EmptyState icon="warning" title={tr("imggen.studio_read_failed")} hint={studio.failed}>
          <button type="button" className="ui-btn" onClick={() => openStudio(null)}>
            {tr("imggen.studio_open_none")}
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
                signal={studioId ? studio.signal : undefined}
                onAttach={() => setAttachOpen("attach")}
                onReplace={() => setAttachOpen("replace")}
              />
            </div>
            <div className="igen-col-form">
              {studioId && (
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
              )}
              {studioId && logOpen && (
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
              <TrialSlot item={latestTrial} onZoom={setZoomPath} onUseSeed={useSeed} />
              <JobList
                rows={rows}
                queuePaused={queuePaused}
                queued={caps.queued}
                queueMax={caps.queueMax}
                now={now}
                onGroupOp={(id, op) => void groupOp(id, op)}
                onQueueOp={(op) => void queueOp(op)}
                onCancelJob={(id) => void cancelJob(id)}
              />
              <ResultCards
                items={results}
                onZoom={setZoomPath}
                actions={pictureActions}
                onAgain={(item, sameSeed) => {
                  const seed = item.file.seed ?? item.job.seed ?? null;
                  if (sameSeed && seed != null) patch({ seed: String(seed), seedPolicy: "fixed" });
                  else patch({ seedPolicy: "random" });
                  void submit(true);
                }}
              />
              {studioId && pictureActions && (
                <StudioHistory
                  items={history}
                  hasMore={!!historyBefore}
                  onMore={() => void readHistory(true)}
                  onZoom={setZoomPath}
                  actions={pictureActions}
                />
              )}
            </div>
          </div>
        </>
      )}
      {attachOpen && (
        <AttachAgentModal
          replacing={attachOpen === "replace" ? session : undefined}
          onClose={() => setAttachOpen(null)}
          onAttach={attach}
        />
      )}
      {zoomPath &&
        createPortal(
          <ImageLightbox src={downloadURL(zoomPath)} path={zoomPath} onClose={close} />,
          document.body,
        )}
    </div>
  );
}
