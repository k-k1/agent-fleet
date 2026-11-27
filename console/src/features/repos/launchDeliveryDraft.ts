import { create } from "zustand";
import { useTenantStore } from "../../core/store/tenant.ts";
import {
  ATTACH_DRAFT_MAX_AGE_MS, clearAttachDraftSnapshot, hasAttachDraftSnapshot, readAttachDraftSnapshots,
  restoreAttachDraftFile, writeAttachDraftSnapshot,
} from "../../lib/attachDraft.ts";
import type { LaunchFile } from "./launchDelivery.ts";

type TenantState = ReturnType<typeof useTenantStore.getState>;

export function launchDraftOwner({ tenant, whoami }: TenantState) {
  const member = whoami?.email || whoami?.user ||
    (whoami?.auth_mode === "dev" && typeof whoami.resolved_user === "string" ? whoami.resolved_user : "");
  return { prefix: "af.launch-delivery." + JSON.stringify([tenant, member]) + ":", identified: !!member };
}

type DraftText = { session: string; kind: string; repo: string; prompt: string };
type DraftContent = DraftText & { files: LaunchFile[] };
export type HeldLaunchDraft = DraftContent & { key: string; owner: string; persisted: boolean };

export const useHeldLaunchDrafts = create<{
  drafts: Record<string, HeldLaunchDraft>;
  busy: ReadonlySet<string>;
}>(() => ({ drafts: {}, busy: new Set() }));

function putDraft(draft: HeldLaunchDraft) {
  useHeldLaunchDrafts.setState(({ drafts }) => ({ drafts: { ...drafts, [draft.key]: draft } }));
}

function persistDraft(draft: HeldLaunchDraft, filesChanged = true) {
  const { key, session, kind, repo, prompt, files, persisted } = draft;
  return writeAttachDraftSnapshot(
    key, { session, kind, repo, prompt } satisfies DraftText, filesChanged || !persisted ? files : undefined, persisted,
  );
}

export async function holdLaunchDraft(owner: string, content: DraftContent): Promise<boolean> {
  const draft = { ...content, key: owner + content.session, owner, persisted: false };
  const persisted = await persistDraft(draft);
  putDraft({ ...draft, persisted });
  return persisted;
}

export async function editLaunchDraft(key: string, changes: Partial<Pick<DraftContent, "prompt" | "files">>): Promise<boolean> {
  const draft = useHeldLaunchDrafts.getState().drafts[key];
  if (!draft) return false;
  const edited = { ...draft, ...changes };
  putDraft(edited);
  const persisted = await persistDraft(edited, changes.files !== undefined);
  if (persisted && !edited.persisted && useHeldLaunchDrafts.getState().drafts[key] === edited) {
    putDraft({ ...edited, persisted: true });
  }
  return persisted;
}

export async function restoreLaunchDrafts(prefix: string, current: () => boolean): Promise<void> {
  const before = useHeldLaunchDrafts.getState().drafts;
  const stored = await readAttachDraftSnapshots(prefix);
  if (!current()) return;
  pruneSettled(prefix);
  const restored: Record<string, HeldLaunchDraft> = {};
  for (const { key, data, items } of stored) {
    if (before[key] || !isDraftText(data) || key !== prefix + data.session || !Array.isArray(items)) continue;
    if (wasSettled(key)) {
      void clearLaunchDraft(key);
      continue;
    }
    const files = items.flatMap((entry) => {
      const file = restoreAttachDraftFile(entry);
      return file ? [{ file, path: entry.path }] : [];
    });
    if (files.length !== items.length) continue;
    restored[key] = { ...data, files, key, owner: prefix, persisted: true };
  }
  useHeldLaunchDrafts.setState(({ drafts }) => ({ drafts: { ...restored, ...drafts } }));
}

function isDraftText(data: unknown): data is DraftText {
  if (!data || typeof data !== "object") return false;
  const { session, kind, repo, prompt } = data as Partial<DraftText>;
  return typeof session === "string" && !!session && typeof kind === "string" &&
    typeof repo === "string" && typeof prompt === "string";
}

const settledSuffix = ":settled";

function wasSettled(key: string): boolean {
  try {
    return localStorage.getItem(key + settledSuffix) !== null;
  } catch {
    return false;
  }
}

function pruneSettled(owner: string): void {
  try {
    const cutoff = Date.now() - ATTACH_DRAFT_MAX_AGE_MS;
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const key = localStorage.key(i);
      if (key?.startsWith(owner) && key.endsWith(settledSuffix) && Number(localStorage.getItem(key)) < cutoff) {
        localStorage.removeItem(key);
      }
    }
  } catch { /* Storage may be unavailable. */ }
}

export async function clearLaunchDraft(key: string): Promise<void> {
  // Navigation can interrupt the asynchronous delete after the Agent accepted the
  // prompt. Mark it synchronously so the next page never offers that prompt again.
  const marker = key + settledSuffix;
  try { localStorage.setItem(marker, String(Date.now())); } catch { /* Use IndexedDB. */ }
  if (await clearAttachDraftSnapshot(key)) {
    try { localStorage.removeItem(marker); } catch { /* Pruned on restore. */ }
  }
  useHeldLaunchDrafts.setState(({ drafts }) => {
    const next = { ...drafts };
    delete next[key];
    return { drafts: next };
  });
}

// Two Console tabs can restore the same draft. Recheck it while holding the lock
// so a second retry cannot deliver after the first has cleared it.
export async function withLaunchDraft(draft: HeldLaunchDraft, action: () => Promise<void>): Promise<void> {
  const { busy, drafts } = useHeldLaunchDrafts.getState();
  if (busy.has(draft.key) || !drafts[draft.key]) return;
  useHeldLaunchDrafts.setState({ busy: new Set(busy).add(draft.key) });
  const run = async () => {
    if (wasSettled(draft.key) || (draft.persisted && await hasAttachDraftSnapshot(draft.key) === false)) {
      await clearLaunchDraft(draft.key);
      return;
    }
    await action();
  };
  try {
    await (navigator.locks?.request(draft.key, run) ?? run());
  } finally {
    const next = new Set(useHeldLaunchDrafts.getState().busy);
    next.delete(draft.key);
    useHeldLaunchDrafts.setState({ busy: next });
  }
}
