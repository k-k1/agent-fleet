// The tenant's defaults for its members' Console (ADR 0114 decision 8): today the agent the
// first-run card names. Tenants pay for different agents, so a new member is pointed at the
// one their team uses instead of being asked to choose. It names, never restricts.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { MEMBER_DEFAULT_AGENTS, agentOf } from "../../../agents/registry.ts";
import type { LaunchDefaults, OpsSettings, TenantMemberDefaults } from "../../../core/store/uiState.ts";

/** GET/PUT /api/admin/tenants/{slug}/member-defaults. */
export interface TenantMemberDefaultsView {
  tenant: string;
  defaults: TenantMemberDefaults;
  updated_by?: string;
  updated_at?: string;
}

const EMPTY_LAUNCH: LaunchDefaults = { agent: "", model: "", driver: "", permission_mode: "", budget_usd: null };
const EMPTY_OPS: OpsSettings = { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" };

/** The budget field's text as the CP's number: "" = unset (null), else a finite 0..10000 with at most
 *  two decimals; undefined = not acceptable. Typing keeps the text, so "1." stays "1." on screen. */
export function parseBudgetText(text: string): number | null | undefined {
  const v = text.trim();
  if (v === "") return null;
  if (!/^\d+(\.\d{0,2})?$/.test(v)) return undefined;
  const n = Number(v);
  return n >= 0 && n <= 10000 ? n : undefined;
}

/** The PUT body: every group, always. The CP keeps a group the body leaves out, so sending all of
 *  them is what lets an admin clear one. `repos` is one owner/name per line. */
export function defaultsBody(agent: string, launch: LaunchDefaults, repos: string, ops: OpsSettings) {
  return {
    agent,
    launch: { ...launch, agent: "" },
    repos: repos.split("\n").map((l) => l.trim()).filter(Boolean),
    ops,
  };
}

export function TenantMemberDefaultsPanel({ slug }: { slug: string }) {
  const tr = useT();
  const [view, setView] = useState<TenantMemberDefaultsView | null>(null);
  const [agent, setAgent] = useState("");
  const [launch, setLaunch] = useState<LaunchDefaults>(EMPTY_LAUNCH);
  const [budgetText, setBudgetText] = useState("");
  const [repos, setRepos] = useState("");
  const [ops, setOps] = useState<OpsSettings>(EMPTY_OPS);
  const fill = (d: TenantMemberDefaults) => {
    setAgent(d.agent || "");
    setLaunch({ ...EMPTY_LAUNCH, ...d.launch });
    setBudgetText(d.launch?.budget_usd == null ? "" : String(d.launch.budget_usd));
    setRepos((d.repos || []).join("\n"));
    setOps({ ...EMPTY_OPS, ...d.ops });
  };
  const [error, setError] = useState("");
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const path = `api/admin/tenants/${encodeURIComponent(slug)}/member-defaults`;
  const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

  // A failed load says so and offers a retry: a panel stuck on "loading" gives the admin nothing
  // to act on.
  const load = useCallback(async () => {
    setLoadFailed(false);
    try {
      const d = await api(path);
      if (d && !d.error && d.defaults) {
        setView(d);
        fill(d.defaults);
        setError("");
        return;
      }
      setError(d?.error ? errDetail(d.error) : tr("tenant.member_defaults_load_failed", { error: "" }));
    } catch (e) {
      setError(tr("tenant.member_defaults_load_failed", { error: message(e) }));
    }
    setLoadFailed(true);
  }, [path]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    const budget = parseBudgetText(budgetText);
    if (budget === undefined) {
      setError(tr("tenant.member_defaults_budget_invalid"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const res = await apiJSON(path, "PUT", defaultsBody(agent, { ...launch, budget_usd: budget }, repos, ops));
      if (res?.error) {
        setError(errDetail(res.error));
        return;
      }
      setView(res);
      if (res.defaults) fill(res.defaults);
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
    } catch (e) {
      // The selection stays, so pressing Save again retries it.
      setError(tr("tenant.member_defaults_save_failed", { error: message(e) }));
    } finally {
      setBusy(false);
    }
  };

  if (!view)
    return loadFailed ? (
      <div className="pad">
        <p className="admin-hint warn" role="alert">
          {error}
        </p>
        <button onClick={load}>{tr("tenant.member_defaults_retry")}</button>
      </div>
    ) : (
      <p className="muted pad">{tr("common.loading")}</p>
    );

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("tenant.member_defaults_title")}</h4>
        <p className="admin-hint">{tr("tenant.member_defaults_note")}</p>
        <label className="admin-fld">
          <span className="af-cap">{tr("tenant.member_defaults_agent")}</span>
          <select
            value={agent}
            onChange={(e) => {
              setAgent(e.target.value);
              setError("");
            }}
          >
            <option value="">{tr("tenant.member_defaults_none")}</option>
            {MEMBER_DEFAULT_AGENTS.map((k) => (
              <option key={k} value={k}>
                {agentOf(k).label}
              </option>
            ))}
          </select>
        </label>
        <h4>{tr("tenant.member_defaults_launch")}</h4>
        <p className="admin-hint">{tr("tenant.member_defaults_launch_note")}</p>
        <label className="admin-fld">
          <span className="af-cap">{tr("tenant.member_defaults_model")}</span>
          <input value={launch.model} onChange={(e) => setLaunch({ ...launch, model: e.target.value.trim() })} />
        </label>
        <label className="admin-fld">
          <span className="af-cap">{tr("tenant.member_defaults_driver")}</span>
          <select value={launch.driver} onChange={(e) => setLaunch({ ...launch, driver: e.target.value })}>
            <option value="">{tr("tenant.member_defaults_none")}</option>
            <option value="managed">{tr("tenant.member_defaults_driver_managed")}</option>
            <option value="terminal">{tr("tenant.member_defaults_driver_terminal")}</option>
          </select>
        </label>
        <label className="admin-fld">
          <span className="af-cap">{tr("tenant.member_defaults_perm")}</span>
          <select value={launch.permission_mode} onChange={(e) => setLaunch({ ...launch, permission_mode: e.target.value })}>
            <option value="">{tr("tenant.member_defaults_none")}</option>
            <option value="ask">{tr("tenant.member_defaults_perm_ask")}</option>
            <option value="skip">{tr("tenant.member_defaults_perm_skip")}</option>
          </select>
        </label>
        <label className="admin-fld">
          <span className="af-cap">{tr("tenant.member_defaults_budget")}</span>
          <input
            inputMode="decimal"
            value={budgetText}
            onChange={(e) => {
              setBudgetText(e.target.value);
              setError("");
            }}
          />
        </label>
        <h4>{tr("tenant.member_defaults_repos")}</h4>
        <p className="admin-hint">{tr("tenant.member_defaults_repos_note")}</p>
        <textarea rows={4} value={repos} onChange={(e) => setRepos(e.target.value)} aria-label={tr("tenant.member_defaults_repos")} />
        <h4>{tr("tenant.member_defaults_ops")}</h4>
        <p className="admin-hint">{tr("tenant.member_defaults_ops_note")}</p>
        {(
          [
            ["grafana_url", "tenant.member_defaults_grafana"],
            ["aws_sso_start_url", "tenant.member_defaults_sso"],
            ["aws_account_id", "tenant.member_defaults_acct"],
            ["aws_role_name", "tenant.member_defaults_role"],
          ] as const
        ).map(([k, label]) => (
          <label className="admin-fld" key={k}>
            <span className="af-cap">{tr(label)}</span>
            <input value={ops[k]} onChange={(e) => setOps({ ...ops, [k]: e.target.value.trim() })} />
          </label>
        ))}
        {error && (
          <p className="admin-hint warn" role="alert">
            {error}
          </p>
        )}
        <div className="le-actions">
          <button className="primary" disabled={busy} onClick={save}>
            {tr("common.save")}
          </button>
          {saved && (
            <span className="saved-note">
              <Icon name="check" /> {tr("admin.saved")}
            </span>
          )}
          {view.updated_at && <span className="muted">{tr("tenant.member_defaults_updated", { at: view.updated_at })}</span>}
        </div>
      </div>
    </section>
  );
}
