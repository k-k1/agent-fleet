import { useRef, useState } from "react";
import { apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { humanSize } from "../../../lib/filemeta.ts";
import type { SecretFinding, ImportPreview } from "./memoryTypes.ts";

// TransferSection — 環境間の持ち出し / 取り込み（docs/log/39 ⑤ P3）。
//
// 持ち出しは既定が **bundle（全履歴）**。受け側で `git bundle verify` が通り、履歴ごと
// 移せる。tar.gz は「最新だけ軽く持ち出す」用の併設。
//
// 書き出しは Agent 側の secret スキャン（★4）を必ず通る。検出時は 409 が返るので、
// **何が引っかかったかを見せてから** ack 付きで叩き直す。ここで確認を省いて自動 ack
// すると、防御が実質無効になる（値そのものは Agent がマスクしており表示もしない）。
//
// 取り込みは受領（refs/imports へ独立系譜として保持）と適用（選択置き換え）を分ける。
// 受領だけでは live に触れないので、中身を見てから範囲を決められる。
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
  // 適用のしかた。replace = 選んだ範囲の内容だけ採る（既定）。migrate = 履歴ごと入れ替える。
  // 移設は bundle（履歴を運ぶ形式）でしか意味を持たないので tar では選ばせない。
  const [mode, setMode] = useState<"replace" | "migrate">("replace");
  const fileRef = useRef<HTMLInputElement>(null);

  // 保存は fetch → Blob → 一時 URL。素のリンク遷移にしないのは、409（secret 検出）を
  // JSON として受け取って確認ダイアログに回す必要があるため。
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
      if (fileRef.current) fileRef.current.value = ""; // 同じファイルを選び直せるように
    }
  };

  // 適用できるのは「この環境に受け皿があるルート」だけ（codex memories 未有効の環境等）。
  const available = (preview?.kinds ?? []).filter((k) => !(preview?.unavailable ?? []).includes(k.kind));
  const wholeKinds = available.filter((k) => !k.scopes);
  const projects = preview?.projects ?? [];
  const pickedProjects = projects.filter((p) => picked["project:" + p.slug]).map((p) => p.slug);
  const pickedKinds = wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => k.kind);
  // 移設は「全体を履歴ごと入れ替える」操作なので、範囲の選択は要らない（サーバ側でも
  // 全体固定にしている — 一部だけ入れ替えると履歴と live が食い違うため）。
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
      // importId はクエリにも載せる（CP の監査台帳は URL からしか target を採らない）。
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
