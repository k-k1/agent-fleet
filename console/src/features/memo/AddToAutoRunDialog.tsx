// "Add to auto-run" on a work item's detail: a small picker that files a pointer to the item,
// with the member's own note if they typed one, in a category of notes. The item's text is never
// copied (ADR 0137 §4); a category that runs by itself hands the item to the session it starts.
//
// Layout and words are not final: every string is a `memo.addrun_*` catalogue key, and the
// fields below are plain labelled rows that can be reordered without touching the logic.
import { useEffect, useMemo, useState } from "react";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import type { Repo } from "../repos/store.ts";
import { PROVIDER_NAMES } from "../workitems/read.ts";
import { memoCategoryList, memoCreate, memoList, memoQueueList } from "./api.ts";
import { useMemoStore } from "./store.ts";
import { readMemoQueues } from "./queues.ts";
import { addToAutoRunInput, categoryChoices, defaultMemoRepo, memoRepoChoices } from "./addToAutoRun.ts";
import type { Memo, MemoCategory, MemoQueue, MemoReference } from "../../types/memo.ts";

interface Props {
  /** The item to point at. `url` is its address at the tracker. */
  reference: MemoReference;
  repos: Repo[];
  /** The working copy a launch from this item would default to ("" = none). */
  defaultRepo: string;
  onClose(): void;
}

export function AddToAutoRunDialog({ reference, repos, defaultRepo, onClose }: Props) {
  const tr = useT();
  const toast = useToast();
  const bump = useMemoStore((s) => s.bump);
  const bases = useMemo(() => memoRepoChoices(repos), [repos]);
  const [repo, setRepo] = useState(() => defaultMemoRepo(repos, defaultRepo));
  const [categoryId, setCategoryId] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [cats, setCats] = useState<MemoCategory[] | null>(null);
  // The notes only supply the names no category row has; if they cannot be read the table's
  // categories are still offered.
  const [memos, setMemos] = useState<Memo[]>([]);
  const [queues, setQueues] = useState<MemoQueue[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  // False when the organization has not allowed auto-run: the categories are still listed, but
  // nothing in them starts by itself, and the picker is where that has to be said.
  const [tenantOff, setTenantOff] = useState(false);
  // The repository list can land after the dialog opened; only an unchosen repository follows it.
  useEffect(() => {
    if (!repo) setRepo(defaultMemoRepo(repos, defaultRepo));
  }, [repo, repos, defaultRepo]);

  useEffect(() => {
    let alive = true;
    void Promise.all([memoCategoryList(), memoQueueList().catch(() => null), memoList().catch(() => null)])
      .then(([cList, qRaw, mList]) => {
        if (!alive) return;
        if (!Array.isArray(cList)) {
          setLoadFailed(true);
          return;
        }
        setCats(cList as MemoCategory[]);
        setMemos(Array.isArray(mList) ? mList : []);
        const qw = readMemoQueues(qRaw);
        setQueues(qw?.queues ?? []);
        setTenantOff(qw ? !qw.tenantEnabled : false);
      })
      .catch(() => alive && setLoadFailed(true));
    return () => {
      alive = false;
    };
  }, []);

  const choices = useMemo(() => categoryChoices(cats ?? [], queues, repo, memos), [cats, queues, repo, memos]);
  const chosen = choices.find((c) => c.key === categoryId);
  const autoRows = choices.filter((c) => c.autoRun);
  const plainRows = choices.filter((c) => !c.autoRun);
  const provider = PROVIDER_NAMES[reference.provider] || reference.provider;
  const repoLabel = repo || tr("memo.common");

  const submit = async () => {
    if (!chosen || busy) return;
    setBusy(true);
    const typed = note.trim() !== "";
    try {
      const res = await memoCreate(addToAutoRunInput(repo, chosen.name, reference, note));
      if (res.error) {
        const err = res.error as { code?: string };
        toast(err.code?.startsWith("memo_") ? errText(res.error) : tr("memo.addrun_failed"), { kind: "warn" });
        return;
      }
      const where = { key: reference.key, category: chosen.name, repo: repoLabel };
      if (res.existing) {
        // Nothing was added, so the text typed with it was not stored either.
        toast(tr("memo.addrun_existing", where) + (typed ? " " + tr("memo.addrun_existing_note") : ""), { kind: "warn" });
      } else {
        toast(tr("memo.addrun_done", where), { kind: "success" });
      }
      bump();
      onClose();
    } catch {
      toast(tr("memo.addrun_failed"), { kind: "warn" });
    } finally {
      setBusy(false);
    }
  };

  const option = (c: (typeof choices)[number]) => (
    <option key={c.key} value={c.key}>
      {c.name}
      {c.autoRun ? ` — ${tr(c.off ? "memo.autorun_badge_off" : "memo.autorun_badge")}` : ""}
    </option>
  );

  return (
    <ConfirmDialog
      title={tr("memo.addrun_title")}
      confirmLabel={tr("memo.addrun_confirm")}
      danger={false}
      busy={busy}
      confirmDisabled={!chosen || !reference.url}
      onConfirm={() => void submit()}
      onCancel={onClose}
    >
      <div className="memo-addrun">
        <p className="wi-shint">{tr("memo.addrun_intro", { key: reference.key, provider })}</p>
        <label className="wi-sfield">
          <span>{tr("memo.addrun_repo")}</span>
          <select
            value={repo}
            onChange={(e) => {
              setRepo(e.target.value);
              setCategoryId("");
            }}
          >
            {!repo && (
              <option value="" disabled>
                {tr("memo.addrun_repo_choose")}
              </option>
            )}
            {bases.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </label>
        <label className="wi-sfield">
          <span>{tr("memo.addrun_category")}</span>
          <select value={categoryId} disabled={!repo || !cats} onChange={(e) => setCategoryId(e.target.value)}>
            <option value="" disabled>
              {tr("memo.addrun_category_choose")}
            </option>
            {autoRows.length > 0 && <optgroup label={tr("memo.addrun_group_auto")}>{autoRows.map(option)}</optgroup>}
            {plainRows.length > 0 && <optgroup label={tr("memo.addrun_group_plain")}>{plainRows.map(option)}</optgroup>}
          </select>
        </label>
        {loadFailed && (
          <p className="wi-merr" role="alert">
            {tr("memo.addrun_load_failed")}
          </p>
        )}
        {!loadFailed && cats && repo && choices.length === 0 && <p className="wi-shint">{tr("memo.addrun_no_category")}</p>}
        {tenantOff && autoRows.length > 0 && <p className="wi-shint">{tr("memo.waiting_tenant_off")}</p>}
      {chosen && !chosen.autoRun && <p className="wi-shint">{tr("memo.addrun_plain_hint")}</p>}
        {!reference.url && (
          <p className="wi-merr" role="alert">
            {tr("memo.addrun_no_link")}
          </p>
        )}
        <label className="wi-sfield">
          <span>{tr("memo.addrun_note")}</span>
          <textarea rows={3} value={note} placeholder={tr("memo.addrun_note_ph")} onChange={(e) => setNote(e.target.value)} />
        </label>
      </div>
    </ConfirmDialog>
  );
}
