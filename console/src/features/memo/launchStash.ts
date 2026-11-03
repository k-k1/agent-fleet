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
import { memoCreate, memoDelete, memoImageURL, memoList, memoPasteImage } from "./api.ts";
import { raw } from "../../core/api/client.ts";
import { useMemoStore } from "./store.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { Memo, MemoAttachment } from "../../types/memo.ts";
import { memoAttachmentLabel } from "./attachName.ts";
import { clearLaunchPrompt, launchAttachKey, readLaunchPrompt } from "../repos/launchDraft.ts";
import { attachDraftSeq, clearAttachDraft, readAttachDraft, settleAttachDraft } from "../../lib/attachDraft.ts";
import type { AttachDraftRec } from "../../lib/attachDraft.ts";
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
 *  draft is a no-op; a failure leaves the draft untouched. A draft that is an untouched copy
 *  of a memo still in the queue (an earlier restore, cancelled or backed out of) is only
 *  cleared: saving it would duplicate that memo. `restoring` is the memo about to fill the
 *  box, checked first so the common case needs no list.
 *
 *  Both checks lean towards keeping: a copy needs the same text and every file byte for
 *  byte equal to the memo's stored one (a same-named newer log is not a copy, and a memo
 *  whose files are gone from the server holds no copy of them). And the draft is cleared
 *  only if it still is what was read at the start — the person may have opened the dialog
 *  and written something else while the save was on the wire. */
export async function stashStoredDraft(
  repo: string,
  restoring?: { body: string; attachments: MemoAttachment[] },
): Promise<DraftStashResult> {
  const before = await readStoredDraft(repo);
  if (!before) return { ok: false, reason: "changed" };
  if (!before.text && !before.recs.length) return { ok: true };
  const candidates = [
    ...(restoring ? [restoring] : []),
    ...(await listedMemos()).filter((m) => m.kind === "text").map((m) => ({ body: m.body, attachments: m.attachments || [] })),
  ].filter((m) => sameShape(before, m));
  let copy = false;
  for (const m of candidates) {
    if (await sameBytes(before.recs, m.attachments)) {
      copy = true;
      break;
    }
  }
  let saved = false;
  if (!copy) {
    const files = before.recs.flatMap((r) => (r.bytes ? [new File([r.bytes], r.name, { type: r.type })] : []));
    const r = await stashLaunchDraft(repo, before.text, files);
    if (!r.ok) return r;
    saved = true;
  }
  // Compare and clear: a draft that changed meanwhile is the person's newer work. It stays,
  // and the caller opens nothing over it (a restore would overwrite its text).
  const now = await readStoredDraft(repo);
  // Re-checked in the same synchronous step as the clear: readStoredDraft's caller resumes a
  // tick after it returned, and a write may have started in that tick.
  const key = launchAttachKey(repo);
  if (!now || !sameStored(before, now) || attachDraftSeq(key) !== now.seq || readLaunchPrompt(repo).trim() !== now.text) {
    return { ok: false, reason: "changed" };
  }
  clearLaunchPrompt(repo);
  await clearAttachDraft(key);
  return { ok: true, ...(saved ? { saved: true } : {}) };
}

/** stashStoredDraft's outcome. "changed": the draft was rewritten while it was being saved,
 *  so it was left in place (and any memo made of the earlier version stays). */
export type DraftStashResult = { ok: true; saved?: boolean } | { ok: false; reason: "too_large" | "failed" | "changed" };

interface StoredDraft {
  text: string;
  recs: AttachDraftRec[];
  /** The attachment write generation the records were read at (attachDraftSeq). */
  seq: number;
}

// One consistent snapshot of the stored draft: the records read while no attachment write
// started, and the text read after them, in the same step that saw the generation unchanged
// (text read before the IndexedDB get could be rewritten during it and still be cleared).
// Launch drafts are never uploaded (no session yet), so every record carries its bytes. Null
// when the draft kept changing under every attempt.
async function readStoredDraft(repo: string): Promise<StoredDraft | null> {
  const key = launchAttachKey(repo);
  for (let i = 0; i < 5; i++) {
    await settleAttachDraft(key);
    const seq = attachDraftSeq(key);
    if (seq === null) continue;
    const recs = await readAttachDraft(key);
    if (attachDraftSeq(key) !== seq) continue;
    return { text: readLaunchPrompt(repo).trim(), recs, seq };
  }
  return null;
}

// The queue as the server has it now; [] when it cannot be read (the draft is then saved).
async function listedMemos(): Promise<Memo[]> {
  try {
    const list = await memoList();
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

// A restored memo's files are staged under their display labels (fetchMemoAttachments), so
// that is what a copy of it holds in the attachment draft. Cheap pre-filter for sameBytes.
function sameShape(d: StoredDraft, m: { body: string; attachments: MemoAttachment[] }): boolean {
  const want = m.attachments.map((a) => memoAttachmentLabel(a.name)).sort();
  const have = d.recs.map((r) => r.name).sort();
  return d.text === m.body.trim() && want.length === have.length && want.every((n, i) => n === have[i]);
}

// Every draft file equals, byte for byte, the memo's stored file of the same label. A file
// that cannot be fetched proves nothing, so it counts as different.
async function sameBytes(recs: AttachDraftRec[], atts: MemoAttachment[]): Promise<boolean> {
  if (!recs.length) return true;
  const stored = await Promise.all(
    atts.map(async (a) => {
      try {
        const r = await raw(memoImageURL(a.name));
        return r.ok ? { label: memoAttachmentLabel(a.name), bytes: await (await r.blob()).arrayBuffer() } : null;
      } catch {
        return null;
      }
    }),
  );
  // sameShape already matched the counts, so a file that could not be fetched leaves a
  // draft file without a partner below.
  const pool = stored.filter((x): x is { label: string; bytes: ArrayBuffer } => !!x);
  return recs.every((rec) => {
    const i = pool.findIndex((x) => x.label === rec.name && !!rec.bytes && bytesEqual(x.bytes, rec.bytes));
    if (i < 0) return false;
    pool.splice(i, 1);
    return true;
  });
}

function sameStored(a: StoredDraft, b: StoredDraft): boolean {
  return (
    a.text === b.text &&
    a.recs.length === b.recs.length &&
    a.recs.every((r, i) => r.name === b.recs[i].name && r.type === b.recs[i].type && bytesEqual(r.bytes, b.recs[i].bytes))
  );
}

function bytesEqual(a?: ArrayBuffer, b?: ArrayBuffer): boolean {
  if (!a || !b) return a === b;
  if (a.byteLength !== b.byteLength) return false;
  const x = new Uint8Array(a);
  const y = new Uint8Array(b);
  for (let i = 0; i < x.length; i++) if (x[i] !== y[i]) return false;
  return true;
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
 *  memo's repository when this workspace has that working copy, else the hub to pick one
 *  (StartHost then runs stashStoredDraft for the pick). When the repo's stored draft cannot
 *  be set aside nothing opens. The memo is deleted only once a session was launched. */
export async function startFromMemo(m: Memo): Promise<DraftStashResult> {
  // memo.repo is the launch dialog's repo name, which is the working copy's own name.
  const repo = m.repo ? useReposStore.getState().repos.find((r) => r.name === m.repo) : undefined;
  const pre: DraftStashResult = repo ? await stashStoredDraft(repo.name, { body: m.body, attachments: m.attachments || [] }) : { ok: true };
  if (!pre.ok) return pre;
  const seed = useLaunchSeed.getState();
  seed.set(m.body);
  seed.setAttachments(m.attachments || []);
  seed.setMemoId(m.id);
  seed.setOnLaunched(() => void deleteRestoredMemo(m.id));
  if (repo) useLaunchTarget.getState().open(repo);
  else useSessionsStore.getState().openStart();
  return pre;
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
