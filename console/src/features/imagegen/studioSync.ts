// studioSync — the pure half of the studio pane (ADR 0100): the form's draft against the
// studio's, the per-field locks, the agent highlight, the edit log's versions, the one-line
// signal a composer send carries, and which kinds may attach.
//
// The form keeps ADR 0081's `ImagegenDraft` (strings for the numeric fields, so a half-typed
// "7." survives), and the studio stores POST /imagegen/jobs' own keys. The two are mapped here
// and nowhere else. Imports are wire.ts, draft.ts and the agent registry only — api.ts reads
// localStorage at module scope and the node test project cannot load it.
import type { SessionKind } from "../../types/session.ts";
import { AGENTS, repoLaunchKinds } from "../../agents/registry.ts";
import { STUDIO_SIGNAL_PREFIX, STUDIO_SIGNAL_SUFFIX } from "../mirror/transcript/model.ts";
import { MAX_BATCH, MAX_JOBS, OPS, emptyDraft, type ImagegenDraft } from "./draft.ts";
import { STUDIO_AGENT_FIELDS, type DraftChange, type DraftLogEntry, type HistoryItem, type StudioDraft, type StudioPatch } from "./wire.ts";

export type StudioKey = keyof StudioDraft;

/** Which studio key each form field lives under. The four sampler fields share `params`. */
export const FORM_TO_STUDIO: Record<keyof ImagegenDraft, StudioKey> = {
  providerId: "provider",
  model: "model",
  prompt: "prompt",
  negative: "negativePrompt",
  size: "size",
  steps: "params",
  cfg: "params",
  sampler: "params",
  scheduler: "params",
  seedPolicy: "seed_policy",
  seed: "seed",
  loras: "loras",
  jobs: "jobs",
  batchSize: "count",
  op: "op",
  inputs: "inputs",
  mask: "mask",
  strength: "strength",
  outDir: "out_dir",
  label: "label",
  fullSteps: "full_steps",
};

/** The keys a member can lock against the agent: the agent's own fields, less its proposal. */
export const LOCKABLE: readonly StudioKey[] = STUDIO_AGENT_FIELDS.filter((k) => k !== "suggest_model");

export const isLockable = (k: string): k is StudioKey => (LOCKABLE as readonly string[]).includes(k);

const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const text = (v: unknown): string => (typeof v === "string" ? v : "");

/** The studio's draft as the form edits it. Absent fields take the form's defaults. */
export function formFromStudio(d: StudioDraft | null | undefined): ImagegenDraft {
  const base = emptyDraft();
  if (!d) return base;
  const p = d.params || {};
  const policy = d.seed_policy;
  return {
    providerId: text(d.provider),
    model: text(d.model),
    prompt: text(d.prompt),
    negative: text(d.negativePrompt),
    size: text(d.size),
    steps: num(p.steps) != null ? String(p.steps) : "",
    cfg: num(p.cfg) != null ? String(p.cfg) : "",
    sampler: text(p.sampler),
    scheduler: text(p.scheduler),
    seedPolicy: policy === "fixed" || policy === "sequence" ? policy : "random",
    seed: num(d.seed) != null ? String(d.seed) : "",
    loras: Array.isArray(d.loras) ? d.loras.filter((l) => l && typeof l.name === "string") : [],
    jobs: Math.min(MAX_JOBS, Math.max(1, num(d.jobs) ?? 1)),
    batchSize: Math.min(MAX_BATCH, Math.max(1, num(d.count) ?? 1)),
    op: OPS.includes(text(d.op)) ? text(d.op) : "generate",
    inputs: Array.isArray(d.inputs) ? d.inputs.filter((s) => typeof s === "string" && s) : [],
    mask: text(d.mask),
    strength: num(d.strength) ?? base.strength,
    outDir: text(d.out_dir),
    label: text(d.label),
    fullSteps: d.full_steps === true,
  };
}

/**
 * The form as a studio draft. A field at the form's default is left out rather than written,
 * so an untouched form is an empty draft and a half-typed number (`"7."` → 7, `"abc"` → absent)
 * never becomes a value the member did not see.
 *
 * `suggest_model` is not a form field; the caller keeps the studio's.
 */
export function studioFromForm(f: ImagegenDraft): StudioDraft {
  const d: StudioDraft = {};
  const n = (s: string): number | undefined => {
    if (!s.trim()) return undefined;
    const v = Number(s);
    return Number.isFinite(v) ? v : undefined;
  };
  if (f.providerId) d.provider = f.providerId;
  if (f.model) d.model = f.model;
  if (f.op && f.op !== "generate") d.op = f.op;
  if (f.prompt) d.prompt = f.prompt;
  if (f.negative) d.negativePrompt = f.negative;
  if (f.size) d.size = f.size;
  const params: NonNullable<StudioDraft["params"]> = {};
  const steps = n(f.steps);
  const cfg = n(f.cfg);
  if (steps != null) params.steps = steps;
  if (cfg != null) params.cfg = cfg;
  if (f.sampler) params.sampler = f.sampler;
  if (f.scheduler) params.scheduler = f.scheduler;
  if (Object.keys(params).length) d.params = params;
  if (f.seedPolicy !== "random") d.seed_policy = f.seedPolicy;
  const seed = n(f.seed);
  if (seed != null) d.seed = seed;
  if (f.loras.length) d.loras = f.loras;
  if (f.jobs > 1) d.jobs = f.jobs;
  if (f.batchSize > 1) d.count = f.batchSize;
  if (f.inputs.length) d.inputs = f.inputs;
  if (f.mask) d.mask = f.mask;
  if (f.strength !== emptyDraft().strength) d.strength = f.strength;
  if (f.outDir) d.out_dir = f.outDir;
  if (f.label) d.label = f.label;
  if (f.fullSteps) d.full_steps = true;
  return d;
}

// Structural equality over JSON values: key order does not matter, undefined equals absent.
function same(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a == null || b == null) return a == null && b == null;
  if (typeof a !== "object" || typeof b !== "object") return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a)) {
    const bb = b as unknown[];
    return a.length === bb.length && a.every((v, i) => same(v, bb[i]));
  }
  const ao = a as Record<string, unknown>;
  const bo = b as Record<string, unknown>;
  const keys = new Set([...Object.keys(ao), ...Object.keys(bo)]);
  for (const k of keys) if (!same(ao[k], bo[k])) return false;
  return true;
}

/** The top-level keys whose values differ between two drafts. */
export function changedKeys(a: StudioDraft | null | undefined, b: StudioDraft | null | undefined): StudioKey[] {
  const ao = (a || {}) as Record<string, unknown>;
  const bo = (b || {}) as Record<string, unknown>;
  const keys = new Set([...Object.keys(ao), ...Object.keys(bo)]);
  return [...keys].filter((k) => !same(ao[k], bo[k])).sort() as StudioKey[];
}

/**
 * The merge patch that turns `base` (the studio as last read) into `next` (the form's draft):
 * changed keys carry their new value, keys the form cleared carry null. Null for "nothing to
 * send", so the debounce does not PUT an empty patch and move `updated_at` for nothing.
 *
 * `base` is first read the way the form reads it, so a value the form cannot tell from its
 * default (an explicit `op: "generate"`, `strength: 0.6`) is not sent back as null — that would
 * log a member's edit nobody made.
 *
 * `params` is merged one level deep on the Agent (RFC 7386), so a knob the member cleared must be
 * named with an explicit null: sending the remaining knobs alone leaves the cleared one in place.
 */
export function draftPatch(base: StudioDraft | null | undefined, next: StudioDraft): StudioPatch["draft"] | null {
  const norm = studioFromForm(formFromStudio(base));
  const keys = changedKeys(norm, next).filter((k) => k !== "suggest_model");
  if (!keys.length) return null;
  const out: Record<string, unknown> = {};
  for (const k of keys) {
    if (k === "params") {
      const p: Record<string, unknown> = { ...(next.params || {}) };
      for (const sub of Object.keys(norm.params || {})) if (!(sub in p)) p[sub] = null;
      out.params = p;
      continue;
    }
    out[k] = next[k] === undefined ? null : next[k];
  }
  return out as StudioPatch["draft"];
}

/**
 * The form rebuilt on a studio that moved while the member's edit was unsent (a 412): the
 * studio's values everywhere except the keys the member was changing, which keep the member's
 * text. The next save then carries only those keys, against the new version.
 */
export function rebaseForm(form: ImagegenDraft, pending: readonly string[], fresh: StudioDraft): ImagegenDraft {
  const out = formFromStudio(fresh) as unknown as Record<keyof ImagegenDraft, unknown>;
  const mine = form as unknown as Record<keyof ImagegenDraft, unknown>;
  for (const f of Object.keys(FORM_TO_STUDIO) as (keyof ImagegenDraft)[]) {
    if (pending.includes(FORM_TO_STUDIO[f])) out[f] = mine[f];
  }
  return out as unknown as ImagegenDraft;
}

/**
 * The form after the studio moved underneath it: every key where the form already says what
 * the studio says keeps the form's own text (so "7." being typed is not rewritten to "7"), and
 * every other key takes the studio's.
 */
export function mergeForm(form: ImagegenDraft, studio: StudioDraft): ImagegenDraft {
  const mine = studioFromForm(form);
  const theirs = formFromStudio(studio);
  const differ = new Set(changedKeys(mine, { ...studio, suggest_model: undefined }));
  if (!differ.size) return form;
  const out = { ...form } as Record<keyof ImagegenDraft, unknown>;
  for (const f of Object.keys(FORM_TO_STUDIO) as (keyof ImagegenDraft)[]) {
    if (differ.has(FORM_TO_STUDIO[f])) out[f] = theirs[f];
  }
  return out as unknown as ImagegenDraft;
}

/** The studio keys a set of form fields lives under — what a member's touch clears. */
export const studioKeysOf = (fields: (keyof ImagegenDraft)[]): StudioKey[] => [
  ...new Set(fields.map((f) => FORM_TO_STUDIO[f])),
];

export function toggleLock(locks: string[] | undefined, key: StudioKey): string[] {
  const cur = locks || [];
  return cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key].sort();
}

/** An edit-log change's field as a studio key: `params.cfg` counts as `params`. */
export const changeKey = (field: string): string => field.split(".")[0];

/**
 * The keys the agent moved since `afterSeq`: what the pane outlines until the member touches
 * them (decision 6). Read off the edit log rather than a diff, so a change the MEMBER made in
 * another pane is not painted as the agent's.
 */
export function agentTouched(entries: DraftLogEntry[] | undefined, afterSeq: number): StudioKey[] {
  const out = new Set<string>();
  for (const e of entries || []) {
    if (e.seq <= afterSeq || e.kind !== "edit" || e.author !== "agent") continue;
    for (const c of e.changes || []) out.add(changeKey(c.field));
  }
  return [...out].sort() as StudioKey[];
}

const lastSeq = (entries: DraftLogEntry[] | undefined): number =>
  (entries || []).reduce((m, e) => (e.seq > m ? e.seq : m), 0);

export { lastSeq };

/** One press as the edit log tells it (decision 9). */
export interface StudioVersion {
  version: string;
  seq: number;
  at: string;
  mode?: string;
  author?: string;
  /** ok / failed / recovered / lost from the first press_result; `pending` when there is none. */
  state: "ok" | "failed" | "recovered" | "lost" | "pending";
  jobs?: string[];
  group?: string;
  error?: string;
}

/**
 * The versions, newest first. The FIRST press_result per version wins and later ones are
 * ignored (a retry and the start-up backfill can both write one).
 */
export function versionsOf(entries: DraftLogEntry[] | undefined): StudioVersion[] {
  const presses = new Map<string, StudioVersion>();
  const sorted = [...(entries || [])].sort((a, b) => a.seq - b.seq);
  for (const e of sorted) {
    if (e.kind === "press" && e.version && !presses.has(e.version)) {
      presses.set(e.version, { version: e.version, seq: e.seq, at: e.at, mode: e.mode, author: e.author, state: "pending" });
    }
  }
  const settled = new Set<string>();
  for (const e of sorted) {
    if (e.kind !== "press_result" || !e.version || settled.has(e.version)) continue;
    settled.add(e.version);
    const v = presses.get(e.version);
    if (!v) continue;
    const st = e.state;
    v.state = st === "failed" || st === "recovered" || st === "lost" ? st : e.error ? "failed" : "ok";
    v.jobs = e.jobs;
    v.group = e.group;
    v.error = e.error;
  }
  return [...presses.values()].sort((a, b) => b.seq - a.seq);
}

/** The press line a picture's version points at — what "back to this picture's settings"
 *  rewinds to. Null when the page loaded so far does not reach it. */
export function pressSeqOf(entries: DraftLogEntry[] | undefined, version: string | undefined): number | null {
  if (!version) return null;
  const e = (entries || []).find((x) => x.kind === "press" && x.version === version);
  return e ? e.seq : null;
}

/** An entry "back to this point" can restore: it carries the whole draft. */
export const rewindable = (e: DraftLogEntry): boolean => !!e.draft && (e.kind === "edit" || e.kind === "rewind" || e.kind === "press");

const short = (v: unknown, max = 24): string => {
  if (v === undefined || v === null || v === "") return "∅";
  const s = typeof v === "string" ? v : JSON.stringify(v);
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
};

/**
 * One change as a card line: `cfg 7→5`. `params` is opened up one level, because "params
 * {…}→{…}" says nothing and the sampler fields are the ones an agent moves most.
 */
export function describeChange(c: DraftChange): string[] {
  const key = c.field;
  if (key === "params" && (typeof c.before === "object" || typeof c.after === "object")) {
    const b = (c.before || {}) as Record<string, unknown>;
    const a = (c.after || {}) as Record<string, unknown>;
    const keys = [...new Set([...Object.keys(b), ...Object.keys(a)])].filter((k) => !same(b[k], a[k])).sort();
    return keys.map((k) => `${k} ${short(b[k])}→${short(a[k])}`);
  }
  const name = key.startsWith("params.") ? key.slice(7) : key;
  return [`${name} ${short(c.before)}→${short(c.after)}`];
}

// --- the signal line (decision 5) --------------------------------------------------------

/** What happened since the agent last heard from this pane. */
export interface SignalSince {
  /** The newest edit-log seq the pane knows. */
  seq: number;
  /** The member changed the draft (an edit authored "human"). */
  draftChanged: boolean;
  /** New pictures from presses since then. */
  newResults: number;
  /** The newest rewind's target, if the member rewound. */
  rewindTo?: number;
}

/**
 * Folds the edit log after `afterSeq` into the signal's facts. `newResults` counts versions
 * whose first press_result landed after `afterSeq`; the agent's own trials count too, since the
 * agent does not see the picture until it asks.
 */
export function foldSince(entries: DraftLogEntry[] | undefined, afterSeq: number): SignalSince {
  let draftChanged = false;
  let newResults = 0;
  let rewindTo: number | undefined;
  const seen = new Set<string>();
  const sorted = [...(entries || [])].sort((a, b) => a.seq - b.seq);
  for (const e of sorted) {
    if (e.kind === "press_result" && e.version) {
      if (seen.has(e.version)) continue;
      seen.add(e.version);
      if (e.seq > afterSeq && !e.error && e.state !== "failed" && e.state !== "lost") newResults += (e.jobs?.length || 1);
      continue;
    }
    if (e.seq <= afterSeq) continue;
    if (e.kind === "edit" && e.author === "human") draftChanged = true;
    if (e.kind === "rewind") rewindTo = e.rewind_to ?? rewindTo;
  }
  return { seq: lastSeq(entries), draftChanged, newResults, rewindTo };
}

/** The words the signal carries, in the member's language (the agent reads either). */
export interface SignalWords {
  draftChanged: string;
  /** Holds `{n}`. */
  newResults: string;
  /** Holds `{n}`. */
  rewind: string;
}

/**
 * The one line appended to a composer send: `[studio v12 · draft changed · 2 new results →
 * get_image_studio]`. It always opens with STUDIO_SIGNAL_PREFIX and closes with
 * STUDIO_SIGNAL_SUFFIX — the transcript's stripper matches both ends, and never a "<", which
 * isNoise would read as a system line.
 */
export function studioSignal(s: SignalSince, w: SignalWords): string {
  const parts = [`v${s.seq}`];
  if (s.draftChanged) parts.push(w.draftChanged);
  if (s.newResults > 0) parts.push(w.newResults.replace("{n}", String(s.newResults)));
  if (s.rewindTo != null) parts.push(w.rewind.replace("{n}", String(s.rewindTo)));
  return `${STUDIO_SIGNAL_PREFIX}${parts.join(" · ")} ${STUDIO_SIGNAL_SUFFIX}`;
}


// --- attaching an agent (decision 8) -------------------------------------------------------

export type StudioDriver = "managed" | "tui";

/** Why a kind is offered but not selectable. */
export type KindBlock = "af_shared" | "af_unreachable" | "af_session_name";

export interface KindChoice {
  kind: SessionKind;
  blocked?: KindBlock;
}

/**
 * The kinds "attach an agent" offers for one execution method. The two lists come from
 * different places on purpose: Managed is the kinds with a managed driver; Terminal is the
 * launchable kinds that can run in a pane (`terminalDriver !== false` — claude and agy leave
 * the field out, so a truthiness test would drop them), without shell.
 *
 * Blocked, with the reason shown: opencode Managed (one af child serves several sessions of a
 * directory, so another session could write this draft), muse (it scrubs the MCP child's
 * environment and the af server answers 401), and copilot / cursor / kiro Managed until their
 * af child is told AF_SESSION_NAME.
 */
export function studioKindChoices(driver: StudioDriver, launchable?: readonly string[]): KindChoice[] {
  const allowed = launchable ? new Set(launchable) : null;
  const out: KindChoice[] = [];
  for (const k of repoLaunchKinds) {
    if (k === "shell") continue;
    if (allowed && !allowed.has(k)) continue;
    const a = AGENTS[k];
    if (driver === "managed" && !a.managedDriver) continue;
    if (driver === "tui" && a.terminalDriver === false) continue;
    let blocked: KindBlock | undefined;
    if (k === "muse") blocked = "af_unreachable";
    else if (driver === "managed" && k === "opencode") blocked = "af_shared";
    else if (driver === "managed" && (k === "copilot" || k === "cursor" || k === "kiro")) blocked = "af_session_name";
    out.push(blocked ? { kind: k, blocked } : { kind: k });
  }
  return out;
}

/**
 * Whether worktree may be turned OFF: only where AF_SESSION_NAME reaches the af child on every
 * path, so the session is never guessed from the working directory — every Terminal kind, and
 * lcpp. codex Managed is not one: a resume after the Agent's daemon was replaced falls back to
 * the guess.
 */
export const worktreeOptional = (kind: string, driver: StudioDriver): boolean => driver === "tui" || kind === "lcpp";

/** The history items the studio's pictures were made from, keyed by path. */
export const historyByPath = (items: HistoryItem[]): Map<string, HistoryItem> => new Map(items.map((i) => [i.path, i]));

// --- the set_image_draft card (decision 9) ------------------------------------------------

/**
 * Whether a transcript tool name is af's set_image_draft. A client namespaces af's tools behind
 * a server name that changes every boot (`mcp__af_1a2b3c4d__set_image_draft` in claude,
 * `af_1a2b3c4d_set_image_draft` in opencode), and kiro keeps the bare name — the same shapes
 * mcpreg.IsAFToolName accepts on the Agent.
 */
export const isStudioDraftTool = (tool: string | undefined): boolean =>
  !!tool && /^(?:(?:mcp__)?(?:af_[0-9a-f]{8}|af)[-_.]+)?set_image_draft$/.test(tool.trim());

/** Where one set_image_draft call sits in the transcript. */
export interface DraftCall {
  /** When the assistant turn holding the call started (the transcript's `ts`). */
  turnTs?: string;
  /** When it ended, from agents that record one (opencode, copilot). */
  turnEndTs?: string;
  /** Which set_image_draft call of that turn this is, from 0. */
  nth: number;
  /** The tool's result, from agents whose transcript carries one (codex, opencode). */
  output?: string;
}

const ms = (iso: string | undefined): number => (iso ? Date.parse(iso) : NaN);
const DRAFT_CALL_SLACK_MS = 10_000;

/**
 * The edit-log entry one set_image_draft call wrote, or null when it cannot be told.
 *
 * The tool's own result names it exactly: set_image_draft answers with the studio after the
 * write, whose newest edit by this session is the one. Where the transcript does not carry the
 * result (claude), the call is matched on time and order instead: the nth agent edit by this
 * session at or after the turn's start (and before its end, when the turn has one). A call whose
 * every field was dropped writes no entry, which can shift the later calls of the same turn onto
 * the next one — the one imprecision of the time match, and the reason an exact result wins.
 */
export function draftCallEntry(entries: DraftLogEntry[] | undefined, session: string, call: DraftCall): DraftLogEntry | null {
  const mine = [...(entries || [])]
    .filter((e) => e.kind === "edit" && e.author === "agent" && (!session || e.session === session))
    .sort((a, b) => a.seq - b.seq);
  if (call.output) {
    try {
      const out = JSON.parse(call.output) as { studio?: { recent_log?: DraftLogEntry[] } };
      const log = (out.studio?.recent_log || []).filter((e) => e.kind === "edit" && e.author === "agent" && (!session || e.session === session));
      const last = log.reduce<DraftLogEntry | null>((m, e) => (!m || e.seq > m.seq ? e : m), null);
      if (last) return mine.find((e) => e.seq === last.seq) || last;
    } catch {
      /* an error result is prose: fall back to the time match */
    }
  }
  const from = ms(call.turnTs);
  if (Number.isNaN(from)) return null;
  // The end is the turn's last transcript row, which can be the tool call itself: the edit it
  // wrote lands a moment after. The slack covers that and is far shorter than a reply and a
  // new prompt, which is what separates this turn's edits from the next one's.
  const to = ms(call.turnEndTs) + DRAFT_CALL_SLACK_MS;
  const inTurn = mine.filter((e) => {
    const at = ms(e.at);
    return at >= from && (Number.isNaN(to) || at <= to);
  });
  return inTurn[call.nth] || null;
}
