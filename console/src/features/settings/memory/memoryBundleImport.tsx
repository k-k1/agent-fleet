// BundleAfPanel — the one-shot import of AF memory out of a memory bundle (#1983, ADR 0108). The
// generic bundle receive (the same upload Memory snapshots > Export & import makes) keeps the
// file's lineage under an import id; its apply never writes AF memory (no secret scan), so this is
// where it is brought in: previewed first, project by project, then sent in batches of 50.
// BundleImportWizard is the Import & export entry: it takes the file in and hands the import id on.
//
// Only each memory's latest text comes across. The bundle's revision, authors, dates, pins and
// history do not (the Agent resets them and records where each came from), a memory that already
// exists here is never replaced, and one forgotten here is never brought back.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, getTenant, isTransientErr, raw } from "../../../core/api/client.ts";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";
import { WizardFrame, GroupedPreview, PreviewSection, Findings, type WizardStep } from "./memoryWizard.tsx";
import type { BundleItem, BundlePreview, BundleResult, BundleScope } from "./memoryTypes.ts";

/** The Agent's per-request cap (agentMemBundleBatch). */
export const BUNDLE_BATCH = 50;

const STATUSES: BundleItem["status"][] = ["new", "exists", "forgotten", "secret", "invalid", "unchanged"];
const statusLabel = (s: string): string => tMaybe("mem.bi_status_" + s) ?? s;
const reasonLabel = (r: string): string => tMaybe("mem.bi_reason_" + r) ?? tMaybe("mem.ci_reason_" + r) ?? r;
const scopeName = (s: BundleScope, tr: (k: "mem.bi_user_scope") => string): string =>
  s.source === "user" ? tr("mem.bi_user_scope") : (s.display ?? s.source);

// owner names who is asking: the tenant the client sends to and the signed-in user. An apply that
// started under one must not send its next batch under another.
const ownerNow = (): string => getTenant() + "\u0000" + (useTenantStore.getState().whoami?.user ?? "");

export function BundleAfPanel({
  importId,
  onChanged,
  parentBusy = false,
  bare = false,
  showEmpty = false,
  onState,
}: {
  importId: string;
  onChanged: () => void;
  /** The surrounding import is uploading or applying: no second action at the same time. */
  parentBusy?: boolean;
  /** The wizard frame around it already carries the title and the intro. */
  bare?: boolean;
  /** Say so when the bundle holds no Agent Fleet memory, instead of drawing nothing. */
  showEmpty?: boolean;
  /** Where the run is, for the frame around it: idle, in flight, or finished. */
  onState?: (s: "idle" | "busy" | "done", outcome?: string) => void;
}) {
  const tr = useT();
  const toast = useToast();
  // Who the screen belongs to, as the Console holds it now: a change of tenant or user discards
  // the preview, the progress and any run in flight, and is read again for the new owner.
  const ownerKey = useTenantStore((s) => s.tenant) + "\u0000" + useTenantStore((s) => s.whoami?.user ?? "");
  const [shown, setShown] = useState<{ owner: string; preview: BundlePreview } | null>(null);
  // The preview counts only for the owner it was read under, whatever arrives late.
  const preview = shown && shown.owner === ownerKey ? shown.preview : null;
  const [loadErr, setLoadErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [again, setAgain] = useState(0);
  // The run in flight holds the generation it started under; leaving the panel, picking another
  // bundle or another tenant moves it on, and an old run stops at its next await without a toast,
  // a state change or a refresh of someone else's list.
  const gen = useRef(0);
  useEffect(() => {
    setShown(null);
    setProgress(null);
    setBusy(false);
    return () => {
      gen.current++;
    };
  }, [importId, ownerKey]);

  const load = useCallback(
    async (signal: AbortSignal) => {
      const res = await api("api/agents/memory/af-import/preview?importId=" + encodeURIComponent(importId));
      if (signal.aborted) return true;
      if (isTransientErr(res)) return false;
      setLoadErr(res?.error ? errText(res.error) : "");
      setShown(res?.error ? null : { owner: ownerKey, preview: res as BundlePreview });
      return true;
    },
    [importId, ownerKey],
  );
  useRetryLoad(load, [importId, again, ownerKey]);

  if (loadErr) return <p className="mem-warn">{loadErr}</p>;
  if (!preview) return showEmpty ? <p className="muted">{tr("common.loading")}</p> : null;
  if (!preview.hasAF) return showEmpty ? <p className="muted">{tr("mem.bi_none")}</p> : null;

  // What would be sent: the new memories of every scope that has a local target.
  const todo = preview.scopes.flatMap((s) =>
    s.target || s.source === "user"
      ? s.items
          .filter((i) => i.status === "new")
          .map((i) => ({ scope: s.source, target: s.target?.id ?? "", name: i.name, sourceHash: i.sourceHash ?? "" }))
      : [],
  );

  const run = async () => {
    if (!preview) return;
    const mine = ++gen.current;
    const owner = ownerNow();
    const live = () => gen.current === mine && ownerNow() === owner;
    setBusy(true);
    onState?.("busy");
    setProgress({ done: 0, total: todo.length });
    let imported = 0;
    let skipped = 0;
    // What the member is told, toasted and handed to the frame so it outlasts the toast.
    let outcome = "";
    const say = (msg: string, opts?: { kind?: "success" }) => {
      outcome = msg;
      toast(msg, opts);
    };
    try {
      for (let at = 0; at < todo.length; at += BUNDLE_BATCH) {
        if (!live()) return;
        const items = todo.slice(at, at + BUNDLE_BATCH);
        // The import id also goes in the query: the CP audit ledger reads the URL only.
        const res = await apiJSON("api/agents/memory/af-import?importId=" + encodeURIComponent(importId), "POST", {
          importId,
          items,
        });
        if (!live()) return;
        if (res?.error) {
          say(errDetail(res.error));
          return;
        }
        for (const r of (res?.results ?? []) as BundleResult[]) {
          if (r.result === "imported") imported++;
          else skipped++;
        }
        setProgress({ done: Math.min(at + BUNDLE_BATCH, todo.length), total: todo.length });
      }
      say(tr(skipped ? "mem.bi_done_skipped" : "mem.bi_done", { n: imported, skipped }), {
        kind: skipped ? undefined : "success",
      });
    } catch {
      // The request may or may not have reached the Agent: read what is there now.
      if (live()) say(tr("mem.bi_failed"));
    } finally {
      if (live()) {
        setBusy(false);
        setProgress(null);
        onState?.("done", outcome);
        setAgain((n) => n + 1);
        onChanged();
      }
    }
  };

  return (
    <div className={bare ? undefined : "mem-ci"}>
      {!bare && <h4>{tr("mem.bi_title")}</h4>}
      {!bare && <p className="muted ds-hint">{tr("mem.bi_intro")}</p>}
      {preview.incomplete && <p className="mem-warn">{tr("mem.bi_incomplete")}</p>}
      {preview.truncated && <p className="mem-warn">{tr("mem.ci_truncated")}</p>}
      {(preview.withheld ?? 0) > 0 && <p className="mem-warn">{tr("mem.bi_withheld", { n: preview.withheld ?? 0 })}</p>}
      {preview.scopes.map((s) => (
        <PreviewSection
          key={s.source}
          open={!!s.target || s.source === "user"}
          summary={
            <>
              <span className="mem-af-name">{scopeName(s, tr)}</span>
              {s.target ? (
                <span className="muted">
                  {" → "}
                  {s.target.display} {s.match === "remote" ? tr("mem.bi_by_remote") : ""}
                </span>
              ) : s.source !== "user" ? (
                <span className="muted">
                  {" — "}
                  {tr(
                    s.reason === "ambiguous"
                      ? "mem.bi_ambiguous"
                      : s.reason === "search_incomplete"
                        ? "mem.bi_incomplete_scope"
                        : "mem.bi_no_local",
                    { n: s.counts["skipped"] ?? 0 },
                  )}
                </span>
              ) : null}
            </>
          }
        >
          <GroupedPreview
            items={s.items}
            order={STATUSES}
            statusOf={(i) => i.status}
            label={statusLabel}
            badge={(st) => "ci-" + st}
            open={(st) => st !== "unchanged"}
            keyOf={(i) => i.name}
            renderItem={(i, st) => (
              <>
                <span className="mem-af-name">{i.name}</span>
                {i.reason && <span className="muted"> · {reasonLabel(i.reason)}</span>}
                {st === "secret" && <Findings findings={i.findings} />}
              </>
            )}
          />
          {(s.withheld ?? 0) > 0 && <p className="mem-warn">{tr("mem.ce_withheld", { n: s.withheld ?? 0 })}</p>}
        </PreviewSection>
      ))}
      <div className="mem-ci-actions">
        <Button variant="primary" disabled={busy || parentBusy || todo.length === 0} onClick={() => void run()}>
          {progress ? tr("mem.ci_progress", { done: progress.done, total: progress.total }) : tr("mem.bi_apply", { n: todo.length })}
        </Button>
      </div>
    </div>
  );
}

/**
 * Import & export > Agent Fleet memory from a bundle. The file is received through the same upload
 * Memory snapshots makes (it keeps the bundle as an independent lineage and touches no live
 * memory); the import id it returns is all BundleAfPanel needs. A tar.gz has no Agent Fleet
 * memory, so it is turned away here instead of showing an empty preview.
 */
export function BundleImportWizard({ onChanged, onClose }: { onChanged: () => void; onClose?: () => void }) {
  const tr = useT();
  const toast = useToast();
  // Who the wizard belongs to: a change of tenant or user discards the received import id and any
  // upload still in flight, so an id the Agent minted for one owner is never read under another.
  const ownerKey = useTenantStore((s) => s.tenant) + "\u0000" + useTenantStore((s) => s.whoami?.user ?? "");
  const fileRef = useRef<HTMLInputElement>(null);
  // The id counts only for the owner it was received under; the check is at render time so a child
  // never gets the chance to read it for another owner (an effect would reset one render too late).
  const [received, setReceived] = useState<{ owner: string; id: string } | null>(null);
  const importId = received && received.owner === ownerKey ? received.id : "";
  const [uploading, setUploading] = useState(false);
  const [run, setRun] = useState<"idle" | "busy" | "done">("idle");
  const [result, setResult] = useState<string | null>(null);
  const [err, setErr] = useState("");
  // The upload in flight holds the generation it started under; picking another file, a change of
  // owner or leaving the wizard moves it on, and a late answer is dropped.
  const gen = useRef(0);
  useEffect(() => {
    setReceived(null);
    setUploading(false);
    setRun("idle");
    setResult(null);
    setErr("");
    return () => {
      gen.current++;
    };
  }, [ownerKey]);

  const readFile = async (file: File) => {
    const mine = ++gen.current;
    const owner = ownerNow();
    const live = () => gen.current === mine && ownerNow() === owner;
    setUploading(true);
    setReceived(null);
    setRun("idle");
    setResult(null);
    setErr("");
    try {
      const fd = new FormData();
      fd.append("file", file, file.name);
      // The header is what keeps another page out: the CP signs this upload for the Agent only when
      // it carries it (ADR 0113).
      const res = await raw("api/agents/memory/import", { method: "POST", headers: { "X-AF-Console": "1" }, body: fd });
      const body = await res.json().catch(() => null);
      if (!live()) return;
      if (!res.ok) {
        setErr(body?.error ? errDetail(body.error) : tr("mem.import_failed"));
        return;
      }
      if (body?.format !== "bundle") {
        setErr(tr("mem.bi_not_bundle"));
        return;
      }
      setReceived({ owner, id: String(body.importId) });
    } catch {
      if (live()) {
        setErr(tr("mem.import_failed"));
        toast(tr("mem.import_failed"));
      }
    } finally {
      if (live()) setUploading(false);
      if (fileRef.current) fileRef.current.value = ""; // so the same file can be picked again
    }
  };

  const busy = uploading || run === "busy";
  const step: WizardStep = run === "busy" ? "apply" : run === "done" ? "result" : importId ? "confirm" : uploading ? "preview" : "source";
  return (
    <WizardFrame title={tr("mem.bi_title")} intro={tr("mem.bi_intro")} step={step} busy={busy} result={result} onClose={onClose}>
      <div className="mem-scope">
        <input
          ref={fileRef}
          type="file"
          className="cinput"
          aria-label={tr("mem.bi_pick")}
          accept=".bundle,application/octet-stream"
          disabled={busy}
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void readFile(f);
          }}
        />
      </div>
      {uploading && <p className="muted">{tr("common.loading")}</p>}
      {err && <p className="mem-warn">{err}</p>}
      {importId && (
        <BundleAfPanel
          importId={importId}
          onChanged={onChanged}
          bare
          showEmpty
          onState={(st, outcome) => {
            setRun(st);
            setResult(st === "done" ? (outcome ?? null) : null);
          }}
        />
      )}
    </WizardFrame>
  );
}

/**
 * The pointer Memory snapshots > Export & import shows under a received bundle that carries Agent
 * Fleet memory. The restore there never writes it (no secret scan); the import lives in Agent
 * memory > Import & export. Nothing is drawn for a bundle without it.
 */
export function BundleAfPointer({ importId }: { importId: string }) {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  const [hasAF, setHasAF] = useState(false);
  const load = useCallback(
    async (signal: AbortSignal) => {
      const res = await api("api/agents/memory/af-import/preview?importId=" + encodeURIComponent(importId));
      if (signal.aborted) return true;
      if (isTransientErr(res)) return false;
      setHasAF(!res?.error && !!res?.hasAF);
      return true;
    },
    [importId],
  );
  useRetryLoad(load, [importId]);
  if (!hasAF) return null;
  return (
    <p className="muted ds-hint">
      {tr("mem.bi_pointer")}{" "}
      <button type="button" className="linklike" onClick={() => openSettings("memory:importexport")}>
        {tr("mem.bi_pointer_go")}
      </button>
    </p>
  );
}
