import { useEffect, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { api, apiJSON, raw } from "../../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useConnections } from "../parts/useConnections.ts";
import { useSettingsUI } from "../store.ts";
import { OnOff } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "../parts/providerCard.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// OpsTab is the home for service-operations connections (docs/log/25 Phase 1): external
// monitoring / incident tools the SRE assistant talks to over MCP, plus the AWS MCP
// Server (Agent Toolkit for AWS), which also attaches to interactive sessions. Today:
// PagerDuty, Grafana, CloudWatch and AWS. Credentials are stored container-side
// (encrypted secrets) and injected into the MCP server at spawn by `workspace-agent
// mcp-run` — they never reach the CP. (The two AWS cards store no secret at all: they
// ride the AWS cred chain.)
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
          <div className="conn-cat">{tr("ops.cat_cloud")}</div>
          <AWSCard st={conns.aws} reload={reload} />
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

// useAWSProfile is the shared half of the two AWS-backed cards (CloudWatch / AWS
// MCP): both point an MCP server at an AWS profile and store no secret. The picker
// lists the user's SSM connection profiles (GET /api/ssm/profiles) and sends their
// non-secret SSO meta along — SSM profiles live in per-session isolated aws configs,
// so the agent generates a durable ops config from the meta (~/.aws/af-ops/<id>.config)
// instead of relying on ~/.aws/config. A manual entry remains for members who
// maintain their own ~/.aws. An expired SSO session just makes the tools error.
function useAWSProfile(connected: boolean) {
  const [profiles, setProfiles] = useState<any[] | null>(null);
  const [sel, setSel] = useState(""); // profile id, or "manual"
  const [manualProfile, setManualProfile] = useState("");
  const [region, setRegion] = useState("");

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
  // body() is the request half both cards PUT; each adds its own extra fields.
  const body = () =>
    manual
      ? { profile: manualProfile.trim(), region: region.trim() }
      : {
          profile: picked.label,
          region: region.trim(),
          startUrl: picked.startUrl || "",
          ssoRegion: picked.ssoRegion || "",
          accountId: picked.accountId || "",
          roleName: picked.roleName || "",
        };
  const reset = () => {
    setSel("");
    setManualProfile("");
    setRegion("");
  };
  return { profiles, sel, manual, ok, pick, manualProfile, setManualProfile, region, setRegion, body, reset };
}

// AWSProfileFields renders that picker: the SSM profile select (or a manual profile
// name when there are none), plus the resource region.
function AWSProfileFields({ p }: { p: ReturnType<typeof useAWSProfile> }) {
  const tr = useT();
  return (
    <>
      {/* Surface the silent SSM dependency: with no profiles we fall back to manual
          entry — point the user at the AWS SSM tab where profiles are defined. */}
      {p.profiles!.length === 0 && (
        <p className="ps-note">
          {tr("ops.cw_no_profiles")}{" "}
          <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
            {tr("ops.cw_open_ssm")}
          </button>
        </p>
      )}
      {p.profiles!.length > 0 && (
        <select className="cinput" value={p.sel} onChange={(e) => p.pick(e.target.value)}>
          <option value="">{tr("ops.cw_profile_select")}</option>
          {p.profiles!.map((x) => (
            <option key={x.id} value={x.id}>
              {x.label}
              {x.accountId ? `（${x.accountId}${x.roleName ? " / " + x.roleName : ""}）` : ""}
            </option>
          ))}
          <option value="manual">{tr("ops.cw_manual_option")}</option>
        </select>
      )}
      {p.manual && (
        <input
          className="cinput"
          type="text"
          placeholder={tr("ops.cw_manual_placeholder")}
          value={p.manualProfile}
          onChange={(e) => p.setManualProfile(e.target.value)}
        />
      )}
      <input
        className="cinput"
        type="text"
        placeholder={tr("ops.cw_region_placeholder")}
        value={p.region}
        onChange={(e) => p.setRegion(e.target.value)}
        style={{ maxWidth: "12em" }}
      />
    </>
  );
}

// CloudWatchCard: point the CloudWatch MCP at an AWS profile (see useAWSProfile).
function CloudWatchCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const p = useAWSProfile(!!st?.connected);

  const save = async () => {
    if (!p.ok) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/cloudwatch", "PUT", p.body());
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      p.reset();
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
      ) : p.profiles === null ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <div className="p-body">
          <div className="flow">
            <AWSProfileFields p={p} />
            <button disabled={busy || !p.ok} onClick={save}>
              {tr("conn.connect")}
            </button>
          </div>
          <Hint>{tr("ops.cw_hint")}</Hint>
        </div>
      )}
    </ProviderCard>
  );
}

// The regions AWS publishes the MCP Server in. This is the region the SERVICE runs
// in (and what SigV4 signs against) — not where the member's resources live, which
// is the region field of the profile picker above.
const AWS_MCP_ENDPOINTS = ["us-east-1", "eu-central-1"];

// AWSCard: Agent Toolkit for AWS — the AWS-operated MCP Server, reached through the
// official SigV4 stdio proxy (`workspace-agent mcp-run aws`). Same profile story as
// CloudWatch, plus two things this card owns:
//   - the endpoint region (where the MCP service runs, ≠ the resource region);
//   - 書き込みツール, off by default. On, the agent can call ~15,000 AWS API actions
//     and run scripts, so it is an explicit choice made here rather than a default
//     that arrives with the connection.
// Unlike the other ops integrations this one also attaches to interactive sessions,
// not just the assistant chat — its tools are for building on AWS.
function AWSCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [endpoint, setEndpoint] = useState(AWS_MCP_ENDPOINTS[0]);
  const [write, setWrite] = useState(false);
  const p = useAWSProfile(!!st?.connected);

  const save = async () => {
    if (!p.ok) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/aws", "PUT", { ...p.body(), endpoint, write });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      p.reset();
      setWrite(false);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/aws", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="aws"
      name="Agent Toolkit for AWS"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.profile}</span>
          {st.region && <span className="p-pl">{st.region}</span>}
          {st.sso && <span className="p-pl">SSO</span>}
          <span className="p-pl">{st.endpoint}</span>
          <span className="p-pl">{st.write ? tr("ops.aws_mode_write") : tr("ops.aws_mode_read")}</span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : p.profiles === null ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <div className="p-body">
          <div className="flow">
            <AWSProfileFields p={p} />
            <select
              className="cinput"
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value)}
              style={{ maxWidth: "12em" }}
            >
              {AWS_MCP_ENDPOINTS.map((r) => (
                <option key={r} value={r}>
                  {tr("ops.aws_endpoint_option", { region: r })}
                </option>
              ))}
            </select>
            <button disabled={busy || !p.ok} onClick={save}>
              {tr("conn.connect")}
            </button>
          </div>
          <div className="ps-row">
            <span className="ps-label">
              {tr("ops.aws_write")}
              <span className="sub">{tr("ops.aws_write_sub")}</span>
            </span>
            <OnOff value={write} onChange={setWrite} />
          </div>
          <Hint>{tr("ops.aws_hint")}</Hint>
        </div>
      )}
    </ProviderCard>
  );
}
