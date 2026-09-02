import type { FormEvent } from "react";
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";

export function EgressView() {
  const tr = useT();
  const [data, setData] = useState<any | null>(null); // { egress, mode, enforce }
  const [list, setList] = useState<any[] | null>(null); // allowlist entries
  const [err, setErr] = useState("");
  const [days, setDays] = useState(7);
  const [entry, setEntry] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setErr("");
    try {
      const [d, al] = await Promise.all([
        api("api/admin/egress?days=" + days),
        api("api/admin/egress/allowlist"),
      ]);
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setData(d);
      setList(al?.allowlist || []);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [days, tr]);
  useEffect(() => {
    load();
  }, [load]);

  const setMode = async (enforce: boolean) => {
    setBusy(true);
    try {
      await apiJSON("api/admin/egress/mode", "PUT", { enforce });
      await load();
    } finally {
      setBusy(false);
    }
  };
  const addEntry = async (e: FormEvent) => {
    e.preventDefault();
    const v = entry.trim();
    if (!v) return;
    setBusy(true);
    try {
      await apiJSON("api/admin/egress/allowlist", "POST", { entry: v, reason });
      setEntry("");
      setReason("");
      await load();
    } finally {
      setBusy(false);
    }
  };
  const setState = async (id: string, state: string) => {
    setBusy(true);
    try {
      await apiJSON("api/admin/egress/allowlist/" + encodeURIComponent(id) + "/state", "POST", { state });
      await load();
    } finally {
      setBusy(false);
    }
  };

  const enforce = !!data?.enforce;
  const proposed = (list || []).filter((e: any) => e.state === "proposed");
  const active = (list || []).filter((e: any) => e.state === "active");
  const stats = data?.egress || [];

  return (
    <div className="admin-stage egress-view">
      {/* mode toggle */}
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.mode_label")}</span>
          {/* "log-only" / "enforce" はサーバ側モードの識別子そのもの（説明文の
              admin.egress_*_note でも同じ語で参照する）なので意図的に訳さない。 */}
          <span className="seg sm">
            <button
              type="button"
              className={"seg-btn" + (!enforce ? " active" : "")}
              disabled={busy}
              onClick={() => setMode(false)}
            >
              log-only
            </button>
            <button
              type="button"
              className={"seg-btn" + (enforce ? " active" : "")}
              disabled={busy}
              onClick={() => setMode(true)}
            >
              enforce
            </button>
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {enforce ? (
          <p className="form-err">{tr("admin.egress_enforce_note")}</p>
        ) : (
          <p className="muted">{tr("admin.egress_logonly_note")}</p>
        )}
        {err && <p className="form-err">{err}</p>}
      </section>

      {/* agent-proposed entries awaiting approval (docs/log/20 M4) */}
      {proposed.length > 0 && (
        <section className="admin-panel">
          <h4 className="egress-h">{tr("admin.egress_proposed")}</h4>
          {proposed.map((e: any) => (
            <div key={e.id} className="adm-allow-row">
              <span className="as-name mono" title={e.entry}>{e.entry}</span>
              <span className="as-repo muted" title={e.reason}>{e.reason}</span>
              <span className="muted" title={e.added_by}>{e.added_by}</span>
              <span className="allow-acts">
                <button type="button" className="btn xs" disabled={busy} onClick={() => setState(e.id, "active")}>{tr("admin.approve")}</button>
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setState(e.id, "retired")}>{tr("admin.reject")}</button>
              </span>
            </div>
          ))}
        </section>
      )}

      {/* active allowlist + add */}
      <section className="admin-panel">
        <h4 className="egress-h">{tr("admin.egress_allowlist")}</h4>
        <form className="egress-add" onSubmit={addEntry}>
          <input
            type="text"
            value={entry}
            onChange={(e) => setEntry(e.target.value)}
            placeholder={tr("admin.egress_entry_ph")}
          />
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={tr("admin.egress_reason_ph")}
          />
          <button type="submit" className="btn" disabled={busy || !entry.trim()}>{tr("admin.add")}</button>
        </form>
        {active.length === 0 ? (
          <p className="muted">{tr("admin.egress_no_entries")}</p>
        ) : (
          active.map((e: any) => (
            <div key={e.id} className="adm-allow-row">
              <span className="as-name mono" title={e.entry}>{e.entry}</span>
              <span className="as-repo muted" title={e.reason}>{e.reason}</span>
              <span className="muted" title={e.added_by}>{e.added_by}</span>
              <span className="allow-acts">
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setState(e.id, "retired")}>{tr("admin.retire")}</button>
              </span>
            </div>
          ))
        )}
      </section>

      {/* observed destinations */}
      <section className="admin-panel">
        <div className="usage-toolbar">
          <h4 className="egress-h">{tr("admin.egress_observed")}</h4>
          <label>
            {tr("admin.period")}
            <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
              <option value={1}>{tr("admin.days_1")}</option>
              <option value={7}>{tr("admin.days_7")}</option>
              <option value={30}>{tr("admin.days_30")}</option>
            </select>
          </label>
        </div>
        {data === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : stats.length === 0 ? (
          <p className="muted">{tr("admin.egress_no_records")}</p>
        ) : (
          <div className="adm-egress">
            {stats.map((e: any) => (
              <div key={e.host} className="adm-egress-row">
                <span className="as-name mono" title={e.host}>{e.host}</span>
                <span className="egress-allow">{tr("admin.egress_allowed", { n: e.allowed })}</span>
                {e.blocked > 0 && <span className="egress-block">{e.blocked} {enforce ? tr("admin.egress_blocked") : tr("admin.egress_blocked_candidate")}</span>}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

// --- TTS: VOICEVOX エンジンの管理者トグル（docs/log/24 Phase 2） -------------------
// super_admin のみ。AWS では ECS Service の desired count を 0↔1（オンデマンド起動・
// 停止中コスト 0）。起動〜ready まで 1〜2 分かかるので、その間は 5s ポーリングで
// 「準備中」を追従表示する（auto ルーティングは Polly JP が代読）。ECS 管理外（dev の
// 常駐 docker 等）ではトグルはルーティングの有効/無効のみ。
