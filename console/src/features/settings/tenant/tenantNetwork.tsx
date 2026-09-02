// テナントの接続元制限（docs/log/66・ADR 0047）。
//
// ★ この画面が守っているのは 1 点だけ:「CP から見えている自分のアドレス」を
// 表示するのではなく、それを含まない一覧の保存を**サーバが拒否する**こと。
// 表示だけだと、プロキシ未申告のデプロイで見えている ALB の私有アドレスを
// 「これが私の IP か」と登録でき、絞ったつもりで全員を通す（決定 4）。
// だから editable / reason はサーバの答えをそのまま写し、押せない理由を書く。
//
// ★ ネットワーク防御ではないことも画面に書く。要求は ALB を通り CP に届き、
// セッションが検証された後で拒否される。効くのは「資格情報を持った人が、
// 許されていない場所からデータに触る」だけである（決定 1）。
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

interface NetworkView {
  allowed_cidrs?: string;
  your_ip?: string;
  proxy_hops?: number;
  editable?: boolean;
  reason?: string;
}

export function TenantNetworkView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [view, setView] = useState<NetworkView | null>(null);
  const [cidrs, setCidrs] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/network`);
      if (d && !d.error) {
        setView(d);
        setCidrs(d.allowed_cidrs || "");
      }
    } catch {
      /* transient; the panel simply stays on its last values */
    }
  }, [slug]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/network`, "PUT", {
        allowed_cidrs: cidrs,
      });
      if (res?.error) {
        // ★ サーバの文言をそのまま出す。締め出しの拒否は「あなたのアドレスは
        // 203.0.113.9 で、この一覧に含まれていない」まで言って初めて直せる。
        toast(errText(res.error));
        return;
      }
      // 正規化された結果を書き戻す（192.0.2.7/24 は 192.0.2.0/24 として保存される）。
      setCidrs(res?.allowed_cidrs || "");
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      load();
    } finally {
      setBusy(false);
    }
  };

  if (!view) return <p className="muted pad">{tr("common.loading")}</p>;
  const on = (view.allowed_cidrs || "").trim() !== "";

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>
          {tr("tenant.net_title")}
          <span className="af-note">{on ? tr("tenant.net_on") : tr("tenant.net_off")}</span>
        </h4>
        <div className="admin-fgrid">
          <label className="admin-fld wide">
            <span className="af-cap">{tr("tenant.net_allowed")}</span>
            <input
              type="text"
              placeholder="203.0.113.0/24, 198.51.100.7"
              value={cidrs}
              disabled={!view.editable}
              onChange={(e) => setCidrs(e.target.value)}
            />
            <span className="af-unit">{tr("tenant.net_allowed_unit")}</span>
          </label>
          <div className="admin-fld">
            <span className="af-cap">{tr("tenant.net_your_ip")}</span>
            <span className={"af-val" + (view.your_ip ? "" : " unset")}>
              {view.your_ip || tr("tenant.net_ip_unknown")}
            </span>
            <span className="af-unit">{tr("tenant.net_your_ip_unit")}</span>
          </div>
        </div>
        {view.reason === "proxy_not_configured" && (
          <p className="admin-hint warn">{tr("tenant.net_proxy_not_configured")}</p>
        )}
        {view.reason === "client_ip_unknown" && <p className="admin-hint warn">{tr("tenant.net_ip_unknown_hint")}</p>}
        <p className="admin-hint">{tr("tenant.net_scope_hint")}</p>
        <p className="admin-hint">{tr("tenant.net_exempt_hint")}</p>
        <p className="admin-hint">{tr("tenant.net_layers_hint")}</p>
        <div className="le-actions">
          <button className="primary" disabled={busy || !view.editable} onClick={save}>
            {tr("common.save")}
          </button>
          {saved && (
            <span className="saved-note">
              <Icon name="check" /> {tr("admin.saved")}
            </span>
          )}
        </div>
      </div>
    </section>
  );
}
