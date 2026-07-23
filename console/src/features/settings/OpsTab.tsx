import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConnections } from "./useConnections.ts";
import { useSettingsUI } from "./store.ts";
import { OnOff } from "./controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "./providerCard.tsx";
import { useT } from "../../lib/i18n/index.ts";

// OpsTab is the home for service-operations connections (docs/25 Phase 1): external
// monitoring / incident tools the SRE assistant talks to over MCP. Today: PagerDuty,
// Grafana and CloudWatch. Credentials are stored container-side (encrypted secrets)
// and injected into the MCP server at spawn by `workspace-agent mcp-run` — they never
// reach the CP. (CloudWatch stores no secret at all: it rides the AWS cred chain.)
// The chat-bridge connections (Discord / Slack) moved to their own チャット連携 tab
// (ChatTab) — they're notification destinations, not monitoring providers.
export function OpsTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const { conns, reload } = useConnections();

  // (Re)load when the workspace is running — including a stopped→running
  // transition while the dialog is open (same pattern as AgentsTab). Without
  // this initial kick, useConnections stays null (読み込み中) forever.
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  return (
    <div className="conns">
      {!running ? (
        <EmptyState
          icon="debug-disconnect"
          title={tr("ops.ws_required_title")}
          hint={tr("ops.ws_required_hint")}
        >
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </EmptyState>
      ) : !conns ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <>
          <p className="muted ds-note">{tr("ops.intro")}</p>
          <div className="conn-cat">{tr("ops.cat_incident")}</div>
          <PagerDutyCard st={conns.pagerduty} reload={reload} />
          <div className="conn-cat">{tr("ops.cat_monitoring")}</div>
          <GrafanaCard st={conns.grafana} reload={reload} />
          <CloudWatchCard st={conns.cloudwatch} reload={reload} />
        </>
      )}
    </div>
  );
}

// PagerDutyCard: paste a PagerDuty API key (read-only key recommended). Optional EU
// host override. On connect the key is stored encrypted; the SRE assistant then gets
// PagerDuty tools on its next message.
function PagerDutyCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [key, setKey] = useState("");
  const [eu, setEu] = useState(false);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!key.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/pagerduty", "PUT", {
        apiKey: key.trim(),
        host: eu ? "https://api.eu.pagerduty.com" : "",
      });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      setKey("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/pagerduty", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="pagerduty"
      name="PagerDuty"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{tr("ops.pd_api_key_set")}</span>
          {st.host && <span className="p-pl">EU</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("ops.pd_api_key_placeholder")}
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
            <button disabled={busy || !key.trim()} onClick={save}>
              {tr("conn.connect")}
            </button>
          </div>
          <div className="ps-row">
            <span className="ps-label">
              {tr("ops.pd_eu_region")}
              <span className="sub">{tr("ops.pd_eu_sub")}</span>
            </span>
            <OnOff value={eu} onChange={setEu} />
          </div>
          <Hint>{tr("ops.pd_hint")}</Hint>
        </div>
      )}
    </ProviderCard>
  );
}

// GrafanaCard: paste a Grafana instance URL + service-account token (Viewer role
// recommended). Works for self-hosted / Grafana Cloud / Amazon Managed Grafana —
// for AMG the URL is the workspace endpoint (https://g-xxxx.grafana-workspace.
// <region>.amazonaws.com) and tokens expire after at most 30 days (re-paste here).
function GrafanaCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const ok = url.trim() !== "" && token.trim() !== "";

  const save = async () => {
    if (!ok) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/grafana", "PUT", {
        url: url.trim(),
        token: token.trim(),
      });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      setUrl("");
      setToken("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/grafana", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="grafana"
      name="Grafana"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.url || tr("ops.grafana_connected_fallback")}</span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="text"
              placeholder={tr("ops.grafana_url_placeholder")}
              value={url}
              onChange={(e) => setUrl(e.target.value)}
            />
          </div>
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("ops.grafana_token_placeholder")}
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button disabled={busy || !ok} onClick={save}>
              {tr("conn.connect")}
            </button>
          </div>
          <Hint>{tr("ops.grafana_hint")}</Hint>
        </div>
      )}
    </ProviderCard>
  );
}

// CloudWatchCard: point the CloudWatch MCP at an AWS SSO profile. The picker
// lists the user's SSM connection profiles (GET /api/ssm/profiles) and sends
// their non-secret SSO meta along — SSM profiles live in per-session isolated
// aws configs, so the agent generates a durable ops config from the meta
// (~/.aws/af-ops/cloudwatch.config) instead of relying on ~/.aws/config. A
// manual entry remains for members who maintain their own ~/.aws. No secret is
// stored either way; an expired SSO session just makes the tools error.
function CloudWatchCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [profiles, setProfiles] = useState<any[] | null>(null);
  const [sel, setSel] = useState(""); // profile id, or "manual"
  const [manualProfile, setManualProfile] = useState("");
  const [region, setRegion] = useState("");
  const [busy, setBusy] = useState(false);

  const connected = !!st?.connected;
  useEffect(() => {
    if (connected) return;
    api("api/ssm/profiles")
      .then((d) => setProfiles(Array.isArray(d) ? d : []))
      .catch(() => setProfiles([]));
  }, [connected]);

  const picked = sel && sel !== "manual" ? profiles?.find((p) => p.id === sel) : null;
  const manual = sel === "manual" || (profiles !== null && profiles.length === 0);
  const ok = manual ? manualProfile.trim() !== "" : !!picked;

  const pick = (id: string) => {
    setSel(id);
    const p = id !== "manual" ? profiles?.find((x) => x.id === id) : null;
    setRegion(p?.region || "");
  };

  const save = async () => {
    if (!ok) return;
    setBusy(true);
    try {
      const body = manual
        ? { profile: manualProfile.trim(), region: region.trim() }
        : {
            profile: picked.label,
            region: region.trim(),
            startUrl: picked.startUrl || "",
            ssoRegion: picked.ssoRegion || "",
            accountId: picked.accountId || "",
            roleName: picked.roleName || "",
          };
      const res = await apiJSON("api/connections/cloudwatch", "PUT", body);
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      setSel("");
      setManualProfile("");
      setRegion("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/cloudwatch", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="cloudwatch"
      name="Amazon CloudWatch"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.profile}</span>
          {st.region && <span className="p-pl">{st.region}</span>}
          {st.sso && <span className="p-pl">SSO</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : profiles === null ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <div className="p-body">
          {/* Surface the silent SSM dependency: with no profiles we fall back to manual
              entry — point the user at the AWS SSM tab where profiles are defined. */}
          {profiles.length === 0 && (
            <p className="ps-note">
              {tr("ops.cw_no_profiles")}{" "}
              <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
                {tr("ops.cw_open_ssm")}
              </button>
            </p>
          )}
          <div className="flow">
            {profiles.length > 0 && (
              <select className="cinput" value={sel} onChange={(e) => pick(e.target.value)}>
                <option value="">{tr("ops.cw_profile_select")}</option>
                {profiles.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                    {p.accountId ? `（${p.accountId}${p.roleName ? " / " + p.roleName : ""}）` : ""}
                  </option>
                ))}
                <option value="manual">{tr("ops.cw_manual_option")}</option>
              </select>
            )}
            {manual && (
              <input
                className="cinput"
                type="text"
                placeholder={tr("ops.cw_manual_placeholder")}
                value={manualProfile}
                onChange={(e) => setManualProfile(e.target.value)}
              />
            )}
            <input
              className="cinput"
              type="text"
              placeholder={tr("ops.cw_region_placeholder")}
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              style={{ maxWidth: "12em" }}
            />
            <button disabled={busy || !ok} onClick={save}>
              {tr("conn.connect")}
            </button>
          </div>
          <Hint>{tr("ops.cw_hint")}</Hint>
        </div>
      )}
    </ProviderCard>
  );
}
