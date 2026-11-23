// The repository settings of one auto-run category (#4881): how .agent-fleet/autorun.yaml on the
// repository's default branch differs from the member's settings, the member's approval of all
// or some of the differences, and the member's settings written as such a file.
//
// Nothing is applied without the approval, and the approval names settings only: the Control
// Plane takes the values from the file at the commit the difference showed, and answers 409
// memo_queue_repo_stale when the file or the settings moved since. Reading the file needs the
// workspace (it fetches origin), so the check runs when the member asks for it, never on open.
import { useId, useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText, type ApiError } from "../../core/api/client.ts";
import { copyText } from "../../lib/clipboard.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import type { MemoRepoDiff, MemoRepoExport, MemoRepoIssue, MemoRepoSettings } from "../../types/memo.ts";
import { memoRepoApprove, memoRepoExport, memoRepoSettings } from "./api.ts";
import { readQueue } from "./queues.ts";
import { queueScope, useQueueSettingsCache } from "./queueSettingsCache.ts";
import { approvedKeys, readRepoExport, readRepoSettings, repoDiffRows, repoKeyLabel, repoValueText, shortCommit } from "./repoSettings.ts";

interface Props {
  categoryId: string;
  categoryName: string;
  /** The category has settings: only then is there something to export. */
  hasQueue: boolean;
  /** The tenant's switch is off, or the dialog is saving: the difference is shown, the approval
   *  is not offered. */
  blocked: boolean;
  /** The dialog holds edits not saved yet. An approval saves the settings as stored and the
   *  dialog then closes, so it would drop them: the approval waits until they are saved or
   *  cancelled. */
  dirty: boolean;
  /** An approval is on its way: the dialog must not save, remove or close meanwhile, or its
   *  write could land after the approval with the values from before it. */
  onBusy: (busy: boolean) => void;
  /** An approval saved the settings, or may have (its answer was lost): the dialog's draft is
   *  out of date and must not be saved over them. */
  onApplied: () => void;
}

type Check = { kind: "idle" } | { kind: "loading" } | { kind: "error"; message: string } | { kind: "loaded"; data: MemoRepoSettings };

export function MemoRepoSettingsCard({ categoryId, categoryName, hasQueue, blocked, dirty, onBusy, onApplied }: Props) {
  const tr = useT();
  const toast = useToast();
  const [check, setCheck] = useState<Check>({ kind: "idle" });
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [approving, setApproving] = useState(false);
  const [exported, setExported] = useState<MemoRepoExport | null>(null);
  const [exporting, setExporting] = useState(false);
  // Numbers the reads: only the newest one's answer is shown, so a slower earlier read cannot put
  // an older difference (and its ticks) back over a newer one.
  const readSeq = useRef(0);
  const ids = useId();

  const load = async () => {
    const mine = ++readSeq.current;
    setCheck({ kind: "loading" });
    try {
      const raw = await memoRepoSettings(categoryId);
      if (mine !== readSeq.current) return;
      const data = readRepoSettings(raw);
      if (!data) {
        setCheck({ kind: "error", message: errText((raw as { error?: ApiError } | null)?.error) || t("memo.repo_check_failed") });
        return;
      }
      // Every difference starts ticked: approving all is the common case, and each can be unticked.
      setChosen(new Set(repoDiffRows(data.diffs).map((r) => r.id)));
      setCheck({ kind: "loaded", data });
    } catch {
      if (mine === readSeq.current) setCheck({ kind: "error", message: t("memo.repo_check_failed") });
    }
  };

  // The dialog is closed rather than left open on a draft that may predate a saved approval: a
  // save from it would write the old values back. The caller refetches the settings.
  // scope is the one the approval was sent from: an answer lost after a switch to another scope
  // must not mark that scope's cache or drop its reads.
  const uncertain = (scope: string) => {
    // Before onApplied, whose refetch must be a read begun after the mark.
    useQueueSettingsCache.getState().markUnsure(scope, categoryId);
    toast(t("memo.repo_approve_unknown"), { kind: "warn" });
    onApplied();
  };

  const approve = async (data: MemoRepoSettings) => {
    const keys = approvedKeys(repoDiffRows(data.diffs), chosen);
    if (keys.length === 0 || approving || blocked || dirty || (data.category !== "" && data.category !== categoryName)) return;
    setApproving(true);
    onBusy(true);
    const scope = queueScope();
    try {
      const res = await memoRepoApprove(categoryId, { commit: data.commit, approvalTag: data.approvalTag, keys });
      // The client names an answer that is not the Control Plane's (a proxy's page, a gateway)
      // http_<status>: that one says nothing about whether the write happened.
      if (res?.error?.code && !res.error.code.startsWith("http_")) {
        // The Control Plane refused it with a reason: nothing was written.
        toast(errText(res.error) || t("memo.repo_approve_failed"), { kind: "warn" });
        // The file, the category or the settings moved: show the difference as it is now.
        if (res.error.code === "memo_queue_repo_stale") void load();
        return;
      }
      if (res?.error) {
        uncertain(scope);
        return;
      }
      const saved = readQueue(res);
      if (saved) useQueueSettingsCache.getState().applySaved(scope, saved);
      toast(t("memo.repo_applied"), { kind: "success" });
      onApplied();
    } catch {
      // The answer was lost: the approval may have been saved.
      uncertain(scope);
    } finally {
      setApproving(false);
      onBusy(false);
    }
  };

  const doExport = async () => {
    setExporting(true);
    try {
      const raw = await memoRepoExport(categoryId);
      const out = readRepoExport(raw);
      if (!out) {
        toast(errText((raw as { error?: ApiError } | null)?.error) || t("memo.repo_export_failed"), { kind: "warn" });
        return;
      }
      setExported(out);
    } catch {
      toast(t("memo.repo_export_failed"), { kind: "warn" });
    } finally {
      setExporting(false);
    }
  };

  const download = (out: MemoRepoExport) => {
    const url = URL.createObjectURL(new Blob([out.yaml], { type: "application/yaml" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "autorun.yaml";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  };

  const label = (key: string) => {
    const k = repoKeyLabel(key);
    return k ? tr(k) : key;
  };
  const value = (d: MemoRepoDiff, v: unknown) => {
    const r = repoValueText(d.key, v);
    return "word" in r ? <em>{tr(r.word)}</em> : <code>{r.text}</code>;
  };
  const issue = (i: MemoRepoIssue, n: number) => (
    <li key={n}>
      {i.line > 0 ? tr("memo.repo_line", { line: i.line }) + " " : ""}
      {i.key && <code>{i.key}</code>} {i.reason}
    </li>
  );

  const loaded = check.kind === "loaded" ? check.data : null;
  // The Control Plane compared the entry of the category's current name. When that is not the name
  // this dialog shows (renamed in another tab), the member would approve another entry than the
  // one they think: the approval waits for the dialog to be opened again.
  const renamed = loaded !== null && loaded.category !== "" && loaded.category !== categoryName;
  const held = dirty || renamed;
  const rows = loaded ? repoDiffRows(loaded.diffs) : [];
  const picked = approvedKeys(rows, chosen).length;

  return (
    <section className="ui-field memo-repo" aria-label={tr("memo.repo_title")}>
      <div className="memo-repo-head">
        <span className="ui-field-label memo-repo-title">{tr("memo.repo_title")}</span>
        <Button small type="button" disabled={check.kind === "loading" || approving} onClick={() => void load()}>
          {check.kind === "idle" ? tr("memo.repo_check") : tr("memo.repo_recheck")}
        </Button>
        {hasQueue && (
          <Button small variant="ghost" type="button" disabled={exporting} onClick={() => void doExport()}>
            {tr("memo.repo_export")}
          </Button>
        )}
      </div>
      <div className="ui-field-hint">{tr("memo.repo_hint", { path: ".agent-fleet/autorun.yaml" })}</div>

      {check.kind === "loading" && (
        <div className="ui-field-hint" role="status">
          {tr("memo.repo_loading")}
        </div>
      )}
      {check.kind === "error" && (
        <div className="ui-field-hint" data-invalid role="alert">
          {check.message}
        </div>
      )}
      {loaded && (
        <div className="memo-repo-result">
          <div className="ui-field-hint">
            {tr("memo.repo_source", { path: loaded.path, branch: loaded.branch, commit: shortCommit(loaded.commit) })}
            {loaded.approvedCommit &&
              " " + tr("memo.repo_approved", { commit: shortCommit(loaded.approvedCommit), when: fmtDateTime(loaded.approvedAt) })}
          </div>
          {!loaded.found && <div className="ui-field-hint">{tr("memo.repo_no_file", { path: loaded.path })}</div>}
          {loaded.errors.length > 0 && (
            <div className="memo-repo-errors" role="alert">
              <strong>{tr("memo.repo_invalid")}</strong>
              <ul>{loaded.errors.map(issue)}</ul>
            </div>
          )}
          {loaded.found && loaded.errors.length === 0 && !loaded.entry && (
            <div className="ui-field-hint">{tr("memo.repo_no_entry", { name: categoryName })}</div>
          )}
          {loaded.warnings.length > 0 && (
            <div className="memo-repo-warnings">
              <strong>{tr("memo.repo_not_applied")}</strong>
              <ul>{loaded.warnings.map(issue)}</ul>
            </div>
          )}
          {loaded.entry && loaded.errors.length === 0 && rows.length === 0 && (
            <div className="ui-field-hint" role="status">
              {tr("memo.repo_same")}
            </div>
          )}
          {rows.length > 0 && (
            <div className="memo-repo-card" role="group" aria-label={tr("memo.repo_differ")}>
              <strong className="memo-repo-card-title">{tr("memo.repo_differ")}</strong>
              <ul className="memo-repo-diffs">
                {rows.map((r) => (
                  <li key={r.id} className="memo-repo-diff" data-raise={r.raise || undefined}>
                    <label className="memo-repo-diff-pick">
                      <input
                        type="checkbox"
                        checked={chosen.has(r.id)}
                        aria-describedby={`${ids}-${r.id}`}
                        disabled={blocked || approving || held}
                        onChange={(e) =>
                          setChosen((s) => {
                            const n = new Set(s);
                            if (e.target.checked) n.add(r.id);
                            else n.delete(r.id);
                            return n;
                          })
                        }
                      />
                      <span className="memo-repo-diff-name">{r.id === "firing" ? tr("memo.q_when") : label(r.keys[0])}</span>
                      {r.raise && <span className="ui-badge ui-badge-warn memo-repo-raise">{tr("memo.repo_raise")}</span>}
                    </label>
                    <div id={`${ids}-${r.id}`}>
                    {r.diffs.map((d) =>
                      d.key === "instruction" ? (
                        <div key={d.key} className="memo-repo-diff-text">
                          <div className="ui-field-hint">{tr("memo.repo_current")}</div>
                          {/* Focusable so a long text scrolls from the keyboard too. */}
                          <pre tabIndex={0}>{typeof d.current === "string" ? d.current : ""}</pre>
                          <div className="ui-field-hint">{tr("memo.repo_file_value", { line: d.line })}</div>
                          <pre tabIndex={0}>{typeof d.file === "string" ? d.file : ""}</pre>
                        </div>
                      ) : (
                        <div key={d.key} className="memo-repo-diff-values">
                          {r.id === "firing" && <span className="memo-repo-diff-sub">{label(d.key)}</span>}
                          {value(d, d.current)}
                          <span aria-hidden="true">
                            {" → "}
                          </span>
                          <span className="memo-repo-sr">{" " + tr("memo.repo_becomes") + " "}</span>
                          {value(d, d.file)}
                          <span className="ui-field-hint"> {tr("memo.repo_line", { line: d.line })}</span>
                        </div>
                      ),
                    )}
                    </div>
                  </li>
                ))}
              </ul>
              <div className="memo-repo-foot">
                <span className="ui-field-hint memo-repo-foot-hint" data-invalid={held || undefined}>
                  {renamed
                    ? tr("memo.repo_renamed", { name: loaded.category })
                    : dirty
                      ? tr("memo.repo_save_first")
                      : tr("memo.repo_approve_hint")}
                </span>
                <Button
                  small
                  variant="primary"
                  type="button"
                  disabled={blocked || approving || held || picked === 0}
                  onClick={() => void approve(loaded)}
                >
                  {tr("memo.repo_approve", { count: picked })}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {exported && (
        <div className="memo-repo-export">
          <div className="ui-field-hint">{tr("memo.repo_export_hint", { path: exported.path })}</div>
          {exported.omitted.length > 0 && (
            <ul className="memo-repo-warnings">
              {exported.omitted.map((i, n) => (
                <li key={n}>
                  <code>{i.key}</code> {tr("memo.repo_export_omitted")}
                </li>
              ))}
            </ul>
          )}
          <textarea className="memo-repo-yaml" readOnly rows={12} value={exported.yaml} aria-label={tr("memo.repo_export")} spellCheck={false} />
          <div className="memo-repo-foot">
            <Button
              small
              type="button"
              onClick={() =>
                void copyText(exported.yaml).then((ok) =>
                  toast(ok ? t("memo.repo_copied") : t("common.copy_failed"), { kind: ok ? "success" : "warn" }),
                )
              }
            >
              {tr("memo.repo_copy")}
            </Button>
            <Button small type="button" onClick={() => download(exported)}>
              {tr("memo.repo_download")}
            </Button>
            <Button small variant="ghost" type="button" onClick={() => setExported(null)}>
              {tr("common.close")}
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}
