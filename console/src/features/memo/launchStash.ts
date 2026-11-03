// Moving a Start working draft into the memo queue and back (#3289). The launch dialog has
// one draft slot per repository, so starting a second task there used to mean throwing the
// half-written one away; the memo queue is where it waits instead, synced across devices.
//
// Both directions move BYTES, not references. A stash uploads the staged files into the
// memo-images dir (the dialog's files exist only in this browser until a session is
// minted). A restore fetches them back into File objects, so the dialog stages them exactly
// like a fresh paste: they are uploaded into the new session's own dir at launch, and a
// memo-image GC that runs after the memo is gone cannot break a chip that is already on
// screen.
import { memoCreate, memoDelete, memoImageURL, memoPasteImage } from "./api.ts";
import { raw } from "../../core/api/client.ts";
import { useMemoStore } from "./store.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { Memo, MemoAttachment } from "../../types/memo.ts";
import { memoAttachmentLabel } from "./attachName.ts";
import { clearLaunchPrompt, launchAttachKey, readLaunchDraftMemo, readLaunchPrompt, setLaunchDraftMemo } from "../repos/launchDraft.ts";
import { clearAttachDraft, readAttachDraft, settleAttachDraft } from "../../lib/attachDraft.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";

export type StashResult = { ok: true } | { ok: false; reason: "too_large" | "failed" };

// Memo-image names uploaded by a stash whose memo the queue may not have listed yet. The
// queue's image GC keeps everything it sees referenced; without this, a delete in the queue
// between the upload and the next list refresh would unlink the only copy of these files.
const inFlight = new Set<string>();

/** pendingMemoUploads lists memo-image names a stash has uploaded but not yet seen listed. */
export const pendingMemoUploads = (): string[] => [...inFlight];

/** stashLaunchDraft saves a Start working draft as a memo in the repo's bucket. Nothing is
 *  created unless every file uploaded: a memo missing one of the screenshots it was written
 *  around would be cleared from the dialog as if it were whole. */
export async function stashLaunchDraft(repo: string, prompt: string, files: File[]): Promise<StashResult> {
  const attachments: MemoAttachment[] = [];
  try {
    for (const f of files) {
      const res = await memoPasteImage(f);
      if (res.status !== 201 || !res.path || !res.name) return { ok: false, reason: res.status === 413 ? "too_large" : "failed" };
      inFlight.add(res.name);
      attachments.push({ path: res.path, name: res.name });
    }
    const created = await memoCreate({
      kind: "text",
      repo,
      body: prompt,
      ...(attachments.length ? { attachments } : {}),
    });
    if (!created || (created as { error?: unknown }).error || !created.id) return { ok: false, reason: "failed" };
  } catch {
    return { ok: false, reason: "failed" };
  } finally {
    // Held a while past the create: the queue's GC works from the list it last fetched, and
    // that fetch is only asked for by the bump below. A failed stash's uploads are released
    // the same way and pruned by a later sweep.
    const names = attachments.map((a) => a.name);
    setTimeout(() => names.forEach((n) => inFlight.delete(n)), 60_000);
  }
  useMemoStore.getState().bump();
  return { ok: true };
}

/** stashStoredDraft saves the repo's stored Start working draft (text and files) to a memo
 *  and clears it, so a memo restored into that dialog does not overwrite it. A repo with no
 *  draft is a no-op; a failure leaves the draft untouched. */
export async function stashStoredDraft(repo: string): Promise<StashResult & { saved?: boolean }> {
  const text = readLaunchPrompt(repo).trim();
  const key = launchAttachKey(repo);
  await settleAttachDraft(key);
  // Launch drafts are never uploaded (no session yet), so every record carries its bytes.
  const files = (await readAttachDraft(key)).flatMap((r) => (r.bytes ? [new File([r.bytes], r.name, { type: r.type })] : []));
  if (!text && !files.length) return { ok: true };
  const r = await stashLaunchDraft(repo, text, files);
  if (!r.ok) return r;
  clearLaunchPrompt(repo);
  await clearAttachDraft(key);
  return { ok: true, saved: true };
}

/** fetchMemoAttachments loads stored attachments back as Files, in order. One that cannot be
 *  read (pruned, workspace stopped) is left out; the caller reports how many came back. */
export async function fetchMemoAttachments(atts: MemoAttachment[]): Promise<File[]> {
  const files = await Promise.all(
    atts.map(async (a) => {
      try {
        const r = await raw(memoImageURL(a.name));
        if (!r.ok) return null;
        const b = await r.blob();
        return new File([b], memoAttachmentLabel(a.name), { type: b.type });
      } catch {
        return null;
      }
    }),
  );
  return files.filter((f): f is File => !!f);
}

/** canStartFromMemo: the member's own text memos. A memo taken in from the inbox carries
 *  provenance the send modal stamps on the launch; a file memo is a reference, not a prompt. */
export const canStartFromMemo = (m: Memo): boolean => m.kind === "text" && !m.origin && (!!m.body.trim() || !!m.attachments?.length);

/** prepareMemoRepo readies `repo`'s dialog for restoring memo `memoId`. When the stored
 *  draft already is that memo's (restored before, then cancelled or backed out of — the
 *  dialog marks it once every file is staged, markLaunchDraftMemo), it is the memo's
 *  continuation: `resume` says to open it as it is rather than seed over it. Any other
 *  draft is saved to a memo first. */
export async function prepareMemoRepo(repo: string, memoId: string): Promise<StashResult & { saved?: boolean; resume?: boolean }> {
  if (readLaunchDraftMemo(repo) === memoId) {
    await settleAttachDraft(launchAttachKey(repo)); // the dialog about to open reads it back
    return { ok: true, resume: true };
  }
  return stashStoredDraft(repo);
}

/** markLaunchDraftMemo records that `repo`'s draft now holds memo `memoId` in full. Called by
 *  the dialog only after the memo's files are staged: marked earlier, a dialog closed while
 *  they were loading would resume later without them. */
export const markLaunchDraftMemo = (repo: string, memoId: string): void => setLaunchDraftMemo(repo, memoId);

/** dropSeedContent keeps the memo's identity on the seed but lets the dialog open on the
 *  stored draft (prepareMemoRepo's resume case). */
export function dropSeedContent(): void {
  useLaunchSeed.setState({ prompt: "", attachments: [] });
}

/** startFromMemo opens the Start working dialog seeded with the memo: straight into the
 *  memo's repository when this workspace has that working copy, else the hub to pick one
 *  (StartHost then runs prepareMemoRepo for the pick). When the repo's stored draft cannot
 *  be set aside nothing opens. The memo is deleted only once a session was launched. */
export async function startFromMemo(m: Memo): Promise<StashResult & { saved?: boolean }> {
  // memo.repo is the launch dialog's repo name, which is the working copy's own name.
  const repo = m.repo ? useReposStore.getState().repos.find((r) => r.name === m.repo) : undefined;
  const pre = repo ? await prepareMemoRepo(repo.name, m.id) : { ok: true as const };
  if (!pre.ok) return pre;
  const seed = useLaunchSeed.getState();
  seed.set(m.body);
  seed.setAttachments(m.attachments || []);
  seed.setMemoId(m.id);
  seed.setOnLaunched(() => void deleteRestoredMemo(m.id));
  if ("resume" in pre && pre.resume) dropSeedContent();
  if (repo) useLaunchTarget.getState().open(repo);
  else useSessionsStore.getState().openStart();
  return { ok: true, ...("saved" in pre && pre.saved ? { saved: true } : {}) };
}

// The session exists by now, so a failure here is only said, never undone: the memo stays
// in the queue and the person can delete it there.
async function deleteRestoredMemo(id: string): Promise<void> {
  try {
    const res = await memoDelete(id);
    if (!res.ok) toast(t("memo.start_delete_failed"));
  } catch {
    toast(t("memo.start_delete_failed"));
  }
  useMemoStore.getState().bump();
}
