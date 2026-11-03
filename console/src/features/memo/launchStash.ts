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
import { clearLaunchPrompt, launchAttachKey, readLaunchPrompt } from "../repos/launchDraft.ts";
import { clearAttachDraft, readAttachDraft } from "../../lib/attachDraft.ts";

export type StashResult = { ok: true } | { ok: false; reason: "too_large" | "failed" };

/** stashLaunchDraft saves a Start working draft as a memo in the repo's bucket. Nothing is
 *  created unless every file uploaded: a memo missing one of the screenshots it was written
 *  around would be cleared from the dialog as if it were whole. */
export async function stashLaunchDraft(repo: string, prompt: string, files: File[]): Promise<StashResult> {
  const attachments: MemoAttachment[] = [];
  try {
    for (const f of files) {
      const res = await memoPasteImage(f);
      if (res.status !== 201 || !res.path || !res.name) return { ok: false, reason: res.status === 413 ? "too_large" : "failed" };
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

/** startFromMemo opens the Start working dialog seeded with the memo: straight into the
 *  memo's repository when this workspace has that working copy, else the hub to pick one.
 *  A draft already stored for that repository is saved to a memo first (stashStoredDraft);
 *  when that fails nothing opens. The memo is deleted only once a session was launched. */
export async function startFromMemo(m: Memo): Promise<StashResult & { saved?: boolean }> {
  // memo.repo is the launch dialog's repo name, which is the working copy's own name.
  const repo = m.repo ? useReposStore.getState().repos.find((r) => r.name === m.repo) : undefined;
  const pre = repo ? await stashStoredDraft(repo.name) : { ok: true as const };
  if (!pre.ok) return pre;
  const seed = useLaunchSeed.getState();
  seed.set(m.body);
  seed.setAttachments(m.attachments || []);
  seed.setMemoId(m.id);
  seed.setOnLaunched(() => {
    void memoDelete(m.id)
      .catch(() => {})
      .finally(() => useMemoStore.getState().bump());
  });
  if (repo) useLaunchTarget.getState().open(repo);
  else useSessionsStore.getState().openStart();
  return pre;
}
