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
  description: string;
  type: string;
  body: string;
}

export function encodeEntry(f: EntryFields): string {
  return JSON.stringify({ description: f.description, type: f.type }) + "\n" + f.body;
}

/** The inverse of encodeEntry. Text that is not its output is taken as a bare body, so a buffer the
 *  hook produced by any other route still shows up rather than throwing in render. */
export function decodeEntry(text: string): EntryFields {
  const cut = text.indexOf("\n");
  if (cut >= 0) {
    try {
      const head = JSON.parse(text.slice(0, cut)) as { description?: unknown; type?: unknown };
      if (typeof head.description === "string" && typeof head.type === "string") {
        return { description: head.description, type: head.type, body: text.slice(cut + 1) };
      }
    } catch {
      // fall through to the bare body
    }
  }
  return { description: "", type: "feedback", body: text };
}

export interface EntryTarget {
  /** The hook's identity for the memory; the tenant is part of it (see draftKey). */
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
}

// Counters by memory and content hash, outside React for the same reason the buffer is outside it:
// a dirty buffer's guard saves after its pane has unmounted. Bounded per memory.
const known = new Map<string, Map<string, Known>>();
const KEEP = 16;
const adoptNext = new Set<string>();
// The text last sent for a memory, as the Agent stores it. The Agent trims the description, type
// and body it is given, so what a read answers after a save differs from the buffer by trailing
// whitespace alone. Kept by memory, outside the adapter, like the counters.
const sent = new Map<string, { raw: string; stored: string; revision: number; kinds: string[] }>();
// The refusal of the last save of a memory, for a pane that was not mounted when it came: the
// adapter outlives the view that asked for the save.
const refusals = new Map<string, EntryRefusal>();

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

/** Test hook. */
export function resetMemoryRevisions() {
  known.clear();
  adoptNext.clear();
  sent.clear();
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
  refusals.delete(key);
  sent.delete(key);
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
  remember(key, revision, { revision: body.revision, kinds: body.kinds ?? [] }, overwrite);
  return { path: key, content, size: content.length, binary: false, truncated: false, editable: true, editabilityReason: null, revision };
}

// The Agent drops duplicate kinds on a save, so a hand-written memory read with one still matches.
const uniq = (a: string[]) => [...new Set(a)];
const sameKinds = (a: string[], b: string[]) => {
  const x = uniq(a);
  const y = uniq(b);
  return x.length === y.length && x.every((k, i) => k === y[i]);
};
const validRevision = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;

// The Agent omits empty fields on the wire (omitempty), so a memory with no type or body is valid.
const optionalString = (v: unknown) => v === undefined || typeof v === "string";
export function isMemoryBody(res: unknown, scope: string, name: string): res is MemoryBody {
  const b = res as Partial<MemoryBody> | null;
  return !!b && b.name === name && b.scope === scope && optionalString(b.body) && typeof b.description === "string" && optionalString(b.type) && (b.kinds === undefined || (Array.isArray(b.kinds) && b.kinds.every((k) => typeof k === "string"))) && validRevision(b.revision);
}

const projectQuery = (project: string) => (project ? "?project=" + encodeURIComponent(project) : "");

export function memoryEntryIO(target: EntryTarget, onRefused: (r: EntryRefusal | null) => void): FileEditorIO {
  const { key, scope, project, name } = target;
  // The requests carry the tenant current at send time; a buffer kept from another tenant must not
  // be read or written under it.
  const tenant = useTenantStore.getState().tenant;
  const sameTenant = () => useTenantStore.getState().tenant === tenant;
  // After a save lost to a conflict, the next read is the remote the member is about to adopt: it
  // replaces the counter and kinds of text it shares with the base. Any other read must not. Kept
  // per memory outside the adapter, which a tab switch rebuilds.
  return {
    async load() {
      if (!sameTenant()) throw new Error("tenant_changed");
      const q = `scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(name)}`;
      const res = await api(`api/agents/memory/entries/member-read?${q}`);
      if (res?.error) throw new Error(res.error.code || "load_failed");
      // An empty or malformed answer is not a memory: never a candidate for conflict resolution.
      if (!isMemoryBody(res, scope, name)) throw new Error("load_failed");
      // Not overwriting: a GET the buffer does not adopt (typing won the race) must not move the
      // counter the edit started from.
      let file = entryFile(key, res as MemoryBody, adoptNext.has(key));
      // Only a read that became a buffer base uses the flag up.
      adoptNext.delete(key);
      // After a save whose outcome is unknown, the memory as the Agent stores the text sent is that
      // text: reporting its trimmed form would read the committed save as a third version. The
      // base for the next save is this very read, whatever counter an older read of the text had.
      const last = sent.get(key);
      const answered = res as MemoryBody;
      // The one counter the sent save would have produced, with the kinds it sent: a read at the
      // old counter is the save not having landed, and anything else is another writer's.
      if (last && last.raw !== file.content && last.stored === file.content && answered.revision === last.revision + 1 && sameKinds(answered.kinds ?? [], last.kinds)) {
        const revision = revisionOf(last.raw);
        remember(key, revision, { revision: (res as MemoryBody).revision, kinds: (res as MemoryBody).kinds ?? [] }, true);
        file = { ...file, content: last.raw, size: last.raw.length, revision };
      }
      return file;
    },
    async save(_path, content, baseRevision): Promise<PutFileResult> {
      sent.delete(key);
      setRefusal(key, null);
      onRefused(null);
      if (!sameTenant()) return { ok: false, status: 400, error: { code: "tenant_changed", message: "the workspace changed since this text was read" } };
      const base = known.get(key)?.get(baseRevision);
      // A base this pane never read cannot be saved over safely: let the hook re-read.
      if (!base) adoptNext.add(key);
      if (!base) return { ok: false, status: 409, error: { code: "revision_conflict", message: "unknown base" } };
      const fields = decodeEntry(content);
      sent.set(key, { raw: content, stored: storedForm(content), revision: base.revision, kinds: base.kinds });
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const res = await apiJSON("api/agents/memory/entries/member-save" + projectQuery(project), "POST", {
        scope,
        project,
        name,
        description: fields.description,
        type: fields.type,
        kinds: base.kinds,
        body: fields.body,
        revision: base.revision,
      });
      if (res?.error) {
        let code: string = res.error.code || "save_failed";
        // The commit may have landed before the snapshot failed: the outcome is unknown.
        if (code === "memory_snapshot_failed" || (res.error.status ?? 0) >= 500) code = "write_state_unknown";
        // Only an unknown outcome keeps the sent text to match a read against.
        if (code !== "write_state_unknown") sent.delete(key);
        if (code === "memory_conflict") adoptNext.add(key);
        if (code === "memory_conflict") return { ok: false, status: 409, error: { code: "revision_conflict", message: errDetail(res.error) } };
        const refusal = { code, text: errDetail(res.error), findings: code === "memory_secret_detected" ? ((res.findings ?? []) as SecretFinding[]) : [], content };
        setRefusal(key, refusal);
        onRefused(refusal);
        return { ok: false, status: 422, error: { code, message: errDetail(res.error) } };
      }
      // A success without the new counter cannot be told from a lost answer.
      if (!validRevision(res?.revision)) throw new Error("save response without revision");
      // What was saved is what the store now holds: the next base is this text at the counter it was given.
      const revision = revisionOf(content);
      sent.delete(key);
      remember(key, revision, { revision: res.revision, kinds: base.kinds }, true);
      // Every route that saves (the button, a guard, the resolution panel) ends here.
      useMemoryStore.getState().bump();
      return { ok: true, path: key, size: content.length, revision };
    },
  };
}
