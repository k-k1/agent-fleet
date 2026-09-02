import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, isTransientErr, errText, rawJSON } from "../../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { Button } from "../../../ui/Button.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Hint } from "../parts/providerCard.tsx";
import { useT } from "../../../lib/i18n/index.ts";
// The wire contract (types, masked-secret round-trip, form↔definition mapping)
// lives beside this file so its rules are unit-tested — see mcpWire.test.ts.
import { bodyOf, emptyForm, formOf } from "./mcpWire.ts";
import type { Form, McpServer, ProbeResult, Registry } from "./mcpWire.ts";
// Egress allowlist tie-in (docs/log/48 §9): a remote server the deployment's proxy will not
// let the workspace reach is warned about here, where it can still be acted on.
import { useEgressCheck } from "./EgressNote.tsx";
import { hostsOf } from "./egressCheck.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { ServerRow } from "./mcpServerRow.tsx";
import { SecretsForm, ServerForm } from "./mcpServerForm.tsx";

// McpTab — the member's own MCP server registry (docs/log/48 P1 + ADR0031). Lists the
// EFFECTIVE registry (builtin ∪ tenant ∪ user) as one table, because that is what the
// assistants and sessions actually see; origin decides what is editable here:
//   user    … full CRUD
//   tenant  … read-only, but locally disableable (the member's escape hatch when a
//             distributed server breaks their session launches)
//   builtin … the ops integrations (PagerDuty / Grafana / CloudWatch / AWS), configured on
//             the 運用・監視 tab — shown so the list isn't lying by omission.
//
// Secrets (env / header VALUES) never come back from the agent: they arrive as "***"
// and are sent back unchanged to keep the stored value (mcpreg.MergeSecrets). So the
// form can be opened, edited and saved without ever handling the real credential.
//
// Everything here lives in the workspace's encrypted store, so the tab is gated on a
// running workspace — a stopped one would otherwise render an empty registry as if the
// user had registered nothing.

// --- tab -------------------------------------------------------------------------

export function McpTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const toast = useToast();

  const [reg, setReg] = useState<Registry | null>(null);
  const [form, setForm] = useState<Form | null>(null);
  // Probe results keyed by server id ("" = the unsaved form), so a test result stays
  // pinned to the row that produced it across a reload.
  const [probes, setProbes] = useState<Record<string, ProbeResult>>({});
  const [refreshing, setRefreshing] = useState(false);
  // Which tenant user_secret row is having its values entered ("" = none).
  const [secretFor, setSecretFor] = useState<McpServer | null>(null);
  // Every remote destination on screen, including the one being typed into the open form.
  // Declared here (not after the loading guards) because it feeds a hook.
  const { check: egress, recheck: recheckEgress } = useEgressCheck(
    hostsOf([...(reg?.servers || []).map((s) => s.url), form?.url]),
  );

  // A CP 502 while the agent is still booting must not render as "no servers
  // registered" — retry, and never downgrade a snapshot we already have
  // (the useConnections / ws-boot-view lesson).
  const reload = useCallback(() => {
    const attempt = (left: number) => {
      api("api/mcp-servers")
        .then((d) => {
          if (d && !d.error) {
            setReg({ ...d, servers: Array.isArray(d.servers) ? d.servers : [] });
            return;
          }
          if (left > 0 && isTransientErr(d)) setTimeout(() => attempt(left - 1), 1200);
          else setReg((prev) => prev ?? { servers: [] });
        })
        .catch(() => {
          if (left > 0) setTimeout(() => attempt(left - 1), 1200);
          else setReg((prev) => prev ?? { servers: [] });
        });
    };
    attempt(2);
  }, []);
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  const save = async (f: Form) => {
    const path = f.id ? `api/mcp-servers/${encodeURIComponent(f.id)}` : "api/mcp-servers";
    const res = await apiJSON(path, f.id ? "PUT" : "POST", bodyOf(f));
    if (res && res.error) {
      toast(tr("mcp.save_failed", { msg: errText(res.error) }));
      return false;
    }
    setForm(null);
    reload();
    return true;
  };

  const test = async (f: Form): Promise<void> => {
    const key = f.id;
    setProbes((p) => {
      const { [key]: _drop, ...rest } = p;
      return rest;
    });
    const res = await apiJSON("api/mcp-servers/test", "POST", bodyOf(f));
    if (res && res.error) {
      setProbes((p) => ({ ...p, [key]: { ok: false, toolCount: 0, elapsedMs: 0, error: errText(res.error) } }));
      return;
    }
    setProbes((p) => ({ ...p, [key]: res as ProbeResult }));
  };

  const setEnabled = async (s: McpServer, on: boolean) => {
    const res = await rawJSON(`api/mcp-servers/${encodeURIComponent(s.id)}/enabled`, "POST", { enabled: on });
    if (!res.ok) {
      const j = await res.json().catch(() => null);
      toast(tr("mcp.save_failed", { msg: errText(j?.error) || String(res.status) }));
      return;
    }
    reload();
  };

  const remove = async (s: McpServer) => {
    const r = await apiJSON(`api/mcp-servers/${encodeURIComponent(s.id)}`, "DELETE");
    if (r && r.error) {
      toast(tr("mcp.save_failed", { msg: errText(r.error) }));
      return;
    }
    if (form?.id === s.id) setForm(null);
    reload();
  };

  // Pull the tenant-distributed set now instead of waiting for the agent's 5-minute
  // poll (docs/log/48 §6). A failure is surfaced verbatim — a silently stale list is the
  // thing this button exists to rule out.
  const refreshTenant = async () => {
    setRefreshing(true);
    try {
      const d = await apiJSON("api/mcp-servers/tenant-refresh", "POST", {});
      if (d && d.error) {
        toast(tr("mcp.tenant_refresh_failed", { msg: errText(d.error) }));
        return;
      }
      setReg({ ...d, servers: Array.isArray(d.servers) ? d.servers : [] });
      // Rows the CP could not decrypt, and rows this agent refused, are both absent from
      // the list. Saying so beats letting the member conclude the admin never added them.
      const unreadable = Number(d.fetch?.unreadable) || 0;
      const dropped = Number(d.fetch?.dropped) || 0;
      if (unreadable + dropped > 0) toast(tr("mcp.tenant_incomplete", { n: unreadable + dropped }));
    } finally {
      setRefreshing(false);
    }
  };

  // Store the member's own header values for a tenant user_secret definition. Lands in
  // this workspace's encrypted store; the distributed definition is never modified.
  const saveSecrets = async (s: McpServer, headers: Record<string, string>) => {
    const d = await apiJSON(`api/mcp-servers/${encodeURIComponent(s.id)}/secrets`, "PUT", { headers });
    if (d && d.error) {
      toast(tr("mcp.save_failed", { msg: errText(d.error) }));
      return false;
    }
    setSecretFor(null);
    reload();
    return true;
  };

  if (!running) {
    return (
      <EmptyState icon="debug-disconnect" title={tr("mcp.ws_required_title")} hint={tr("mcp.ws_required_hint")}>
        <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
          {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
        </Button>
      </EmptyState>
    );
  }
  if (!reg) return <p className="muted pad">{tr("common.loading")}</p>;

  const shadowed = reg.shadowed || [];
  const hasTenant = reg.servers.some((s) => s.origin === "tenant") || !!reg.tenantFetchedAt;
  return (
    <div className="mcp-tab">
      <Hint>{tr("mcp.intro")}</Hint>
      {/* プロジェクトスコープ（リポジトリの .mcp.json 等）は別軸（docs/log/56 P0）— この
          タブは実効レジストリ（user/tenant/builtin）だけを扱う。行き止まりにしない
          よう導線だけ 1 行置く（docs/log/57 §3）。 */}
      <p className="ps-note">{tr("mcp.project_scope_note")}</p>
      {shadowed.length > 0 && (
        <p className="ps-note ps-note-warn">{tr("mcp.shadowed", { names: shadowed.join(", ") })}</p>
      )}
      {/* Tenant distribution status. Shown only once a set has actually been fetched:
          on a deployment without the bridge (no CP public URL) there is nothing to say,
          and an unexplained "last fetched: never" reads as a fault. */}
      {hasTenant && (
        <div className="mcp-tenant-bar">
          <span className="muted">
            {reg.tenantFetchedAt
              ? tr("mcp.tenant_fetched_at", { when: fmtDateTime(reg.tenantFetchedAt * 1000, DATETIME_FULL) })
              : tr("mcp.tenant_never_fetched")}
          </span>
          <button className="ghost mcp-btn" disabled={refreshing} onClick={() => void refreshTenant()}>
            <Icon name="refresh" /> {refreshing ? tr("mcp.tenant_refreshing") : tr("mcp.tenant_refresh")}
          </button>
        </div>
      )}
      {reg.servers.length === 0 ? (
        <p className="muted mcp-empty">{tr("mcp.empty")}</p>
      ) : (
        <ul className="ssm-list mcp-list">
          {reg.servers.map((s) =>
            form && form.id === s.id ? (
              <li key={s.id} className="ssm-item">
                <ServerForm
                  form={form}
                  setForm={setForm}
                  onSave={save}
                  onTest={test}
                  probe={probes[s.id]}
                  egress={egress}
                  onProposed={recheckEgress}
                  submitLabel={tr("common.save")}
                />
              </li>
            ) : secretFor && secretFor.id === s.id ? (
              <li key={s.id} className="ssm-item">
                <SecretsForm s={s} onSave={saveSecrets} onCancel={() => setSecretFor(null)} />
              </li>
            ) : (
              <ServerRow
                key={s.id}
                s={s}
                probe={probes[s.id]}
                egress={egress}
                onProposed={recheckEgress}
                onEdit={() => setForm(formOf(s))}
                onTest={() => void test(formOf(s))}
                onToggle={(on) => void setEnabled(s, on)}
                onDelete={() => void remove(s)}
                onEnterSecrets={() => setSecretFor(s)}
              />
            ),
          )}
        </ul>
      )}
      {form && form.id === "" ? (
        <div className="mcp-newform">
          <ServerForm
            form={form}
            setForm={setForm}
            onSave={save}
            onTest={test}
            probe={probes[""]}
            egress={egress}
            onProposed={recheckEgress}
            submitLabel={tr("mcp.add")}
          />
        </div>
      ) : (
        !form && (
          <button className="ghost ssm-add-toggle" onClick={() => setForm(emptyForm())}>
            <Icon name="add" /> {tr("mcp.add")}
          </button>
        )
      )}
    </div>
  );
}

// --- list row --------------------------------------------------------------------
