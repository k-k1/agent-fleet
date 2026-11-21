// The memory pane's I/O adapter for useFileEditor (ADR 0141 decision 4, the `{load, save}` step).
//
// The hook edits one string and compares revisions as `sha256:` hashes of it; a memory is three
// fields and a store counter. This file is the mapping between the two, and it is the only place
// that knows both:
//  - the buffer is one JSON line {description, type} followed by the body, so the conflict panel's
//    mine/remote diff reads as the body it is about;
//  - the counter the store hands out is kept by the hash of the text it was read as, and a save
//    sends the counter that belongs to the base the hook names;
//  - a `memory_conflict` answer is the hook's `revision_conflict`, which starts its conflict flow.
import { api, apiJSON, errDetail } from "../../core/api/client.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useMemoryStore } from "./store.ts";
import type { EditableFile, PutFileResult } from "../editor/api.ts";
import { revisionOf } from "../editor/buffer.ts";
import type { FileEditorIO } from "../editor/useFileEditor.ts";
import type { MemoryBody, SecretFinding } from "../settings/memory/memoryTypes.ts";

export interface EntryFields {
  /** Carried only by a new memory's buffer, which has no name to key it by until one is typed. */
  name?: string;
  description: string;
  type: string;
  body: string;
}

export function encodeEntry(f: EntryFields): string {
  const head = f.name === undefined ? { description: f.description, type: f.type } : { name: f.name, description: f.description, type: f.type };
  return JSON.stringify(head) + "\n" + f.body;
}

/** The inverse of encodeEntry. Text that is not its output is taken as a bare body, so a buffer the
 *  hook produced by any other route still shows up rather than throwing in render. */
export function decodeEntry(text: string): EntryFields {
  const cut = text.indexOf("\n");
  if (cut >= 0) {
    try {
      const head = JSON.parse(text.slice(0, cut)) as { name?: unknown; description?: unknown; type?: unknown };
      if (typeof head.description === "string" && typeof head.type === "string") {
        const body = text.slice(cut + 1);
        return typeof head.name === "string" ? { name: head.name, description: head.description, type: head.type, body } : { description: head.description, type: head.type, body };
      }
    } catch {
      // fall through to the bare body
    }
  }
  return { description: "", type: "feedback", body: text };
}

/** The hook's identity for the buffer of a new memory: one per pane, since no name exists to share
 *  it by. The tenant is part of it, as for an existing memory. */
export const newEntryKey = (tenant: string, c: { scope: string; project: string }, paneId: string) => `${tenant}|new:${c.scope}:${c.project}/${paneId}`;
/** The key of an existing memory; the tenant is part of it: a user-scope memory of one tenant has
 *  the same name as another's, and a buffer carried across a switch would be saved into the wrong
 *  workspace. */
export const entryKey = (tenant: string, c: { scope: string; project: string; name: string }) => `${tenant}|${c.scope}:${c.project}/${c.name}`;

/** The file a new memory's buffer rests on: empty, at a revision no save is ever based on. */
export function blankNewFile(key: string): EditableFile {
  const content = encodeEntry({ name: "", description: "", type: "feedback", body: "" });
  return { path: key, content, size: content.length, binary: false, truncated: false, editable: true, editabilityReason: null, revision: revisionOf(content) };
}

export interface EntryTarget {
  /** The hook's identity for the memory (entryKey, or newEntryKey for a memory without a name). */
  key: string;
  scope: string;
  project: string;
  name: string;
}

/** What a save the store refused leaves for the pane to show in place. */
export interface EntryRefusal {
  code: string;
  text: string;
  findings: SecretFinding[];
  /** The buffer text the save was for: the reason holds only while the buffer still reads so. */
  content: string;
}

interface Known {
  revision: number;
  kinds: string[];
  /** The type as the store holds it: unset ("") reads as feedback in the buffer but is not the same type. */
  type: string;
}

// Counters by memory and content hash, outside React for the same reason the buffer is outside it:
// a dirty buffer's guard saves after its pane has unmounted. Bounded per memory.
const known = new Map<string, Map<string, Known>>();
const KEEP = 16;
// Set when a save lost to a conflict (or met an unknown base): the next read that *starts after*
// it is the remote the member is about to adopt. The value is a ticket, so a read already in
// flight when the flag was set (an edit-start follow) cannot consume it by finishing first.
const adoptNext = new Map<string, number>();
let adoptTicket = 0;
const raiseAdopt = (key: string) => void adoptNext.set(key, ++adoptTicket);
// The text last sent for a memory, as the Agent stores it. The Agent trims the description, type
// and body it is given, so what a read answers after a save differs from the buffer by trailing
// whitespace alone. Kept by memory, outside the adapter, like the counters.
const sent = new Map<string, { raw: string; stored: string; base: string; revision: number; kinds: string[] }>();
// The refusal of the last save of a memory, for a pane that was not mounted when it came: the
// adapter outlives the view that asked for the save.
const refusals = new Map<string, EntryRefusal>();
// The new memory a save was sent for, until its outcome is known: a recovery read has to look for
// that name, not for the blank the buffer began on.
const sentNew = new Map<string, { name: string; raw: string; stored: string }>();
const KEEP_SENT = 16;

function remember(key: string, sha: string, entry: Known, overwrite: boolean) {
  let byHash = known.get(key);
  if (!byHash) known.set(key, (byHash = new Map()));
  // A re-read of text the buffer already rests on must not move its base: another writer may have
  // changed only the kinds, and the counter the edit started from is what the store has to check.
  if (!overwrite && byHash.has(sha)) return;
  byHash.delete(sha);
  byHash.set(sha, entry);
  while (byHash.size > KEEP) byHash.delete(byHash.keys().next().value as string);
}

// The pane that was in edit mode, and on which memory: a tabbed cell renders only its selected
// view, so the view that comes back reads this instead of falling to the read view. Per pane and
// key, so a retarget does not carry edit mode to another memory. Shared and observable: a save that
// finishes after the view was rebuilt must end edit mode in the view that is on screen.
const editingPanes = new Map<string, string>();
const editingListeners = new Set<() => void>();
export const isEditingPane = (paneId: string, key: string) => editingPanes.get(paneId) === key;
export function setEditingPane(paneId: string, key: string, on: boolean) {
  if (on) editingPanes.set(paneId, key);
  else if (editingPanes.get(paneId) === key) editingPanes.delete(paneId);
  else return;
  for (const fn of [...editingListeners]) fn();
}
/** Keeps only the panes `keep` accepts (still open, still on that memory). */
export function pruneEditingPanes(keep: (paneId: string, key: string) => boolean) {
  let changed = false;
  for (const [paneId, key] of [...editingPanes]) if (!keep(paneId, key)) changed = editingPanes.delete(paneId) || changed;
  if (changed) for (const fn of [...editingListeners]) fn();
}
export function subscribeEditing(fn: () => void): () => void {
  editingListeners.add(fn);
  return () => void editingListeners.delete(fn);
}

/** Test hook. */
export function resetMemoryRevisions() {
  known.clear();
  adoptNext.clear();
  editingPanes.clear();
  editingListeners.clear();
  sent.clear();
  sentNew.clear();
  refusals.clear();
  refusalListeners.clear();
}

const refusalListeners = new Map<string, Set<(r: EntryRefusal | null) => void>>();
function setRefusal(key: string, r: EntryRefusal | null) {
  if (r) refusals.set(key, r);
  else refusals.delete(key);
  for (const fn of refusalListeners.get(key) ?? []) fn(r);
}

/** The refusal the last save of this memory ended in, until the next save starts or it is cleared. */
export const refusalOf = (key: string): EntryRefusal | null => refusals.get(key) ?? null;
/** Called with every refusal (or its clearing) of this memory's saves, whichever view asked for the
 *  save: a view that was unmounted and mounted again while the save was in flight is told too. */
export function subscribeRefusal(key: string, fn: (r: EntryRefusal | null) => void): () => void {
  let set = refusalListeners.get(key);
  if (!set) refusalListeners.set(key, (set = new Set()));
  set.add(fn);
  return () => void set.delete(fn);
}
/** Drop the stored reason without telling the views: the text it was about changed. */
export const clearRefusal = (key: string) => void refusals.delete(key);
/** Forget what a finished or abandoned edit left: its refusal, and the text an unknown outcome
 *  would have to be matched against. */
export function clearPending(key: string) {
  // A conflict resolution that was abandoned must not make the next unrelated read adopt.
  adoptNext.delete(key);
  refusals.delete(key);
  sent.delete(key);
  sentNew.delete(key);
}

// Go's strings.TrimSpace, which the Agent trims with: JS trim() differs at U+0085 (Go trims it, JS
// does not) and U+FEFF (JS trims it, Go does not).
const GO_SPACE = "[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]+";
const goSpaceEdges = new RegExp(`^${GO_SPACE}|${GO_SPACE}$`, "g");
const goTrim = (v: string) => v.replace(goSpaceEdges, "");

/** `text` as the Agent stores it (it trims every field it is given). */
export function storedForm(text: string): string {
  const f = decodeEntry(text);
  return encodeEntry({ description: goTrim(f.description), type: goTrim(f.type), body: goTrim(f.body) });
}

export function entryFile(key: string, body: MemoryBody, overwrite = false): EditableFile {
  const content = encodeEntry({ description: body.description, type: body.type || "feedback", body: body.body ?? "" });
  const revision = revisionOf(content);
  remember(key, revision, { revision: body.revision, kinds: body.kinds ?? [], type: body.type ?? "" }, overwrite);
  return { path: key, content, size: content.length, binary: false, truncated: false, editable: true, editabilityReason: null, revision };
}

// The Agent drops duplicate kinds on a save, so a hand-written memory read with one still matches.
const uniq = (a: string[]) => [...new Set(a)];
// ...and trims each one, as it trims every field it is given.
const sameKinds = (a: string[], b: string[]) => {
  const x = uniq(a.map(goTrim));
  const y = uniq(b.map(goTrim));
  return x.length === y.length && x.every((k, i) => k === y[i]);
};
// Exactly as the store holds them: unlike a save's normalising comparison, whitespace inside a kind is
// part of what a delivery matches on.
const sameKindsExact = (a: string[], b: string[]) => a.length === b.length && a.every((k, i) => k === b[i]);
const validRevision = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;

// The Agent omits empty fields on the wire (omitempty), so a memory with no type or body is valid.
const optionalString = (v: unknown) => v === undefined || typeof v === "string";
export function isMemoryBody(res: unknown, scope: string, name: string): res is MemoryBody {
  const b = res as Partial<MemoryBody> | null;
  return !!b && b.name === name && b.scope === scope && optionalString(b.body) && typeof b.description === "string" && optionalString(b.type) && (b.kinds === undefined || (Array.isArray(b.kinds) && b.kinds.every((k) => typeof k === "string"))) && validRevision(b.revision);
}

// A request that never answers would hold the pane in `saving` (or its recovery read in flight)
// for good. Aborted, a save throws and reads as an unknown outcome, which the recovery read then
// settles; an aborted read is an unavailable remote the member can retry.
export const READ_TIMEOUT_MS = 20_000;
export const SAVE_TIMEOUT_MS = 30_000;
async function timed(ms: number, send: (signal: AbortSignal) => Promise<any>): Promise<any> { // eslint-disable-line @typescript-eslint/no-explicit-any
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), ms);
  try {
    return await send(ctl.signal);
  } finally {
    clearTimeout(timer);
  }
}

const projectQuery = (project: string) => (project ? "?project=" + encodeURIComponent(project) : "");

/** `onSaved` runs once a new memory is stored, with its name: the adapter outlives the view, and a
 *  guard's save of an unmounted pane must still land the pane on the memory it became. */
export function memoryEntryIO(target: EntryTarget, onRefused: (r: EntryRefusal | null) => void, onSaved?: (name: string) => void): FileEditorIO {
  const { key, scope, project, name } = target;
  const isNew = name === "";
  // The requests carry the tenant current at send time; a buffer kept from another tenant must not
  // be read or written under it.
  const tenant = useTenantStore.getState().tenant;
  const sameTenant = () => useTenantStore.getState().tenant === tenant;
  // After a save lost to a conflict, the next read is the remote the member is about to adopt: it
  // replaces the counter and kinds of text it shares with the base. Any other read must not. Kept
  // per memory outside the adapter, which a tab switch rebuilds.
  return {
    // The member may drop a buffer whose outcome or remote cannot be read (a secret-scan refusal, a
    // 404 on the recovery read): a resave is checked against the counter, so a save that had landed
    // makes it a 409, not an overwrite.
    discardUnconfirmed: true,
    async load() {
      if (!sameTenant()) throw new Error("tenant_changed");
      // Nothing is stored under a new memory's key: what a recovery read finds is the blank it began on.
      if (isNew) {
        const last = sentNew.get(key);
        if (!last) return blankNewFile(key);
        // Stored by another, or not at all, is a definite "not ours": the blank the buffer began on.
        // A read that fails confirms nothing and stays unavailable; the buffer can still be dropped
        // (discardUnconfirmed), since a resave of it at revision 0 is refused if the save had landed.
        const q = `scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(last.name)}`;
        const res = await timed(READ_TIMEOUT_MS, (signal) => api(`api/agents/memory/entries/member-read?${q}`, { signal }));
        if (res?.error?.code === "memory_not_found" || res?.error?.status === 404) return blankNewFile(key);
        if (res?.error) throw new Error(res.error.code || "load_failed");
        if (!isMemoryBody(res, scope, last.name)) throw new Error("load_failed");
        const b = res as MemoryBody;
        const found = encodeEntry({ description: b.description, type: b.type || "feedback", body: b.body ?? "" });
        // Revision 1 with no kinds is what a first save of this text leaves; anything else is a
        // memory that was there before, whatever its text.
        if (found !== last.stored || b.revision !== 1 || (b.kinds ?? []).length > 0) return blankNewFile(key);
        // Stored as sent: the buffer's text is reported as the remote, which the panel offers to accept
        // (the view lands the pane when it is).
        return { ...blankNewFile(key), content: last.raw, size: last.raw.length, revision: revisionOf(last.raw) };
      }
      // The ticket this read began under: only the flag raised before the read started, and still
      // the current one when it answers, lets it replace a counter.
      const adopting = adoptNext.get(key);
      // The save attempt this read began under: a read that began before the save started must not
      // be matched against it (it describes the memory as of before the save).
      const attemptAtStart = sent.get(key);
      const q = `scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(name)}`;
      const res = await timed(READ_TIMEOUT_MS, (signal) => api(`api/agents/memory/entries/member-read?${q}`, { signal }));
      if (res?.error) throw new Error(res.error.code || "load_failed");
      // An empty or malformed answer is not a memory: never a candidate for conflict resolution.
      if (!isMemoryBody(res, scope, name)) throw new Error("load_failed");
      // Not overwriting: a GET the buffer does not adopt (typing won the race) must not move the
      // counter the edit started from.
      const adoptNow = adopting !== undefined && adoptNext.get(key) === adopting;
      let file = entryFile(key, res as MemoryBody, adoptNow);
      // Used up by the first read that adopts; a read answering later with the same ticket finds it gone.
      if (adoptNow) adoptNext.delete(key);
      // The text is one the buffer already rests on, read again at a later counter with the same
      // kinds (another writer reverted to it): nothing the member sees differs, so the counter
      // moves forward and a save from this base is not refused. Different kinds keep the old
      // counter, so the save ends in a conflict instead of carrying kinds the member never saw.
      const prior = known.get(key)?.get(file.revision);
      const later = res as MemoryBody;
      if (!adoptNow && prior && later.revision > prior.revision && sameKindsExact(later.kinds ?? [], prior.kinds) && (later.type ?? "") === prior.type) {
        remember(key, file.revision, { revision: later.revision, kinds: later.kinds ?? [], type: later.type ?? "" }, true);
      }
      // After a save whose outcome is unknown, the memory as the Agent stores the text sent is that
      // text: reporting its trimmed form would read the committed save as a third version. The
      // base for the next save is this very read, whatever counter an older read of the text had.
      // Only the recovery read of this attempt may settle it: an older read answering late would
      // move the recorded counter back.
      const last = attemptAtStart && sent.get(key) === attemptAtStart ? attemptAtStart : undefined;
      const answered = res as MemoryBody;
      // The one counter the sent save would have produced, with the kinds it sent: a read at the
      // old counter is the save not having landed, and anything else is another writer's.
      if (last && last.raw !== file.content && last.stored === file.content && answered.revision === last.revision + 1 && sameKinds(answered.kinds ?? [], last.kinds)) {
        const revision = revisionOf(last.raw);
        remember(key, revision, { revision: (res as MemoryBody).revision, kinds: (res as MemoryBody).kinds ?? [], type: (res as MemoryBody).type ?? "" }, true);
        file = { ...file, content: last.raw, size: last.raw.length, revision };
      } else if (last && file.revision === last.base && (answered.revision !== last.revision || !sameKinds(answered.kinds ?? [], last.kinds)) && answered.revision > (known.get(key)?.get(file.revision)?.revision ?? 0)) {
        // Another writer stored the text the save started from, at a later counter: the save did not
        // land, but the counter it was sent with is spent. A resave from the original has to carry
        // the live one, or it ends in a 409.
        remember(key, file.revision, { revision: answered.revision, kinds: answered.kinds ?? [], type: answered.type ?? "" }, true);
      }
      return file;
    },
    async save(_path, content, baseRevision): Promise<PutFileResult> {
      sent.delete(key);
      setRefusal(key, null);
      onRefused(null);
      if (!sameTenant()) return { ok: false, status: 400, error: { code: "tenant_changed", message: "the workspace changed since this text was read" } };
      // A new memory is stored at revision 0, which the Agent refuses once the name exists: a retry of
      // a save whose answer was lost cannot write a second copy.
      const base = isNew ? { revision: 0, kinds: [] as string[] } : known.get(key)?.get(baseRevision);
      // A base this pane never read cannot be saved over safely: let the hook re-read.
      if (!base) raiseAdopt(key);
      if (!base) return { ok: false, status: 409, error: { code: "revision_conflict", message: "unknown base" } };
      const fields = decodeEntry(content);
      const saveName = isNew ? (fields.name ?? "").trim() : name;
      if (isNew) {
        sentNew.delete(key);
        // Bounded: a pane that never settles its save leaves its text here.
        while (sentNew.size >= KEEP_SENT) sentNew.delete(sentNew.keys().next().value as string);
      }
      if (isNew) sentNew.set(key, { name: saveName, raw: content, stored: storedForm(encodeEntry({ description: fields.description, type: fields.type, body: fields.body })) });
      if (!isNew) sent.set(key, { raw: content, stored: storedForm(content), base: baseRevision, revision: base.revision, kinds: base.kinds });
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const payload = {
        scope,
        project,
        name: saveName,
        description: fields.description,
        type: fields.type,
        kinds: base.kinds,
        body: fields.body,
        revision: base.revision,
      };
      const res = await timed(SAVE_TIMEOUT_MS, (signal) =>
        apiJSON("api/agents/memory/entries/member-save" + projectQuery(project), "POST", payload, undefined, { signal }),
      );
      if (res?.error) {
        let code: string = res.error.code || "save_failed";
        // The commit may have landed before the snapshot failed: the outcome is unknown.
        if (code === "memory_snapshot_failed" || (res.error.status ?? 0) >= 500) code = "write_state_unknown";
        // Only an unknown outcome keeps the sent text to match a read against.
        if (code !== "write_state_unknown") {
          sent.delete(key);
          sentNew.delete(key);
        }
        // For a new memory the name is taken: there is no remote text to resolve against.
        if (code === "memory_conflict" && !isNew) raiseAdopt(key);
        if (code === "memory_conflict" && !isNew) return { ok: false, status: 409, error: { code: "revision_conflict", message: errDetail(res.error) } };
        const refusal = { code, text: errDetail(res.error), findings: code === "memory_secret_detected" ? ((res.findings ?? []) as SecretFinding[]) : [], content };
        setRefusal(key, refusal);
        onRefused(refusal);
        return { ok: false, status: 422, error: { code, message: errDetail(res.error) } };
      }
      if (isNew) {
        // The pane re-reads the memory it becomes; no counter is kept for this key.
        sentNew.delete(key);
        useMemoryStore.getState().bump();
        onSaved?.(saveName);
        return { ok: true, path: key, size: content.length, revision: revisionOf(content) };
      }
      // A success without the new counter cannot be told from a lost answer.
      if (!validRevision(res?.revision)) throw new Error("save response without revision");
      // What was saved is what the store now holds: the next base is this text at the counter it was given.
      const revision = revisionOf(content);
      sent.delete(key);
      remember(key, revision, { revision: res.revision, kinds: uniq(base.kinds.map(goTrim)), type: goTrim(fields.type) }, true);
      // Every route that saves (the button, a guard, the resolution panel) ends here.
      useMemoryStore.getState().bump();
      return { ok: true, path: key, size: content.length, revision };
    },
  };
}
