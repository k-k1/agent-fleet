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
}

interface Known {
  revision: number;
  kinds: string[];
}

// Counters by memory and content hash, outside React for the same reason the buffer is outside it:
// a dirty buffer's guard saves after its pane has unmounted. Bounded per memory.
const known = new Map<string, Map<string, Known>>();
const KEEP = 16;

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
}

export function entryFile(key: string, body: MemoryBody, overwrite = false): EditableFile {
  const content = encodeEntry({ description: body.description ?? "", type: body.type || "feedback", body: body.body ?? "" });
  const revision = revisionOf(content);
  remember(key, revision, { revision: body.revision, kinds: body.kinds ?? [] }, overwrite);
  return { path: key, content, size: content.length, binary: false, truncated: false, editable: true, editabilityReason: null, revision };
}

const validRevision = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;

// The Agent omits empty fields on the wire (omitempty), so a memory with no type or body is valid.
const optionalString = (v: unknown) => v === undefined || typeof v === "string";
export function isMemoryBody(res: unknown, scope: string, name: string): res is MemoryBody {
  const b = res as Partial<MemoryBody> | null;
  return !!b && b.name === name && b.scope === scope && optionalString(b.body) && optionalString(b.description) && optionalString(b.type) && validRevision(b.revision);
}

const projectQuery = (project: string) => (project ? "?project=" + encodeURIComponent(project) : "");

export function memoryEntryIO(target: EntryTarget, onRefused: (r: EntryRefusal | null) => void): FileEditorIO {
  const { key, scope, project, name } = target;
  // The requests carry the tenant current at send time; a buffer kept from another tenant must not
  // be read or written under it.
  const tenant = useTenantStore.getState().tenant;
  const sameTenant = () => useTenantStore.getState().tenant === tenant;
  // After a save lost to a conflict, the next read is the remote the member is about to adopt: it
  // replaces the counter and kinds of text it shares with the base. Any other read must not.
  let adoptNextRead = false;
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
      const file = entryFile(key, res as MemoryBody, adoptNextRead);
      adoptNextRead = false;
      return file;
    },
    async save(_path, content, baseRevision): Promise<PutFileResult> {
      onRefused(null);
      if (!sameTenant()) return { ok: false, status: 400, error: { code: "tenant_changed", message: "the workspace changed since this text was read" } };
      const base = known.get(key)?.get(baseRevision);
      // A base this pane never read cannot be saved over safely: let the hook re-read.
      if (!base) adoptNextRead = true;
      if (!base) return { ok: false, status: 409, error: { code: "revision_conflict", message: "unknown base" } };
      const fields = decodeEntry(content);
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
        if (code === "memory_conflict") adoptNextRead = true;
        if (code === "memory_conflict") return { ok: false, status: 409, error: { code: "revision_conflict", message: errDetail(res.error) } };
        onRefused({ code, text: errDetail(res.error), findings: code === "memory_secret_detected" ? (res.findings ?? []) : [] });
        return { ok: false, status: 422, error: { code, message: errDetail(res.error) } };
      }
      // A success without the new counter cannot be told from a lost answer.
      if (!validRevision(res?.revision)) throw new Error("save response without revision");
      // What was saved is what the store now holds: the next base is this text at the counter it was given.
      const revision = revisionOf(content);
      remember(key, revision, { revision: res.revision, kinds: base.kinds }, true);
      // Every route that saves (the button, a guard, the resolution panel) ends here.
      useMemoryStore.getState().bump();
      return { ok: true, path: key, size: content.length, revision };
    },
  };
}
