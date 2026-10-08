// ClaudeExportPanel — the one-shot write-back of AF memory into claude's own auto-memory (ADR 0108
// decision 6, #1914). Picking a project previews what would be written without touching
// anything; the confirm button then sends the preview's token, so an apply is refused when AF
// memory or claude's files moved since. A conflict (a file AF did not write, or one that changed
// since) is skipped unless its checkbox is ticked; secrets have no checkbox at all.
//
// It does not need the Agent Fleet memory switch: it writes claude's store, and its main use is
// after the member turned the switch off.

import { useCallback, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import type {
  ClaudeExportPreview,
  ClaudeExportResult,
  ClaudeExportSource,
  ClaudeExportStatus,
} from "./memoryTypes.ts";

const STATUSES: ClaudeExportStatus[] = ["new", "update", "remove", "conflict", "secret", "unchanged", "native_only"];
// A leaf the Agent will not replace even when named.
const LOCKED = new Set(["symlink", "too_large", "unreadable"]);
const INDEX_KEY = {
  new: "mem.ce_index_new",
  rewrite: "mem.ce_index_rewrite",
  unchanged: "mem.ce_index_unchanged",
  symlink: "mem.ce_index_symlink",
} as const;
const reasonLabel = (r: string): string => tMaybe("mem.ce_reason_" + r) ?? r;

export function ClaudeExportPanel({ reload }: { reload: number }) {
  const tr = useT();
  const toast = useToast();
  const [sources, setSources] = useState<ClaudeExportSource[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [project, setProject] = useState("");
  const [preview, setPreview] = useState<ClaudeExportPreview | null>(null);
  const [previewErr, setPreviewErr] = useState("");
  const [overwrite, setOverwrite] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [again, setAgain] = useState(0);

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api("api/agents/memory/claude-export");
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    setLoadErr(res?.error ? errText(res.error) : "");
    setSources(res?.error ? [] : (res?.projects ?? []));
    return true;
  }, []);
  useRetryLoad(load, [reload, again]);

  // Only the latest request may answer: a slow preview for a project picked earlier must not
  // replace the one shown, or the token would be paired with another project.
  const seqRef = useRef(0);
  const loadPreview = useCallback(async (id: string) => {
    const seq = ++seqRef.current;
    setPreview(null);
    setPreviewErr("");
    setOverwrite(new Set());
    if (!id) return;
    const res = await api("api/agents/memory/claude-export/preview?project=" + encodeURIComponent(id));
    if (seq !== seqRef.current) return;
    if (res?.error) {
      setPreviewErr(errDetail(res.error));
      return;
    }
    setPreview(res as ClaudeExportPreview);
  }, []);

  const pick = (id: string) => {
    setProject(id);
    void loadPreview(id);
  };

  const toggle = (name: string) =>
    setOverwrite((prev) => {
      const next = new Set(prev);
      if (!next.delete(name)) next.add(name);
      return next;
    });

  const count = (st: ClaudeExportStatus) => preview?.counts[st] ?? 0;
  const changes = count("new") + count("update") + count("remove") + overwrite.size;
  const canApply = !!preview && !busy && (changes > 0 || preview.index === "new" || preview.index === "rewrite");

  const run = async () => {
    if (!preview) return;
    setBusy(true);
    try {
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const res = await apiJSON("api/agents/memory/claude-export?project=" + encodeURIComponent(project), "POST", {
        project,
        token: preview.token,
        overwrite: [...overwrite],
      });
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      const results = (res?.results ?? []) as ClaudeExportResult[];
      const done = results.filter((r) => r.result !== "skipped").length;
      const skipped = results.length - done;
      toast(tr(skipped ? "mem.ce_done_skipped" : "mem.ce_done", { n: done, skipped }), {
        kind: skipped ? undefined : "success",
      });
    } catch {
      // The request may or may not have reached the Agent: re-read what is there now.
      toast(tr("mem.ce_failed"));
    } finally {
      setBusy(false);
      setAgain((n) => n + 1);
      void loadPreview(project);
    }
  };

  const statusLabels: Record<ClaudeExportStatus, string> = {
    new: tr("mem.ce_status_new"),
    update: tr("mem.ce_status_update"),
    remove: tr("mem.ce_status_remove"),
    conflict: tr("mem.ce_status_conflict"),
    secret: tr("mem.ce_status_secret"),
    unchanged: tr("mem.ce_status_unchanged"),
    native_only: tr("mem.ce_status_native_only"),
  };
  const statusLabel = (st: ClaudeExportStatus) => statusLabels[st];
  const group = (st: ClaudeExportStatus) => {
    const items = (preview?.items ?? []).filter((i) => i.status === st);
    if (items.length === 0) return null;
    return (
      <details key={st} open={st !== "unchanged" && st !== "native_only"} className="mem-ci-group">
        <summary>
          <span className={"mem-badge ci-" + st}>{statusLabel(st)}</span> {items.length}
        </summary>
        <ul className="mem-ci-items">
          {items.map((i) => (
            <li key={i.name}>
              {st === "conflict" && (
                <input
                  type="checkbox"
                  aria-label={tr("mem.ce_overwrite", { name: i.name })}
                  checked={overwrite.has(i.name)}
                  disabled={busy || LOCKED.has(i.reason ?? "") || i.reason === "forgotten_but_changed"}
                  onChange={() => toggle(i.name)}
                />
              )}{" "}
              <span className="mem-af-name">{i.name}</span>
              {i.reason && <span className="muted"> · {reasonLabel(i.reason)}</span>}
              {st === "secret" && (
                <ul className="mem-findings">
                  {(i.findings ?? []).map((f, n) => (
                    <li key={n}>
                      <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
      </details>
    );
  };

  return (
    <div className="mem-ci">
      <h4>{tr("mem.ce_title")}</h4>
      <p className="muted ds-hint">{tr("mem.ce_intro")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {sources === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : sources.length === 0 ? (
        !loadErr && <p className="muted">{tr("mem.ce_none")}</p>
      ) : (
        <label className="mem-scope">
          <span className="muted">{tr("mem.ce_source")}</span>
          <select value={project} disabled={busy} onChange={(e) => pick(e.target.value)}>
            <option value="">{tr("mem.ce_pick")}</option>
            {sources.map((s) => (
              <option key={s.project.id} value={s.project.id} disabled={!!s.reason}>
                {`${s.project.display} (${s.count})${s.reason ? " — " + reasonLabel(s.reason) : ""}`}
              </option>
            ))}
          </select>
        </label>
      )}
      {previewErr && <p className="mem-warn">{previewErr}</p>}
      {project && !preview && !previewErr && <p className="muted">{tr("common.loading")}</p>}
      {preview && (
        <>
          <p className="muted ds-hint">{tr("mem.ce_to", { slug: preview.slug })}</p>
          {preview.switchOn && <p className="mem-warn">{tr("mem.ce_switch_on")}</p>}
          {STATUSES.map(group)}
          {preview.userScope > 0 && <p className="muted ds-hint">{tr("mem.ce_user_scope", { n: preview.userScope })}</p>}
          {(preview.notForClaude ?? 0) > 0 && (
            <p className="muted ds-hint">{tr("mem.ce_other_kinds", { n: preview.notForClaude ?? 0 })}</p>
          )}
          {(preview.withheld ?? 0) > 0 && <p className="mem-warn">{tr("mem.ce_withheld", { n: preview.withheld ?? 0 })}</p>}
          {preview.truncated && <p className="mem-warn">{tr("mem.ci_truncated")}</p>}
          <p className="muted ds-hint">
            {tr(INDEX_KEY[preview.index], { listed: preview.indexListed, more: preview.indexMore })}
          </p>
          <div className="mem-ci-actions">
            <Button variant="primary" disabled={!canApply} onClick={() => void run()}>
              {tr("mem.ce_apply", { n: changes })}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
