// The tenant's git provider OAuth apps (docs/log/71, ADR 0052).
//
// This decides which OAuth app the "connect with OAuth" buttons for GitHub / Bitbucket on a
// member's connections tab talk to. The app lives in the customer's own GitHub org or
// Bitbucket workspace, so the tenant admin owns it — it is not a deployment-wide env setting.
//
// There is no approval step (decision 3). Sign-in methods (tenantLogin) need super_admin
// approval because they grant the power to declare who someone is; these apps only clone, add
// no identity, have a CP-fixed redirect_uri, and hand the token only to the owner's own
// workspace. They take effect the moment they are saved.
//
// client_secret is write-only. A stored value is never returned, so saving with the field
// empty means "leave unchanged" (the server keeps the same contract).
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText, raw } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

interface GitOAuthApp {
  provider: string;
  client_id?: string;
  has_secret?: boolean;
  needs_secret?: boolean;
  updated_at?: string;
  redirect_uri?: string;
}

const PROVIDER_LABEL: Record<string, string> = { github: "GitHub", bitbucket: "Bitbucket", jira: "Jira" };

// Where to register: without a destination an admin is stuck on "where do I get a client_id".
// Bitbucket's OAuth consumers live under a workspace (/{workspace}/workspace/settings/api) and
// we do not know the workspace name, so guessing a URL would send them to a 404 — point at the
// procedure documentation instead.
const REGISTER_URL: Record<string, string> = {
  github: "https://github.com/settings/developers",
  bitbucket: "https://support.atlassian.com/bitbucket-cloud/docs/use-oauth-on-bitbucket-cloud/",
  // Jira is Atlassian like Bitbucket but registers elsewhere: 3LO apps live in the Developer
  // Console, and a Bitbucket consumer cannot be reused (docs/log/80 §80.17).
  jira: "https://developer.atlassian.com/console/myapps/",
};

export function TenantGitOAuthView({ slug }: { slug: string }) {
  const tr = useT();
  const [apps, setApps] = useState<GitOAuthApp[] | null>(null);

  const load = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/git-oauth`);
      if (d && !d.error) setApps(d.providers || []);
    } catch {
      /* transient; keep the previous values */
    }
  }, [slug]);
  useEffect(() => {
    load();
  }, [load]);

  if (!apps) return <p className="muted pad">{tr("common.loading")}</p>;
  return (
    <section className="admin-panel">
      <p className="admin-hint">{tr("tenant.git_oauth_intro")}</p>
      <p className="admin-hint">{tr("tenant.git_oauth_optional")}</p>
      {apps.map((app) => (
        <GitOAuthCard key={app.provider} slug={slug} app={app} onChanged={load} />
      ))}
    </section>
  );
}

function GitOAuthCard({ slug, app, onChanged }: { slug: string; app: GitOAuthApp; onChanged: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [clientID, setClientID] = useState(app.client_id || "");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    setClientID(app.client_id || "");
    setSecret("");
  }, [app.client_id, app.provider]);

  const base = `api/admin/tenants/${encodeURIComponent(slug)}/git-oauth/${encodeURIComponent(app.provider)}`;
  const registered = !!app.client_id;

  const save = async () => {
    setBusy(true);
    try {
      const res = await apiJSON(base, "PUT", { client_id: clientID.trim(), client_secret: secret.trim() });
      if (res?.error) {
        toast(errText(res.error), { kind: "warn" });
        return;
      }
      setSecret("");
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      onChanged();
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    try {
      await raw(base, { method: "DELETE" });
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="admin-fgroup">
      <h4>
        {PROVIDER_LABEL[app.provider] || app.provider}
        <span className="af-note">{registered ? tr("tenant.git_oauth_on") : tr("tenant.git_oauth_off")}</span>
      </h4>
      <div className="admin-fgrid">
        <label className="admin-fld wide">
          <span className="af-cap">{tr("tenant.git_oauth_client_id")}</span>
          <input type="text" value={clientID} onChange={(e) => setClientID(e.target.value)} />
        </label>
        {/* GitHub uses the device flow and has no secret. Omit the field entirely rather than
            greying it out: never show a field whose contents nobody can guess. */}
        {app.needs_secret && (
          <label className="admin-fld wide">
            <span className="af-cap">{tr("tenant.git_oauth_client_secret")}</span>
            <input
              type="password"
              placeholder={app.has_secret ? tr("tenant.git_oauth_secret_kept") : ""}
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
            />
            <span className="af-unit">{tr("tenant.git_oauth_secret_unit")}</span>
          </label>
        )}
      </div>
      {/* The callback is what gets pasted into the provider's registration screen; withhold it
          and the admin is stuck. An empty URL on a provider that has a callback means
          PUBLIC_BASE_URL is unset, which only the operator can fix — saying so here is the
          difference between that and "registered it, but it does not work". */}
      {app.needs_secret &&
        (app.redirect_uri ? (
          <p className="admin-hint">
            {tr("tenant.git_oauth_redirect")} <code>{app.redirect_uri}</code>
          </p>
        ) : (
          <p className="admin-hint warn">{tr("tenant.git_oauth_no_base_url")}</p>
        ))}
      {app.provider === "github" && <p className="admin-hint">{tr("tenant.git_oauth_gh_device")}</p>}
      {/* Bitbucket puts no scope on the authorize URL: whatever is ticked in the consumer's
          Permissions is what gets granted, so which boxes to tick can only be said here.
          Adding Pull requests: Read later requires every member to reconnect, because existing
          tokens have the old permissions baked in (docs/log/80 §80.19.3). */}
      {app.provider === "bitbucket" && <p className="admin-hint">{tr("tenant.git_oauth_bb_scopes")}</p>}
      {/* A 3LO app defaults to "in development", where only its creator can authorize it. The
          admin who created it can connect, so the check at registration time passes, and every
          other member is stopped by Atlassian's "You don't have access to this app" — which
          happens before the authorize screen, so af gets nothing back and they stay silently
          unconnected. */}
      {app.provider === "jira" && (
        <>
          <p className="admin-hint">{tr("tenant.git_oauth_jira_access")}</p>
          <p className="admin-hint">{tr("tenant.git_oauth_jira_scopes")}</p>
          <p className="admin-hint">{tr("tenant.git_oauth_jira_sharing")}</p>
        </>
      )}
      <p className="admin-hint">
        {tr("tenant.git_oauth_where")}{" "}
        <a href={REGISTER_URL[app.provider]} target="_blank" rel="noopener noreferrer">
          {REGISTER_URL[app.provider]}
        </a>
      </p>
      <div className="le-actions">
        <button className="primary" disabled={busy || !clientID.trim()} onClick={save}>
          {tr("common.save")}
        </button>
        {registered && (
          <button className="ghost" disabled={busy} onClick={remove}>
            {tr("tenant.git_oauth_remove")}
          </button>
        )}
        {saved && (
          <span className="saved-note">
            <Icon name="check" /> {tr("admin.saved")}
          </span>
        )}
      </div>
    </div>
  );
}
