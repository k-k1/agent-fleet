// Deployment branding (control-plane/brand.go): the favicon / PWA icon colour and the
// short label folded into the app name, so several deployments of the same image are told
// apart in a tab strip and on a phone's home screen.
//
// It used to be AF_BRAND_* only, which meant a redeploy — on ECS a stack update — to change
// a favicon. Here it is a stored setting that wins over the environment; "follow the
// environment" is a separate action rather than "pick teal and clear the label", because on
// a deployment whose environment names a colour those are different outcomes.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { applyBrand, readableInk } from "../../../lib/brand.ts";

interface BrandStatus {
  color: string;
  hex: string;
  label: string;
  name: string;
  source: "admin" | "env" | "default";
  env: { color: string; label: string };
  presets: { name: string; hex: string }[];
  max_label: number;
}

export function BrandAdminView() {
  const tr = useT();
  const [data, setData] = useState<BrandStatus | null>(null);
  const [color, setColor] = useState("");
  const [label, setLabel] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  // The server's answer is the single source of both what is shown and what the form
  // starts from: it sanitises the label, so reading it back is how the field shows what
  // will actually appear in the title bar.
  const take = useCallback((d: BrandStatus) => {
    setData(d);
    setColor(d.color);
    setLabel(d.label);
    applyBrand({ label: d.label, color: d.hex, name: d.name });
  }, []);

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/brand");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      take(d as BrandStatus);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [take, tr]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/brand", "PUT", { color, label });
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      take(d as BrandStatus);
    } finally {
      setBusy(false);
    }
  };

  const reset = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/brand", "DELETE");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      take(d as BrandStatus);
    } finally {
      setBusy(false);
    }
  };

  const presets = data?.presets ?? [];
  const hex = presets.find((p) => p.name === color)?.hex ?? data?.hex ?? "#149ba7";
  const dirty = !!data && (color !== data.color || label !== data.label);
  // The preview is the two strings this setting actually produces: what a tab says, and
  // what a phone's home screen calls the installed app.
  const previewName = label.trim() ? `[${label.trim()}] Agent Fleet` : "Agent Fleet";

  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.brand_title")}</span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        <p className="muted">{tr("admin.brand_note")}</p>

        <div className="brand-field">
          <label>{tr("admin.brand_color")}</label>
          <div className="swatch-row">
            {presets.map((p) => (
              <button
                key={p.name}
                type="button"
                title={p.name}
                className={"swatch" + (p.name === color ? " active" : "")}
                style={{ background: p.hex, color: readableInk(p.hex) }}
                onClick={() => setColor(p.name)}
              >
                {p.name === color ? "✓" : ""}
              </button>
            ))}
          </div>
        </div>

        <div className="brand-field">
          <label htmlFor="brand-label">{tr("admin.brand_label")}</label>
          <input
            id="brand-label"
            type="text"
            value={label}
            maxLength={data?.max_label ?? 16}
            placeholder={tr("admin.brand_label_ph")}
            onChange={(e) => setLabel(e.target.value)}
          />
          <p className="muted">{tr("admin.brand_label_note")}</p>
        </div>

        <div className="brand-preview">
          <span className="brand-env" style={{ background: hex, color: readableInk(hex) }}>
            {label.trim() || tr("admin.brand_preview_none")}
          </span>
          <span className="brand-preview-title">{previewName} — Console</span>
        </div>

        {err && <p className="form-err">{err}</p>}
        <div className="brand-actions">
          <button type="button" className="btn primary" disabled={busy || !dirty} onClick={save}>
            {busy ? tr("admin.saving") : tr("common.save")}
          </button>
          {/* Only offered when there is a stored choice to drop; on an unbranded
              deployment the button would be a no-op that reads as broken. */}
          {data?.source === "admin" && (
            <button type="button" className="btn" disabled={busy} onClick={reset}>
              {tr("admin.brand_reset")}
            </button>
          )}
        </div>
        <p className="muted">
          {data?.source === "admin"
            ? tr("admin.brand_src_admin", {
                color: data.env.color || "teal",
                label: data.env.label || tr("admin.brand_preview_none"),
              })
            : data?.source === "env"
              ? tr("admin.brand_src_env")
              : tr("admin.brand_src_default")}
        </p>
        <p className="muted">{tr("admin.brand_pwa_note")}</p>
      </section>
    </div>
  );
}
