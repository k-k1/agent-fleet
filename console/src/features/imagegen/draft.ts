// draft — the form's state, and its round trip through localStorage (ADR 0081 decision 6).
//
// Why localStorage and not the pane content: the layout store is not a place for a 2 kB
// prompt (it is written on every geometry change and validated on every load), and a draft
// is per browser, not per layout. Why not React state alone: a reload mid-sentence is the
// one thing a mass-production screen must not punish.
//
// Everything read back is untrusted JSON — the same stance `layout/migrate.ts` takes. A
// field that does not survive its check is dropped to the default rather than repaired,
// because a repaired number here becomes a request the Agent refuses with 400 and the user
// has no idea which field lied.
import type { EngineParams, ImageProperties, LoraRef, SeedPolicy } from "./wire.ts";

export interface ImagegenDraft {
  /**
   * Which FLEET ROW drives this pane (ADR 0082 P1, the pane's answer to the ADR's own
   * unresolved question 2: "does a member need to pick among N fleet rows, or does 'the
   * first one' suffice?"). "" means no explicit choice — the pane falls back to the first
   * ready fleet row, which is ALSO what every single-engine deployment already saw, so this
   * is additive and changes nothing when there is only one row to choose from.
   *
   * A stale id (the row was removed, or this is a different deployment's draft) is read the
   * same way as "" by the reader (ImagegenView), never as an error — the same rule `model`
   * already follows one field up.
   */
  providerId: string;
  model: string;
  prompt: string;
  /** The member's own negative. The row's and the deployment's are chips, not text. */
  negative: string;
  size: string;
  /** Empty string = "the model's default", which is what the placeholder shows. */
  steps: string;
  cfg: string;
  sampler: string;
  scheduler: string;
  seedPolicy: SeedPolicy;
  /** Only meaningful for `fixed` / `sequence`. */
  seed: string;
  loras: LoraRef[];
  /** N — the number of JOBS, one picture each (decision 8). */
  jobs: number;
  /** ComfyUI `batch_size`, the advanced field. Ceiling 4. */
  batchSize: number;
  op: string;
  inputs: string[];
  strength: number;
  outDir: string;
  label: string;
  /** Run a trial at the form's steps instead of the family's reduced ones (decision 11). */
  fullSteps: boolean;
}

export const MAX_JOBS = 200;
export const MAX_BATCH = 4;
export const OPS = ["generate", "edit", "inpaint"];

export const emptyDraft = (): ImagegenDraft => ({
  providerId: "",
  model: "",
  prompt: "",
  negative: "",
  size: "",
  steps: "",
  cfg: "",
  sampler: "",
  scheduler: "",
  seedPolicy: "random",
  seed: "",
  loras: [],
  jobs: 1,
  batchSize: 1,
  op: "generate",
  inputs: [],
  strength: 0.6,
  outDir: "",
  label: "",
  fullSteps: false,
});

/** One draft per workspace. The tenant slug is the browser's name for the workspace; a
 *  member with one membership still gets a stable key ("" → `default`). */
export const draftKey = (workspace: string): string => `af.imagegen-draft.${workspace || "default"}`;

const str = (v: unknown, max: number): string => (typeof v === "string" ? v.slice(0, max) : "");

const int = (v: unknown, lo: number, hi: number, dflt: number): number => {
  const n = typeof v === "number" ? v : Number(v);
  if (!Number.isFinite(n)) return dflt;
  return Math.min(hi, Math.max(lo, Math.round(n)));
};

const numText = (v: unknown): string => {
  if (typeof v === "number" && Number.isFinite(v)) return String(v);
  if (typeof v !== "string") return "";
  return /^-?\d*\.?\d*$/.test(v) ? v.slice(0, 20) : "";
};

const loras = (v: unknown): LoraRef[] => {
  if (!Array.isArray(v)) return [];
  const out: LoraRef[] = [];
  for (const raw of v.slice(0, 16)) {
    const name = str((raw as LoraRef | undefined)?.name, 200);
    if (!name) continue;
    const w = (raw as LoraRef).weight;
    out.push(typeof w === "number" && Number.isFinite(w) ? { name, weight: w } : { name });
  }
  return out;
};

const paths = (v: unknown): string[] =>
  Array.isArray(v) ? (v.map((p) => str(p, 512)).filter(Boolean) as string[]).slice(0, 8) : [];

/** Parse a stored draft. Anything unreadable yields the empty draft, never a throw. */
export function parseDraft(raw: string | null | undefined): ImagegenDraft {
  const base = emptyDraft();
  if (!raw) return base;
  let p: Record<string, unknown>;
  try {
    p = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return base;
  }
  if (!p || typeof p !== "object") return base;
  const policy = p.seedPolicy;
  return {
    providerId: str(p.providerId, 200),
    model: str(p.model, 200),
    prompt: str(p.prompt, 8000),
    negative: str(p.negative, 4000),
    size: /^\d{1,5}x\d{1,5}$/.test(str(p.size, 12)) ? str(p.size, 12) : "",
    steps: numText(p.steps),
    cfg: numText(p.cfg),
    sampler: str(p.sampler, 60),
    scheduler: str(p.scheduler, 60),
    seedPolicy: policy === "fixed" || policy === "sequence" ? policy : "random",
    seed: numText(p.seed),
    loras: loras(p.loras),
    jobs: int(p.jobs, 1, MAX_JOBS, 1),
    batchSize: int(p.batchSize, 1, MAX_BATCH, 1),
    op: OPS.includes(str(p.op, 20)) ? str(p.op, 20) : "generate",
    inputs: paths(p.inputs),
    strength: Math.min(1, Math.max(0, typeof p.strength === "number" ? p.strength : base.strength)),
    outDir: str(p.outDir, 512),
    label: str(p.label, 200),
    fullSteps: p.fullSteps === true,
  };
}

export const serializeDraft = (d: ImagegenDraft): string => JSON.stringify(d);

export function loadDraft(key: string): ImagegenDraft {
  try {
    return parseDraft(localStorage.getItem(key));
  } catch {
    return emptyDraft();
  }
}

export function saveDraft(key: string, d: ImagegenDraft): void {
  try {
    localStorage.setItem(key, serializeDraft(d));
  } catch {
    /* a full or blocked store loses the draft, never the form */
  }
}

/**
 * The fields a picture's properties can put back into a form ("open in image generation",
 * decision 3). Only what was RESOLVED: a `source: "none"` picture contributes nothing, and
 * a field the PNG chunk did not carry must not overwrite what the user has typed.
 *
 * The output folder, the label, N and the batch size are deliberately not restored — they
 * describe the run, not the picture, and re-running one image into someone's 40-job label
 * would be a surprise.
 */
export function draftFromProperties(base: ImagegenDraft, props: ImageProperties): ImagegenDraft {
  if (props.source === "none") return base;
  const next: ImagegenDraft = { ...base };
  if (props.model) next.model = props.model;
  if (props.prompt) next.prompt = props.prompt;
  if (props.negative) next.negative = props.negative;
  if (props.size) next.size = props.size;
  // The sampler knobs are nested in `params` on this route, the same shape the request
  // carries them in — not flattened alongside model and seed.
  const pm = props.params;
  if (pm?.steps != null) next.steps = String(pm.steps);
  if (pm?.cfg != null) next.cfg = String(pm.cfg);
  if (pm?.sampler) next.sampler = pm.sampler;
  if (pm?.scheduler) next.scheduler = pm.scheduler;
  if (props.loras?.length) next.loras = props.loras.map((l) => ({ name: l.name, ...(l.weight != null ? { weight: l.weight } : {}) }));
  if (props.op) next.op = OPS.includes(props.op) ? props.op : base.op;
  if (props.strength != null) next.strength = props.strength;
  // A recovered seed is the reason this button exists: pin it rather than leaving the
  // policy on `random`, which would reproduce everything except the picture.
  if (props.seed != null) {
    next.seed = String(props.seed);
    next.seedPolicy = "fixed";
  }
  return next;
}

/** The `params` overlay for a request: only the fields the user actually typed. An empty
 *  field means "the model's default", and sending 0 for it would be a different picture. */
export function draftParams(d: ImagegenDraft): EngineParams | undefined {
  const p: EngineParams = {};
  const steps = Number(d.steps);
  const cfg = Number(d.cfg);
  if (d.steps.trim() && Number.isFinite(steps)) p.steps = steps;
  if (d.cfg.trim() && Number.isFinite(cfg)) p.cfg = cfg;
  if (d.sampler) p.sampler = d.sampler;
  if (d.scheduler) p.scheduler = d.scheduler;
  return Object.keys(p).length ? p : undefined;
}
