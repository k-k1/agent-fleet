import { useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { NO_AUTOFILL } from "../../../ui/noAutofill.ts";
import { isOk } from "../api.ts";
import { Field } from "../parts/mcpForm.tsx";
import { createWorkspaceMcpServers, testMcpServer } from "./mcpApi.ts";
import type { WorkspaceMcpPreset, WorkspaceMcpService, WorkspaceScopeBundle } from "./mcpApi.ts";
import { ProbeView } from "./mcpServerRow.tsx";
import { MASKED, probeOfAnswer } from "./mcpWire.ts";
import type { ProbeResult, TenantServer } from "./mcpWire.ts";

export function WorkspaceMcpForm({ slug, preset, rows, onSaved, onCancel }: {
  slug: string;
  preset: WorkspaceMcpPreset;
  rows: TenantServer[];
  onSaved: () => void;
  onCancel: () => void;
}) {
  const tr = useT();
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [bundle, setBundle] = useState<WorkspaceScopeBundle>("read_only");
  const [selected, setSelected] = useState<string[]>([]);
  const [probes, setProbes] = useState<Record<string, ProbeResult | undefined>>({});
  const [testing, setTesting] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const existing = new Set(rows.map((row) => row.name.toLowerCase()));
  const services = preset.services.filter((service) => selected.includes(service.id) && !existing.has(service.name));
  const scopes = [...new Set(services.flatMap((service) => service[bundle]))].sort();
  const valid = clientId.trim() && secret.trim() && secret.trim() !== MASKED && services.length > 0;
  const test = async (service: WorkspaceMcpService) => {
    setTesting(service.id);
    setProbes((current) => ({ ...current, [service.id]: undefined }));
    try {
      // This runs in the current Workspace. Only the public server definition is needed.
      const res = await testMcpServer({ name: service.name, transport: "http", url: service.url, auth: "oauth" });
      if (!res) throw new TypeError("empty probe response");
      setProbes((current) => ({ ...current, [service.id]: probeOfAnswer(res, errText) }));
    } catch {
      setProbes((current) => ({ ...current, [service.id]: { ok: false, toolCount: 0, elapsedMs: 0 } }));
    } finally {
      setTesting("");
    }
  };

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await createWorkspaceMcpServers({
        tenant_slug: slug, client_id: clientId.trim(), client_secret: secret.trim(),
        services: services.map((service) => service.id), bundle,
      });
      if (!res) throw new TypeError("empty save response");
      if (!isOk(res)) {
        setError(errText(res.error));
        return;
      }
      setSecret("");
      onSaved();
    } catch {
      setError(tr("admin.mcp_workspace_save_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="ssm-frm mcp-frm adm-mcp-form">
      <div className="ssm-fgroup">
        <p className="ssm-fg-title">{tr("admin.mcp_workspace")}</p>
        <p className="ps-note">{tr("admin.mcp_workspace_client_hint")}</p>
        <div className="ssm-fgrid">
          <Field label={tr("admin.mcp_workspace_redirect")} wide>
            <code className="mcp-probe-mono">{preset.redirect_uri || "{PUBLIC_BASE_URL}/oauth2/mcp/callback"}</code>
          </Field>
          <Field label={tr("admin.mcp_oauth_client_id")} req>
            <input {...NO_AUTOFILL} className="cinput mono" aria-label={tr("admin.mcp_oauth_client_id")} maxLength={255}
              value={clientId} onChange={(e) => setClientId(e.target.value)} disabled={busy} />
          </Field>
          <Field label={tr("admin.mcp_oauth_secret")} req>
            <input className="cinput" type="password" autoComplete="new-password" aria-label={tr("admin.mcp_oauth_secret")} maxLength={1024}
              value={secret} onChange={(e) => setSecret(e.target.value)} disabled={busy} />
          </Field>
          <Field label={tr("admin.mcp_workspace_bundle")} wide hint={tr("admin.mcp_workspace_bundle_hint")}>
            <select className="cinput" aria-label={tr("admin.mcp_workspace_bundle")} value={bundle} disabled={busy}
              onChange={(e) => setBundle(e.target.value === "standard" ? "standard" : "read_only")}>
              <option value="read_only">{tr("admin.mcp_workspace_read_only")}</option>
              <option value="standard">{tr("admin.mcp_workspace_standard")}</option>
            </select>
          </Field>
          <Field label={tr("admin.mcp_workspace_services")} req wide>
            <p className="ps-note">{tr("admin.mcp_workspace_egress")}</p>
            <p className="ps-note">{tr("admin.mcp_workspace_probe_hint")}</p>
            {preset.services.map((service) => {
              const added = existing.has(service.name);
              const checked = !added && selected.includes(service.id);
              return (
                <div key={service.id}>
                  <div className="adm-mcp-row">
                    <label className="mcp-check">
                      <input type="checkbox" checked={checked} disabled={busy || added}
                        onChange={(e) => setSelected(e.target.checked
                          ? [...selected, service.id]
                          : selected.filter((id) => id !== service.id))} />
                      {service.label}
                    </label>
                    <span className="as-repo"><code title={service.host}>{service.host}</code></span>
                    {service.preview && <span className="muted">{tr("admin.mcp_workspace_preview")}</span>}
                    {added && <span className="muted">{tr("admin.mcp_workspace_exists")}</span>}
                    {checked && (
                      <span className="allow-acts">
                        <button type="button" className="ghost xs" disabled={busy || !!testing}
                          aria-label={tr("admin.mcp_workspace_test", { service: service.label })} onClick={() => void test(service)}>
                          {tr(testing === service.id ? "mcp.testing" : "mcp.test")}
                        </button>
                      </span>
                    )}
                  </div>
                  {checked && <ProbeView probe={probes[service.id]} hideSignInHint />}
                </div>
              );
            })}
          </Field>
          {scopes.length > 0 && (
            <div className="ssm-fld wide">
              <details className="mcp-scopes">
                <summary>{tr("admin.mcp_workspace_scopes", { n: scopes.length })}</summary>
                <ul className="mono mcp-scopes-mono">{scopes.map((scope) => <li key={scope}>{scope}</li>)}</ul>
              </details>
            </div>
          )}
        </div>
        {error && <p role="alert" className="form-err">{error}</p>}
      </div>
      <div className="ssm-frm-foot">
        <button type="button" className="primary" disabled={busy || !valid} onClick={() => void submit()}>
          {tr("admin.mcp_save_add")}
        </button>
        <button type="button" className="ghost" disabled={busy} onClick={onCancel}>{tr("common.cancel")}</button>
      </div>
    </div>
  );
}
