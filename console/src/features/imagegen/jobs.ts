// jobs — the pure half of the queue view (ADR 0081 decisions 8, 11, 12): folding the job
// list into group rows, turning counts and a rolling average into a bar, and building the
// request the form submits.
//
// It is a separate module from the view because all of it is arithmetic with a wrong answer
// that looks plausible — a bar that claims 100 % on a job that has not returned, a trial
// counted into a batch's progress, a `params.steps` dropped from a trial so the sidecar
// cannot say what the batch would have run.
import type { EnqueueRequest, ImagegenModel, ImagegenStatus, Job, JobGroup, SeedPolicy } from "./wire.ts";
import { fleetProvider } from "./wire.ts";
import { draftParams, type ImagegenDraft } from "./draft.ts";

/** States that are still going to change. The poller runs only while one of these exists. */
const LIVE: Job["state"][] = ["queued", "waking", "uploading", "running", "fetching"];

export const isLive = (j: Job): boolean => LIVE.includes(j.state);

export const anyLive = (jobs: Job[] | undefined): boolean => (jobs || []).some(isLive);

/**
 * One line of the job list. A group of forty is ONE row with counts (decision 8); a job
 * with no group — a trial, or an older Agent that does not send one — is its own row so it
 * is never invisible.
 */
export interface JobRow {
  key: string;
  group: JobGroup | null;
  /** Every job of this row, newest first. A single-job row holds exactly one. */
  jobs: Job[];
  /** The job in flight, when there is one. Serial, so at most one per row. */
  running: Job | null;
  done: number;
  failed: number;
  total: number;
  label: string;
  trial: boolean;
}

const startedMs = (j: Job): number => {
  const t = Date.parse(j.started_at || j.finished_at || "");
  return Number.isFinite(t) ? t : 0;
};

/**
 * Fold jobs into rows, newest first.
 *
 * `groups` is the Agent's word on counts and state; when a group id appears on a job but
 * not in `groups` the row is still built from the jobs themselves, because a batch that
 * vanishes from the list because one array lagged the other is worse than an estimate.
 */
export function foldGroups(jobs: Job[] | undefined, groups: JobGroup[] | undefined): JobRow[] {
  const byId = new Map((groups || []).map((g) => [g.id, g] as const));
  const rows = new Map<string, JobRow>();
  const order: string[] = [];
  for (const j of jobs || []) {
    if (!j || !j.id) continue;
    const key = j.group || `job:${j.id}`;
    let row = rows.get(key);
    if (!row) {
      const g = j.group ? (byId.get(j.group) ?? null) : null;
      row = {
        key,
        group: g,
        jobs: [],
        running: null,
        done: 0,
        failed: 0,
        total: 0,
        label: j.label || g?.label || "",
        trial: !!j.trial,
      };
      rows.set(key, row);
      order.push(key);
    }
    row.jobs.push(j);
    row.total++;
    if (j.state === "done") row.done++;
    else if (j.state === "failed") row.failed++;
    if (isLive(j) && j.state !== "queued") row.running = j;
    if (!row.label && j.label) row.label = j.label;
  }
  // The Agent's counts win where it sent them: its list is capped at the last 500 finished
  // jobs, so a long-running group's `done` outlives the jobs the Console can still see.
  for (const row of rows.values()) {
    const g = row.group;
    if (!g) continue;
    if (typeof g.total === "number" && g.total > row.total) row.total = g.total;
    if (typeof g.done === "number" && g.done > row.done) row.done = g.done;
    if (typeof g.failed === "number" && g.failed > row.failed) row.failed = g.failed;
  }
  return order.map((k) => rows.get(k)!);
}

/** What the header says about the engine box, in the member's words (decision 10). */
export type EngineState = "ready" | "cold" | "starting" | "unavailable";

/**
 * Which of the four the header shows.
 *
 * `starting` outranks everything because a job is visibly in `waking` and that is the one
 * state a person needs an explanation for. `ready` means the SELECTED model is warm — a
 * different warm checkpoint does not make this one's first picture fast, and saying "ready"
 * then waiting five minutes is the lie this state exists to avoid.
 */
export function engineState(
  status: ImagegenStatus | null,
  jobs: Job[] | undefined,
  model: ImagegenModel | null,
): EngineState {
  if ((jobs || []).some((j) => j.state === "waking")) return "starting";
  if (!status || !status.ready || !fleetProvider(status)) return "unavailable";
  if (model?.warm) return "ready";
  return "cold";
}

export interface BarSegments {
  /** Fractions of the whole bar, 0..1, that never sum above 1. */
  done: number;
  failed: number;
  running: number;
}

/** How far the running segment may fill on time alone. It stops here and waits for the job
 *  to actually end, so the bar never claims a completion nobody has seen (decision 12). */
export const RUNNING_CAP = 0.95;

/**
 * The three segments of a group's bar. The running one fills by elapsed time against
 * `typical_ms` — an estimate, and labelled as one; there is no per-step progress before P1
 * because upstream publishes it only on the websocket.
 */
export function barSegments(row: JobRow, now: number): BarSegments {
  const total = Math.max(1, row.total);
  const done = Math.min(1, row.done / total);
  const failed = Math.min(1 - done, row.failed / total);
  let running = 0;
  const r = row.running;
  if (r && r.state === "running") {
    const typical = r.typical_ms || 0;
    const started = startedMs(r);
    if (typical > 0 && started > 0) {
      const frac = Math.max(0, Math.min(RUNNING_CAP, (now - started) / typical));
      running = frac / total;
    }
  }
  return { done, failed, running: Math.max(0, Math.min(1 - done - failed, running)) };
}

/**
 * Milliseconds left: the Agent's `eta_ms` when it sent one, else remaining × `typical_ms`.
 * Null when neither is knowable — no number at all beats a fabricated one on a screen whose
 * whole purpose is deciding whether to wait.
 */
export function etaMs(row: JobRow): number | null {
  if (row.group?.eta_ms != null && row.group.eta_ms >= 0) return row.group.eta_ms;
  const typical = row.running?.typical_ms || row.jobs.find((j) => j.typical_ms)?.typical_ms || 0;
  if (!typical) return null;
  const left = Math.max(0, row.total - row.done - row.failed);
  return left * typical;
}

/** Coarse "about N min" / "about N s". Anything under 10 s is "a moment" rather than a
 *  countdown that is wrong by the time it is read. */
export function etaBucket(ms: number | null): { unit: "min" | "sec" | "soon"; value: number } {
  if (ms == null) return { unit: "soon", value: 0 };
  if (ms < 10_000) return { unit: "soon", value: 0 };
  if (ms < 90_000) return { unit: "sec", value: Math.round(ms / 1000) };
  return { unit: "min", value: Math.max(1, Math.round(ms / 60_000)) };
}

/** The seeds a policy produces for N jobs. `random` yields nothing — the Agent draws each
 *  one and reports it back, which is the only way a random run is reproducible. */
export function seedsFor(policy: SeedPolicy, base: number | null, n: number): (number | null)[] {
  const count = Math.max(0, Math.floor(n));
  if (policy === "random" || base == null || !Number.isFinite(base)) return Array(count).fill(null);
  if (policy === "fixed") return Array(count).fill(base);
  return Array.from({ length: count }, (_, i) => base + i);
}

/**
 * The body of `POST /imagegen/jobs` for this form.
 *
 * A trial differs in exactly what decision 11 lists: one job, one picture, `trial: true`.
 * The form's own steps stay on the request as `params.steps` — the Agent substitutes the
 * family's trial value for the run and records both, so the sidecar can say what the batch
 * would do. Size, seed, cfg, sampler, LoRAs and the negative are the form's untouched,
 * because a trial at another size previews a different picture.
 */
export function buildRequest(d: ImagegenDraft, opts: { trial?: boolean; provider?: string } = {}): EnqueueRequest {
  const trial = !!opts.trial;
  const seed = Number(d.seed);
  const hasSeed = d.seed.trim() !== "" && Number.isFinite(seed);
  const body: EnqueueRequest = {
    prompt: d.prompt,
    ...(opts.provider ? { provider: opts.provider } : {}),
    ...(d.model ? { model: d.model } : {}),
    ...(d.negative.trim() ? { negative_prompt: d.negative } : {}),
    ...(d.size ? { size: d.size } : {}),
    ...(d.op && d.op !== "generate" ? { op: d.op } : {}),
    ...(d.op !== "generate" && d.inputs.length ? { inputs: d.inputs } : {}),
    ...(d.op !== "generate" ? { strength: d.strength } : {}),
    ...(d.loras.length ? { loras: d.loras } : {}),
    ...(d.outDir.trim() ? { out_dir: d.outDir.trim() } : {}),
    ...(d.label.trim() ? { label: d.label.trim() } : {}),
    jobs: trial ? 1 : Math.max(1, d.jobs),
    count: trial ? 1 : Math.max(1, d.batchSize),
    seed_policy: trial ? (hasSeed ? "fixed" : "random") : d.seedPolicy,
    ...(hasSeed && (trial || d.seedPolicy !== "random") ? { seed } : {}),
    ...(trial ? { trial: true } : {}),
    ...(trial && d.fullSteps ? { trial_full_steps: true } : {}),
  };
  const params = draftParams(d);
  if (params) body.params = params;
  return body;
}
