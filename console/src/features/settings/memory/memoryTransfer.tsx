import { useRef, useState } from "react";
import { apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { humanSize } from "../../../lib/filemeta.ts";
import type { SecretFinding, ImportPreview } from "./memoryTypes.ts";

// TransferSection — export / import between environments (docs/log/39 item 5, P3).
//
// Export defaults to a bundle (full history): the receiving side can run `git bundle verify`
// and the history moves with it. tar.gz sits alongside it for taking just the latest state.
//
// Every export goes through the Agent's secret scan (★4). A hit returns 409, so the findings
// are shown FIRST and only then is the call repeated with ack. Skipping that confirmation and
// acking automatically would disable the guard (the values themselves are masked by the Agent
// and never displayed).
//
// Import separates receiving (kept in refs/imports as an independent lineage) from applying
// (replacing the selected scope). Receiving alone does not touch live, so the scope can be
// chosen after looking at the contents.
export function TransferSection({
  busy,
  setBusy,
  onApplied,
}: {
  busy: boolean;
  setBusy: (v: boolean) => void;
  onApplied: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [format, setFormat] = useState<"bundle" | "tar">("bundle");
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  // How to apply. replace = take only the contents of the chosen scope (default);
  // migrate = swap in the history as well. Migration only means anything for a bundle (the
  // format that carries history), so it is not offered for tar.
  const [mode, setMode] = useState<"replace" | "migrate">("replace");
  const fileRef = useRef<HTMLInputElement>(null);

  // Saving goes fetch -> Blob -> temporary URL rather than a plain link navigation, because a
  // 409 (secrets detected) has to be read as JSON and routed to the confirm dialog.
  const saveBlob = async (res: Response) => {
    const blob = await res.blob();
    const cd = res.headers.get("Content-Disposition") ?? "";
    const name = /filename="?([^";]+)"?/.exec(cd)?.[1] ?? "af-memory." + (format === "tar" ? "tar.gz" : "bundle");
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
    toast(tr("mem.export_done"), { kind: "success" });
  };

  const doExport = async (ack: boolean) => {
    setBusy(true);
    try {
      const res = await raw(
        "api/agents/memory/export?format=" + format + (ack ? "&ack=1" : ""),
      );
      if (res.status === 409) {
        const body = await res.json().catch(() => null);
        const secrets: SecretFinding[] = body?.secrets ?? [];
        const ok = await askConfirm({
          title: tr("mem.export_secret_title"),
          body: (
            <>
              <p>{tr("mem.export_secret_body", { n: secrets.length })}</p>
              <ul className="mem-secrets">
                {secrets.slice(0, 20).map((s, i) => (
                  <li key={i}>
                    <code>{s.rule}</code> {s.path}:{s.line} <span className="muted">{s.hint}</span>
                    {s.history && <span className="muted"> {tr("mem.export_secret_history")}</span>}
                  </li>
                ))}
              </ul>
              <p className="muted">{tr("mem.export_secret_hint")}</p>
            </>
          ),
          confirmLabel: tr("mem.export_anyway"),
          danger: true,
        });
        if (ok) await doExport(true);
        return;
      }
      if (!res.ok) {
        const body = await res.json().catch(() => null);
        toast(body?.error ? errDetail(body.error) : tr("mem.export_failed"));
        return;
      }
      await saveBlob(res);
    } finally {
      setBusy(false);
    }
  };

  const readFile = async (file: File) => {
    setBusy(true);
    setPreview(null);
    try {
      const fd = new FormData();
      fd.append("file", file, file.name);
      const res = await raw("api/agents/memory/import", { method: "POST", body: fd });
      const body = await res.json().catch(() => null);
      if (!res.ok) {
        toast(body?.error ? errDetail(body.error) : tr("mem.import_failed"));
        return;
      }
      setPicked({});
      setMode("replace");
      setPreview(body as ImportPreview);
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = ""; // so the same file can be picked again
    }
  };

  // Only a root this environment has somewhere to put can be applied (e.g. codex memories
  // are not enabled here).
  const available = (preview?.kinds ?? []).filter((k) => !(preview?.unavailable ?? []).includes(k.kind));
  const wholeKinds = available.filter((k) => !k.scopes);
  const projects = preview?.projects ?? [];
  const pickedProjects = projects.filter((p) => picked["project:" + p.slug]).map((p) => p.slug);
  const pickedKinds = wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => k.kind);
  // Migration swaps in everything including the history, so there is no scope to choose. The
  // server pins it to the whole tree too: replacing only part of it would leave the history
  // and live disagreeing.
  const migrating = mode === "migrate";
  const canApply = !!preview && (migrating || pickedProjects.length > 0 || pickedKinds.length > 0);

  const applyImport = async () => {
    if (!preview) return;
    const label = [
      ...projects.filter((p) => picked["project:" + p.slug]).map((p) => p.display),
      ...wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => tr("mem.scope_whole_root", { label: k.label })),
    ].join(tr("common.list_sep"));
    const ok = await askConfirm({
      title: tr(migrating ? "mem.import_migrate_confirm_title" : "mem.import_confirm_title"),
      body: migrating ? (
        <>
          <p>{tr("mem.import_migrate_confirm_body", { snapshots: preview.snapshots })}</p>
          <p className="muted">{tr("mem.import_migrate_confirm_note")}</p>
        </>
      ) : (
        <>
          <p>{tr("mem.import_confirm_body", { scope: label })}</p>
          <p className="muted">{tr("mem.restore_undo_hint")}</p>
        </>
      ),
      confirmLabel: tr(migrating ? "mem.import_migrate_do" : "mem.import_do"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // importId also goes in the query: the CP audit ledger takes its target from the URL only.
      const res = await apiJSON(
        "api/agents/memory/import/apply?importId=" + encodeURIComponent(preview.importId),
        "POST",
        {
          importId: preview.importId,
          mode,
          scope: { projects: pickedProjects, kinds: pickedKinds },
        },
      );
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(
        res.adopted
          ? tr("mem.import_migrated", { snapshots: preview.snapshots })
          : res.committed
            ? tr("mem.import_done", {
                written: res.written?.length ?? 0,
                deleted: res.deleted?.length ?? 0,
              })
            : tr("mem.import_nochange"),
        { kind: "success", persist: true },
      );
      setPreview(null);
      onApplied();
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="mem-section">
      <div className="mem-head">
        <h3>{tr("mem.transfer_title")}</h3>
      </div>

      {/* 持ち出し */}
      <div className="mem-transfer">
        <div className="mem-scope">
          <label>
            <input
              type="radio"
              checked={format === "bundle"}
              onChange={() => setFormat("bundle")}
            />
            {tr("mem.export_format_bundle")}
          </label>
          <label>
            <input type="radio" checked={format === "tar"} onChange={() => setFormat("tar")} />
            {tr("mem.export_format_tar")}
          </label>
          <button type="button" disabled={busy} onClick={() => void doExport(false)}>
            {tr("mem.export_do")}
          </button>
        </div>
        <p className="muted ds-hint">{tr("mem.export_note")}</p>
      </div>

      {/* 取り込み */}
      <div className="mem-transfer">
        <div className="mem-scope">
          <input
            ref={fileRef}
            type="file"
            className="cinput"
            accept=".bundle,.gz,.tgz,application/gzip,application/octet-stream"
            disabled={busy}
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) void readFile(f);
            }}
          />
        </div>
        <p className="muted ds-hint">{tr("mem.import_hint")}</p>
      </div>

      {preview && (
        <div className="mem-import">
          <p className="muted">
            {tr("mem.import_summary", {
              format: preview.format,
              snapshots: preview.snapshots,
              when: preview.headTs ? fmtDateTime(preview.headTs, DATETIME_FULL) : "-",
            })}
          </p>
          {preview.secrets.length > 0 && (
            <p className="mem-warn">{tr("mem.import_secrets", { n: preview.secrets.length })}</p>
          )}
          {/* 🔴 スキャンが失敗したときは「秘密 0 件」と読める画面にしない。走査できなかったのは
              「検出なし」より弱い保証で、Go 側も `SecretScanFailed = true // 失敗を「検出なし」に
              見せない` と明示している（internal/memoryx/memory_import.go）。旗が無いと
              secrets が [] になるだけで、警告が 1 つも出ずに「見つからなかった」と同じ画面になる。 */}
          {preview.secretScanFailed && (
            <p className="mem-warn">{tr("mem.import_secret_scan_failed")}</p>
          )}
          {preview.unavailable.length > 0 && (
            <p className="mem-warn">
              {tr("mem.import_unavailable", { kinds: preview.unavailable.join(tr("common.list_sep")) })}
            </p>
          )}
          {preview.rejected.length > 0 && (
            <p className="muted ds-hint" title={preview.rejected.slice(0, 50).join("\n")}>
              {tr("mem.import_rejected", { n: preview.rejected.length })}
            </p>
          )}
          {/* 適用のしかた。移設は履歴を運ぶ bundle でしか意味を持たない（tar は 1 世代
              しか無いので、選ばせると「履歴を捨てるだけ」の選択肢になる）。 */}
          {preview.format === "bundle" && (
            <div className="mem-scope">
              <span className="muted">{tr("mem.import_mode_label")}</span>
              <label>
                <input type="radio" checked={!migrating} onChange={() => setMode("replace")} />
                {tr("mem.import_mode_replace")}
              </label>
              <label>
                <input type="radio" checked={migrating} onChange={() => setMode("migrate")} />
                {tr("mem.import_mode_migrate")}
              </label>
            </div>
          )}
          {migrating && <p className="muted ds-hint">{tr("mem.import_mode_migrate_hint")}</p>}
          {projects.length === 0 && wholeKinds.length === 0 ? (
            <p className="muted pad">{tr("mem.import_none")}</p>
          ) : (
            <ul className="mem-picks">
              {projects.map((p) => (
                <li key={p.slug}>
                  <label title={p.slug}>
                    <input
                      type="checkbox"
                      disabled={migrating}
                      checked={!!picked["project:" + p.slug]}
                      onChange={() =>
                        setPicked((cur) => ({ ...cur, ["project:" + p.slug]: !cur["project:" + p.slug] }))
                      }
                    />
                    {p.display}
                    <span className="muted">
                      {" "}
                      {tr("mem.root_stats", { files: p.files, size: humanSize(p.bytes) })}
                    </span>
                  </label>
                </li>
              ))}
              {wholeKinds.map((k) => (
                <li key={k.kind}>
                  <label>
                    <input
                      type="checkbox"
                      disabled={migrating}
                      checked={!!picked["kind:" + k.kind]}
                      onChange={() =>
                        setPicked((cur) => ({ ...cur, ["kind:" + k.kind]: !cur["kind:" + k.kind] }))
                      }
                    />
                    {tr("mem.scope_whole_root", { label: k.label })}
                    <span className="muted">
                      {" "}
                      {tr("mem.root_stats", { files: k.files, size: humanSize(k.bytes) })}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          <div className="flow">
            <button type="button" disabled={busy || !canApply} onClick={() => void applyImport()}>
              {tr(migrating ? "mem.import_migrate_do" : "mem.import_do")}
            </button>
            <button type="button" className="ghost" onClick={() => setPreview(null)}>
              {tr("common.cancel")}
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
