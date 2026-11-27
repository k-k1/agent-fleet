import { create } from "zustand";
import { useTenantStore } from "../../core/store/tenant.ts";
import {
  clearAttachDraft, hasAttachDraftSnapshot, readAttachDraftSnapshots,
  restoreAttachDraftFile, writeAttachDraftSnapshot,
} from "../../lib/attachDraft.ts";
import type { LaunchFile } from "./launchDelivery.ts";

type TenantState = ReturnType<typeof useTenantStore.getState>;

export function launchDraftOwner({ tenant, whoami }: TenantState) {
  const member = whoami?.email || whoami?.user ||
    (whoami?.auth_mode === "dev" && typeof whoami.resolved_user === "string" ? whoami.resolved_user : "");
  return { prefix: "af.launch-delivery." + JSON.stringify([tenant, member]) + ":", identified: !!member };
}

type Owner = ReturnType<typeof launchDraftOwner>;
type DraftText = { session: string; kind: string; repo: string; prompt: string };
type DraftContent = DraftText & { files: LaunchFile[] };
export type HeldLaunchDraft = DraftContent & { key: string; owner: string; identified: boolean; persisted: boolean };

export const useHeldLaunchDrafts = create<{ drafts: Record<string, HeldLaunchDraft> }>(() => ({ drafts: {} }));

function putDraft(draft: HeldLaunchDraft) {
  useHeldLaunchDrafts.setState(({ drafts }) => ({ drafts: { ...drafts, [draft.key]: draft } }));
}

function persistDraft(draft: HeldLaunchDraft) {
  if (!draft.identified) return Promise.resolve(false);
  const { key, session, kind, repo, prompt, files, persisted } = draft;
  return writeAttachDraftSnapshot(key, { session, kind, repo, prompt } satisfies DraftText, files, persisted);
}

export async function holdLaunchDraft(owner: Owner, content: DraftContent): Promise<boolean> {
  const draft = { ...content, key: owner.prefix + content.session, owner: owner.prefix, identified: owner.identified, persisted: false };
  const persisted = await persistDraft(draft);
  putDraft({ ...draft, persisted });
  return persisted;
}

export async function editLaunchDraft(key: string, changes: Partial<Pick<DraftContent, "prompt" | "files">>): Promise<boolean> {
  const draft = useHeldLaunchDrafts.getState().drafts[key];
  if (!draft) return false;
  const edited = { ...draft, ...changes };
  putDraft(edited);
  const persisted = await persistDraft(edited);
  if (persisted && !edited.persisted && useHeldLaunchDrafts.getState().drafts[key] === edited) {
    putDraft({ ...edited, persisted: true });
  }
  return persisted;
}

export async function restoreLaunchDrafts(prefix: string, current: () => boolean): Promise<void> {
  const before = useHeldLaunchDrafts.getState().drafts;
  const stored = await readAttachDraftSnapshots(prefix);
  if (!current()) return;
  const restored: Record<string, HeldLaunchDraft> = {};
  for (const { key, data, items } of stored) {
    if (before[key] || !isDraftText(data) || key !== prefix + data.session || !Array.isArray(items)) continue;
    const files = items.flatMap((entry) => {
      const file = restoreAttachDraftFile(entry);
      return file ? [{ file, path: entry.path }] : [];
    });
    if (files.length !== items.length) continue;
    restored[key] = { ...data, files, key, owner: prefix, identified: true, persisted: true };
  }
  useHeldLaunchDrafts.setState(({ drafts }) => ({ drafts: { ...restored, ...drafts } }));
}

function isDraftText(data: unknown): data is DraftText {
  if (!data || typeof data !== "object") return false;
  const { session, kind, repo, prompt } = data as Partial<DraftText>;
  return typeof session === "string" && !!session && typeof kind === "string" &&
    typeof repo === "string" && typeof prompt === "string";
}

export async function clearLaunchDraft(key: string): Promise<void> {
  await clearAttachDraft(key);
  useHeldLaunchDrafts.setState(({ drafts }) => {
    const next = { ...drafts };
    delete next[key];
    return { drafts: next };
  });
}

// Two Console tabs can restore the same draft. Recheck it while holding the lock
// so a second retry cannot deliver after the first has cleared it.
export function withLaunchDraft(draft: HeldLaunchDraft, action: () => Promise<void>): Promise<void> {
  const run = async () => {
    if (draft.persisted && await hasAttachDraftSnapshot(draft.key) === false) {
      await clearLaunchDraft(draft.key);
      return;
    }
    await action();
  };
  return navigator.locks?.request(draft.key, run) ?? run();
}
