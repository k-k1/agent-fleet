// The tenant layer of the branch naming rules (ADR 0103 decision 10).
//
// The list is edited as JSON: a rule is `{match, name, base, types}` with per-kind overrides,
// and a form for that shape would be most of this feature's code for a screen an admin opens
// rarely. The CP runs the Agent's own checks and answers which rule failed and why, so the
// screen shows that answer verbatim and keeps the text as typed. Rules advise and never refuse
// (decision 8), which is why there is no enforce switch here.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export interface TenantBranchKind {
  prefix?: string;
  base?: string;
  from?: string[];
}

export interface TenantBranchRule {
  match: string;
  name?: string;
  base?: string;
  types?: Record<string, TenantBranchKind>;
}

/** GET/PUT /api/admin/tenants/{slug}/branch-rules. */
export interface TenantBranchRules {
  tenant: string;
  rules: TenantBranchRule[];
  updated_by?: string;
  updated_at?: string;
}

const EXAMPLE = `[
  {
    "match": "bitbucket.org/acme/*",
    "name": "{prefix}{key}",
    "base": "develop",
    "types": { "bugfix": { "prefix": "bugfix/", "from": ["Bug", "Defect"] } }
  }
]`;

const pretty = (rules: TenantBranchRule[]) =>
  rules.length ? JSON.stringify(rules, null, 2) : "";

export function TenantBranchRulesView({ slug }: { slug: string }) {
  const tr = useT();
  const [view, setView] = useState<TenantBranchRules | null>(null);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const path = `api/admin/tenants/${encodeURIComponent(slug)}/branch-rules`;

  const [loadFailed, setLoadFailed] = useState(false);
  const failText = (
    key: "tenant.branch_rules_load_failed" | "tenant.branch_rules_save_failed",
    e: unknown,
  ) => tr(key, { error: e instanceof Error ? e.message : String(e) });

  // A failed load says so and offers to retry: a panel stuck on "loading" gives the admin
  // nothing to act on.
  const load = useCallback(async () => {
    setLoadFailed(false);
    try {
      const d = await api(path);
      if (d && !d.error && Array.isArray(d.rules)) {
        setView(d);
        setText(pretty(d.rules));
        setError("");
        return;
      }
      setError(
        d?.error
          ? errDetail(d.error)
          : tr("tenant.branch_rules_load_failed", { error: "" }),
      );
    } catch (e) {
      setError(failText("tenant.branch_rules_load_failed", e));
    }
    setLoadFailed(true);
  }, [path]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    let rules: unknown = [];
    if (text.trim() !== "") {
      try {
        rules = JSON.parse(text);
      } catch (e) {
        setError(
          tr("tenant.branch_rules_not_json", {
            error: e instanceof Error ? e.message : String(e),
          }),
        );
        return;
      }
    }
    if (!Array.isArray(rules)) {
      setError(tr("tenant.branch_rules_not_list"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const res = await apiJSON(path, "PUT", { rules });
      if (res?.error) {
        // The server names the rule and the field ("rule 2: base "a..b" is not a branch name",
        // `unknown field "prefixes"`); errDetail keeps that message after a generic code's text.
        setError(errDetail(res.error));
        return;
      }
      setView(res);
      setText(pretty(res.rules || []));
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
    } catch (e) {
      // The text stays as typed, so pressing Save again retries it.
      setError(failText("tenant.branch_rules_save_failed", e));
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
        <button onClick={load}>{tr("tenant.branch_rules_retry")}</button>
      </div>
    ) : (
      <p className="muted pad">{tr("common.loading")}</p>
    );

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>
          {tr("tenant.branch_rules_title")}
          <span className="af-note">
            {tr("tenant.branch_rules_count", { n: view.rules.length })}
          </span>
        </h4>
        <p className="admin-hint">{tr("tenant.branch_rules_note")}</p>
        <label className="admin-fld wide">
          <span className="af-cap">{tr("tenant.branch_rules_json")}</span>
          <textarea
            className="branch-rules-json"
            rows={14}
            spellCheck={false}
            placeholder={EXAMPLE}
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setError("");
            }}
          />
        </label>
        <p className="admin-hint">{tr("tenant.branch_rules_fields_hint")}</p>
        <p className="admin-hint">{tr("tenant.branch_rules_poll_hint")}</p>
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
          {view.updated_at && (
            <span className="muted">
              {tr("tenant.branch_rules_updated", { at: view.updated_at })}
            </span>
          )}
        </div>
      </div>
    </section>
  );
}
