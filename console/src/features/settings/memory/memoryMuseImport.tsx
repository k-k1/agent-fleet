// MuseImportPanel — the one-time import of muse's native memory into AF memory (ADR 0108
// decision 6, #1991, #2005). It follows the claude import panel: preview first, confirm sends the
// previewed items in batches.
//
// Three muse stores are offered: a working copy's `.agents/memory` (project), muse's own directory
// for a working copy (personal_project) and muse's notes for every project (personal). Only the
// last reaches AF's user scope, which every project reads, so nothing is previewed or sent for it
// until the member ticks the box that says so. A file git tracks may be shared with teammates, so
// the preview says so before the member confirms. Applying needs the Agent Fleet memory switch on.

import { useCallback, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { useSettings } from "../../../lib/settings.ts";
import { WizardFrame, GroupedPreview, Findings, type WizardStep } from "./memoryWizard.tsx";
import { IMPORT_BATCH } from "./memoryClaudeImport.tsx";
import type { ClaudeImportResult, MuseImportItem, MuseImportPreview, MuseImportSource, MuseImportStatus, MuseScope } from "./memoryTypes.ts";

const STATUSES: MuseImportStatus[] = ["new", "update", "unchanged", "diverged", "forgotten", "secret", "invalid"];
// An unknown reason from a newer Agent is printed raw rather than breaking the row.
const reasonLabel = (r: string): string => tMaybe("mem.mi_reason_" + r) ?? tMaybe("mem.ci_reason_" + r) ?? r;

// A select value names the scope and the directory: two scopes can share a directory name.
const srcKey = (s: { scope: MuseScope; dir: string }) => s.scope + "|" + s.dir;

export function MuseImportPanel({ reload, onChanged, onClose }: { reload: number; onChanged: () => void; onClose?: () => void }) {
  const tr = useT();
  const toast = useToast();
  const enabled = useSettings().agentMemory;
  const [sources, setSources] = useState<MuseImportSource[] | null>(null);
  const [withheld, setWithheld] = useState(0);
  const [loadErr, setLoadErr] = useState("");
  const [key, setKey] = useState("");
  const [intoUser, setIntoUser] = useState(false);
  const [preview, setPreview] = useState<MuseImportPreview | null>(null);
  const [previewErr, setPreviewErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [again, setAgain] = useState(0);
  const [result, setResult] = useState<string | null>(null);
  // The outcome is both toasted and kept in the frame until another source is picked.
  const notify = (msg: string, opts?: { kind?: "success" }) => {
    toast(msg, opts);
    setResult(msg);
  };

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api("api/agents/memory/muse-import");
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    setLoadErr(res?.error ? errText(res.error) : "");
    setSources(res?.error ? [] : (res?.sources ?? []));
    setWithheld(res?.error ? 0 : (res?.withheld ?? 0));
    return true;
  }, []);
  useRetryLoad(load, [reload, again]);

  // Only the latest request may answer, or the confirm would pair one project with another dir.
  const previewSeq = useRef(0);
  const loadPreview = useCallback(async (k: string, user: boolean) => {
    const seq = ++previewSeq.current;
    setPreview(null);
    setPreviewErr("");
    if (!k) return;
    const [scope, d] = [k.slice(0, k.indexOf("|")), k.slice(k.indexOf("|") + 1)];
    // The personal scope is not even read until the member has said it goes to the user scope.
    if (scope === "personal" && !user) return;
    const q = scope === "project" ? "dir=" + encodeURIComponent(d) : "scope=" + scope + "&dir=" + encodeURIComponent(d);
    const res = await api("api/agents/memory/muse-import/preview?" + q + (scope === "personal" ? "&into=user" : ""));
    if (seq !== previewSeq.current) return;
    if (res?.error) {
      setPreviewErr(errDetail(res.error));
      return;
    }
    setPreview(res as MuseImportPreview);
  }, []);

  const pick = (k: string) => {
    setKey(k);
    setResult(null);
    setIntoUser(false);
    void loadPreview(k, false);
  };
  const pickUser = (on: boolean) => {
    setIntoUser(on);
    setResult(null);
    void loadPreview(key, on);
  };

  const todo: MuseImportItem[] = (preview?.items ?? []).filter((i) => i.status === "new" || i.status === "update");
  const sharedTodo = todo.filter((i) => i.shared).length;

  const run = async () => {
    const toUser = preview?.target === "user";
    if (!preview || (!toUser && !preview.project) || todo.length === 0) return;
    const project = preview.project?.id ?? "";
    setBusy(true);
    setProgress({ done: 0, total: todo.length });
    let imported = 0;
    let skipped = 0;
    try {
      for (let at = 0; at < todo.length; at += IMPORT_BATCH) {
        const items = todo.slice(at, at + IMPORT_BATCH).map((i) => ({ name: i.name, sourceHash: i.sourceHash }));
        // The project id also goes in the query: the CP audit ledger reads the URL only.
        const res = await apiJSON("api/agents/memory/muse-import?project=" + encodeURIComponent(project), "POST", {
          scope: preview.scope,
          project,
          dir: preview.dir,
          ...(toUser ? { into: "user" } : {}),
          items,
        });
        if (res?.error) {
          notify(errDetail(res.error));
          return;
        }
        for (const r of (res?.results ?? []) as ClaudeImportResult[]) {
          if (r.result === "skipped") skipped++;
          else imported++;
        }
        setProgress({ done: Math.min(at + IMPORT_BATCH, todo.length), total: todo.length });
      }
      notify(tr(skipped ? "mem.ci_done_skipped" : "mem.ci_done", { n: imported, skipped }), {
        kind: skipped ? undefined : "success",
      });
    } catch {
      // The request may or may not have reached the Agent: re-read what is there now.
      notify(tr("mem.ci_failed"));
    } finally {
      setBusy(false);
      setProgress(null);
      setAgain((n) => n + 1);
      onChanged();
      void loadPreview(key, intoUser);
    }
  };

  const scopeNames: Record<MuseScope, string> = {
    project: tr("mem.mi_scope_project"),
    personal_project: tr("mem.mi_scope_personal_project"),
    personal: tr("mem.mi_scope_personal"),
  };
  const scopeLabel = (s: MuseScope) => scopeNames[s] ?? s;
  const sourceLabel = (s: MuseImportSource) =>
    `${scopeLabel(s.scope)}: ${s.target === "user" ? tr("mem.mi_to_user") : (s.project?.display ?? s.dir)} (${s.count})${s.reason ? " — " + reasonLabel(s.reason) : ""}`;

  const statusLabel: Record<MuseImportStatus, string> = {
    new: tr("mem.mi_status_new"),
    update: tr("mem.mi_status_update"),
    unchanged: tr("mem.mi_status_unchanged"),
    diverged: tr("mem.mi_status_diverged"),
    forgotten: tr("mem.mi_status_forgotten"),
    secret: tr("mem.mi_status_secret"),
    invalid: tr("mem.mi_status_invalid"),
  };
  const waiting = !!key && (!key.startsWith("personal|") || intoUser);
  const step: WizardStep = busy ? "apply" : result ? "result" : preview ? "confirm" : waiting ? "preview" : "source";

  return (
    <WizardFrame title={tr("mem.mi_title")} intro={tr("mem.mi_intro")} step={step} busy={busy} result={result} onClose={onClose}>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {withheld > 0 && <p className="mem-warn">{tr("mem.mi_withheld_sources", { n: withheld })}</p>}
      {sources === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : sources.length === 0 ? (
        !loadErr && <p className="muted">{tr("mem.mi_none")}</p>
      ) : (
        <label className="mem-scope">
          <span className="muted">{tr("mem.mi_source")}</span>
          <select value={key} disabled={busy} onChange={(e) => pick(e.target.value)}>
            <option value="">{tr("mem.ci_pick")}</option>
            {sources.map((s) => (
              <option key={srcKey(s)} value={srcKey(s)} disabled={!s.project && s.target !== "user"}>
                {sourceLabel(s)}
              </option>
            ))}
          </select>
        </label>
      )}
      {previewErr && <p className="mem-warn">{previewErr}</p>}
      {key.startsWith("personal|") && (
        <label className="mem-scope">
          <input type="checkbox" checked={intoUser} disabled={busy} onChange={(e) => pickUser(e.target.checked)} />{" "}
          <span>{tr("mem.mi_user_choice")}</span>
        </label>
      )}
      {key && (!key.startsWith("personal|") || intoUser) && !preview && !previewErr && (
        <p className="muted">{tr("common.loading")}</p>
      )}
      {preview && (
        <>
          <p className="muted ds-hint">
            {tr("mem.ci_to", { project: preview.target === "user" ? tr("mem.mi_to_user") : (preview.project?.display ?? "") })}
          </p>
          {(preview.items ?? []).some((i) => i.derived) && <p className="muted ds-hint">{tr("mem.mi_derived_note")}</p>}
          {(preview.items ?? []).some((i) => i.shared) && <p className="mem-warn">{tr("mem.mi_shared_warn")}</p>}
          <GroupedPreview
            items={preview.items ?? []}
            order={STATUSES}
            statusOf={(i) => i.status}
            label={(st) => statusLabel[st as MuseImportStatus]}
            badge={(st) => "ci-" + st}
            open={(st) => st === "new" || st === "update" || st === "secret"}
            keyOf={(i) => i.name + "|" + (i.path ?? "")}
            renderItem={(i, st) => (
              <>
                <span className="mem-af-name">{i.name}</span>
                {i.path && <span className="muted"> ({i.path})</span>}
                {i.derived && <span className="mem-badge"> {tr("mem.mi_derived_badge")}</span>}
                {i.shared && <span className="mem-badge"> {tr("mem.mi_shared_badge")}</span>}
                {st === "update" && i.sourceModified && i.afUpdated && (
                  <span className="muted">
                    {" · "}
                    {tr("mem.mi_update_times", {
                      muse: fmtDateTime(i.sourceModified, DATETIME_FULL),
                      af: fmtDateTime(i.afUpdated, DATETIME_FULL),
                    })}
                  </span>
                )}
                {(st === "invalid" || st === "diverged") && i.reason && (
                  <span className="muted"> · {reasonLabel(i.reason)}</span>
                )}
                {st === "secret" && <Findings findings={i.findings} />}
              </>
            )}
          />
          {(preview.withheld ?? 0) > 0 && (
            <p className="mem-warn">{tr("mem.ci_withheld_files", { n: preview.withheld ?? 0 })}</p>
          )}
          {preview.truncated && <p className="mem-warn">{tr("mem.ci_truncated")}</p>}
          {!enabled && <p className="mem-warn">{tr("mem.ci_off")}</p>}
          <div className="mem-ci-actions">
            <Button variant="primary" disabled={busy || !enabled || todo.length === 0} onClick={() => void run()}>
              {tr("mem.ci_apply", { n: todo.length })}
            </Button>
            {sharedTodo > 0 && <span className="muted">{tr("mem.mi_apply_shared", { n: sharedTodo })}</span>}
            {progress && (
              <span className="muted" role="status">
                {tr("mem.ci_progress", { done: progress.done, total: progress.total })}
              </span>
            )}
          </div>
        </>
      )}
    </WizardFrame>
  );
}
