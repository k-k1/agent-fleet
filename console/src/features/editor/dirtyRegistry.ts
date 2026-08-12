import type { Layout } from "../../layout/types.ts";
import { allViews } from "../../layout/ops.ts";

export interface DirtyEditorEntry {
  paneId: string;
  label: string;
  isDirty(): boolean;
  save(): Promise<boolean>;
  discard(signal?: AbortSignal): boolean | void | Promise<boolean | void>;
}

export type DirtyGuardReason =
  | "layout"
  | "history"
  | "tenant"
  | "popout"
  | "reload"
  | "logout"
  | "version_update"
  | "workspace_lifecycle";

export interface DirtyGuardRequest {
  id: number;
  reason: DirtyGuardReason;
  entries: DirtyEditorEntry[];
  /** Fires when the request is cancelled; a pending discard must stop before cleaning its buffer. */
  signal: AbortSignal;
  resolve(proceed: boolean): void;
}

const entries = new Map<string, DirtyEditorEntry>();
let request: DirtyGuardRequest | null = null;
let sequence = 0;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

export function subscribeDirtyRegistry(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function registerDirtyEditor(entry: DirtyEditorEntry): () => void {
  entries.set(entry.paneId, entry);
  emit();
  return () => {
    if (entries.get(entry.paneId) === entry) {
      entries.delete(entry.paneId);
      emit();
    }
  };
}

export function notifyDirtyEditorChanged(): void {
  emit();
}

export function currentDirtyGuardRequest(): DirtyGuardRequest | null {
  return request;
}

export function hasDirtyEditors(): boolean {
  for (const entry of entries.values()) if (entry.isDirty()) return true;
  return false;
}

export function dirtyPaneIds(): string[] {
  return [...entries.values()].filter((entry) => entry.isDirty()).map((entry) => entry.paneId);
}

function dirtyEntries(ids?: Iterable<string>): DirtyEditorEntry[] {
  if (!ids) return [...entries.values()].filter((entry) => entry.isDirty());
  const out: DirtyEditorEntry[] = [];
  for (const id of ids) {
    const entry = entries.get(id);
    if (entry?.isDirty()) out.push(entry);
  }
  return out;
}

/** Prompt once for all affected buffers. Save proceeds only if every buffer becomes clean. */
export function confirmDirtyNavigation(
  reason: DirtyGuardReason,
  paneIds?: Iterable<string>,
): Promise<boolean> {
  const affected = dirtyEntries(paneIds);
  if (affected.length === 0) return Promise.resolve(true);
  if (request) return Promise.resolve(false);
  return new Promise<boolean>((resolve) => {
    const abort = new AbortController();
    let settled = false;
    const next: DirtyGuardRequest = {
      id: ++sequence,
      reason,
      entries: affected,
      signal: abort.signal,
      resolve: (proceed) => {
        // A stalled save/discard can resolve long after this request was
        // cancelled; a second resolve must not clobber a newer request.
        if (settled) return;
        settled = true;
        if (request === next) request = null;
        if (!proceed) abort.abort();
        emit();
        resolve(proceed);
      },
    };
    request = next;
    emit();
  });
}

export async function saveDirtyGuardRequest(id: number): Promise<boolean> {
  const active = request;
  if (!active || active.id !== id) return false;
  for (const entry of active.entries) {
    if (!entry.isDirty()) continue;
    if (!(await entry.save()) || entry.isDirty()) return false;
  }
  if (request !== active) return false;
  active.resolve(true);
  return true;
}

export async function discardDirtyGuardRequest(id: number): Promise<boolean> {
  const active = request;
  if (!active || active.id !== id) return false;
  for (const entry of active.entries) {
    if (!entry.isDirty()) continue;
    let discarded: boolean | void;
    try {
      discarded = await entry.discard(active.signal);
    } catch {
      return false;
    }
    if (request !== active) return false;
    if (discarded === false || entry.isDirty()) return false;
  }
  if (request !== active) return false;
  active.resolve(true);
  return true;
}

export function cancelDirtyGuardRequest(id: number): void {
  const active = request;
  if (active?.id === id) active.resolve(false);
}

function panesById(layout: Layout): Map<string, string> {
  const out = new Map<string, string>();
  for (const pane of allViews(layout)) {
    const key = pane.content.kind === "file" ? `file:${pane.content.filePath}` : pane.content.kind;
    out.set(pane.id, key);
  }
  return out;
}

/** Dirty pane identities destroyed or retargeted by a proposed layout mutation. */
export function dirtyPanesDestroyedByLayout(current: Layout, next: Layout): string[] {
  const before = panesById(current);
  const after = panesById(next);
  return dirtyPaneIds().filter((id) => before.has(id) && before.get(id) !== after.get(id));
}

/** Test/reset hook. */
export function clearDirtyRegistryForTests(): void {
  entries.clear();
  if (request) request.resolve(false);
  request = null;
  emit();
}
