// ImagegenView — the image-generation studio (ADR 0081). A pane like the others: a ViewHead,
// the tabbed grid's header actions, and no state of its own that a tab switch could lose —
// the form is a localStorage draft and the queue is the Agent's (decision 6).
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
import { useT } from "../../lib/i18n/index.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { ImageLightbox } from "../viewer/ImageLightbox.tsx";
import {
  fleetProvider,
  imagegenCancelJob,
  imagegenEnqueue,
  imagegenGroupOp,
  imagegenJobs,
  imagegenQueueOp,
  imagegenStatus,
  type GroupOp,
  type ImagegenStatus,
  type Job,
  type JobGroup,
  type Knob,
} from "./api.ts";
import { anyLive, buildRequest, engineState, foldGroups } from "./jobs.ts";
import { draftKey, loadDraft, saveDraft, type ImagegenDraft } from "./draft.ts";
import { GenerateForm } from "./parts/GenerateForm.tsx";
import { JobList } from "./parts/JobList.tsx";
import { PromptHelpModal } from "./parts/PromptHelpModal.tsx";
import { ResultCards, TrialSlot, resultsOf } from "./parts/ResultCards.tsx";
import "./imagegen.css";

/** Decision 2's cadence. Only ever runs while something is unfinished AND the tab is shown. */
const POLL_MS = 2000;

export function ImagegenView({ headerActions }: { headerActions?: ReactNode }) {
  const tr = useT();
  const toast = useToast();
  const running = useWorkspaceStore((s) => wsRunning(s.state));

  const key = useMemo(() => draftKey(getTenant()), []);
  const [draft, setDraft] = useState<ImagegenDraft>(() => loadDraft(key));
  const [status, setStatus] = useState<ImagegenStatus | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [groups, setGroups] = useState<JobGroup[]>([]);
  const [queuePaused, setQueuePaused] = useState(false);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [zoomPath, setZoomPath] = useState<string | null>(null);
  // The bar's running segment fills against the clock, so the row needs a tick of its own.
  // It advances only alongside a poll, never on a timer of its own.
  const [now, setNow] = useState(() => Date.now());

  const patch = useCallback(
    (p: Partial<ImagegenDraft>) => {
      setDraft((d) => {
        const next = { ...d, ...p };
        saveDraft(key, next);
        return next;
      });
    },
    [key],
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
      setQueuePaused(!!r.queue_paused);
      setNow(Date.now());
    } catch {
      /* a transient 502 while the agent restarts keeps the list on screen */
    }
  }, []);

  useEffect(() => {
    if (!running) return;
    void readStatus();
    void readJobs();
  }, [running, readStatus, readJobs]);

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

  const provider = fleetProvider(status);
  const models = provider?.models || [];
  const loras = provider?.loras || [];
  const model = models.find((m) => m.id === draft.model) || null;
  const samplers = status?.samplers || provider?.samplers || [];
  const schedulers = status?.schedulers || provider?.schedulers || [];
  const loraWeightMax = status?.lora_weight_max || provider?.lora_weight_max || 2;
  const alwaysNegative = status?.negative_always || "";
  const negativeReaches = !model?.knobs || model.knobs.includes("negative" as Knob);
  const state = engineState(status, jobs, model);

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
      try {
        const r = await imagegenEnqueue(buildRequest(draft, { trial, provider: provider?.id }));
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
    [draft, provider, readJobs, toast, tr],
  );

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
          ? status?.cold_ms
            ? tr("imggen.engine_cold_hint", { min: Math.max(1, Math.round(status.cold_ms / 60_000)) })
            : tr("imggen.engine_cold_hint_unknown")
          : tr("imggen.engine_ready");

  return (
    <div className="igen">
      <ViewHead
        actions={
          <>
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
        <span className="igen-engine-hint muted">{engineLine}</span>
        {/* Never "$0.00": comfy's CostUSD is 0 by construction and the attribution lives in
            the administrator's hourly table (decision 10). */}
        <span className="igen-cost muted">{tr("imggen.cost_note")}</span>
      </ViewHead>
      {failed && !status ? (
        <EmptyState icon="warning" title={tr("imggen.engine_unavailable")} />
      ) : (
        <div className="igen-body">
          <div className="igen-col-form">
            <GenerateForm
              draft={draft}
              patch={patch}
              models={models}
              loras={loras}
              model={model}
              samplers={samplers}
              schedulers={schedulers}
              loraWeightMax={loraWeightMax}
              alwaysNegative={alwaysNegative}
              busy={busy || state === "unavailable"}
              onTrial={() => void submit(true)}
              onEnqueue={() => void submit(false)}
              onPromptHelp={() => setHelpOpen(true)}
            />
          </div>
          <div className="igen-col-out">
            <TrialSlot item={latestTrial} onZoom={setZoomPath} onUseSeed={useSeed} />
            <JobList
              rows={rows}
              queuePaused={queuePaused}
              now={now}
              onGroupOp={(id, op) => void groupOp(id, op)}
              onQueueOp={(op) => void queueOp(op)}
              onCancelJob={(id) => void cancelJob(id)}
            />
            <ResultCards
              items={results}
              onZoom={setZoomPath}
              onAgain={(item, sameSeed) => {
                const seed = item.file.seed ?? item.job.seed ?? null;
                if (sameSeed && seed != null) patch({ seed: String(seed), seedPolicy: "fixed" });
                else patch({ seedPolicy: "random" });
                void submit(true);
              }}
            />
          </div>
        </div>
      )}
      {helpOpen && (
        <PromptHelpModal
          model={model}
          loras={loras.filter((l) => draft.loras.some((x) => x.name === l.name))}
          alwaysNegative={alwaysNegative}
          negativeReaches={negativeReaches}
          onClose={() => setHelpOpen(false)}
          onUse={(prompt, negative) => {
            patch(negative === null ? { prompt } : { prompt, negative });
            setHelpOpen(false);
          }}
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
