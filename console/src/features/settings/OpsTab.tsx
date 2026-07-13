import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, raw } from "../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConnections } from "./useConnections.ts";
import { OnOff } from "./controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "./providerCard.tsx";

// OpsTab is the home for service-operations connections (docs/25 Phase 1): external
// monitoring / incident tools the SRE assistant talks to over MCP. Today: PagerDuty,
// Grafana and CloudWatch. Credentials are stored container-side (encrypted secrets)
// and injected into the MCP server at spawn by `workspace-agent mcp-run` — they never
// reach the CP. (CloudWatch stores no secret at all: it rides the AWS cred chain.)
export function OpsTab() {
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
          title="運用ツールの接続はワークスペース内で実行されます"
          hint="API キーはコンテナ内の Agent が暗号化保存するため、ワークスペースの起動が必要です。"
        >
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? "起動中…" : "ワークスペースを起動"}
          </Button>
        </EmptyState>
      ) : !conns ? (
        <p className="muted pad">読み込み中…</p>
      ) : (
        <>
          <p className="muted ds-note">
            インシデント対応・監視運用の連携です。接続すると「SRE
            アシスタント」がこれらを読み取り専用で参照して壁打ちに使います。接続の変更は次のチャット送信から反映されます（ワークスペースの再起動は不要）。
          </p>
          <div className="conn-cat">インシデント管理</div>
          <PagerDutyCard st={conns.pagerduty} reload={reload} />
          <div className="conn-cat">監視 / メトリクス</div>
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
        toast("接続に失敗: " + (res.error.message || res.error));
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">API キー設定済み</span>
          {st.host && <span className="p-pl">EU</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder="PagerDuty API キー"
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
            <button disabled={busy || !key.trim()} onClick={save}>
              接続
            </button>
          </div>
          <div className="ps-row">
            <span className="ps-label">
              EU リージョン
              <span className="sub">
                PagerDuty に app.eu.pagerduty.com でログインしている場合のみオン（通常はオフのまま）
              </span>
            </span>
            <OnOff value={eu} onChange={setEu} />
          </div>
          <Hint>
            読み取り専用キーを推奨します（PagerDuty の Integrations &gt; API Access Keys で「Read-only」を選択）。
            キーはワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます。書き込み操作（ack/resolve
            など）は有効化しません。
          </Hint>
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
        toast("接続に失敗: " + (res.error.message || res.error));
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.url || "接続設定済み"}</span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="text"
              placeholder="Grafana URL（https://grafana.example.com）"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
            />
          </div>
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder="サービスアカウントトークン"
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button disabled={busy || !ok} onClick={save}>
              接続
            </button>
          </div>
          <Hint>
            Viewer 権限のサービスアカウントトークンを推奨します。トークンはワークスペース内に暗号化保存され、MCP
            サーバの起動時にだけ渡されます（書き込み・管理ツールは無効で起動）。Amazon Managed Grafana の場合は
            URL に workspace endpoint（g-xxxx.grafana-workspace.リージョン.amazonaws.com）を指定してください
            （トークンは最長30日で失効するため、失効したら貼り直します）。
          </Hint>
        </div>
      )}
    </ProviderCard>
  );
}

// CloudWatchCard: point the CloudWatch MCP at an AWS profile (+ optional region).
// No secret is stored — auth is the AWS credential chain (the user's `aws sso
// login`, same as ssm sessions), so an expired SSO session just makes the tools
// error until the user logs in again.
function CloudWatchCard({ st, reload }: { st: any; reload: () => void }) {
  const toast = useToast();
  const [profile, setProfile] = useState("");
  const [region, setRegion] = useState("");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!profile.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/cloudwatch", "PUT", {
        profile: profile.trim(),
        region: region.trim(),
      });
      if (res && res.error) {
        toast("接続に失敗: " + (res.error.message || res.error));
        return;
      }
      setProfile("");
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.profile}</span>
          {st.region && <span className="p-pl">{st.region}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="text"
              placeholder="AWS プロファイル名（SSM 接続と同じ SSO プロファイル）"
              value={profile}
              onChange={(e) => setProfile(e.target.value)}
            />
            <input
              className="cinput"
              type="text"
              placeholder="リージョン（任意）"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              style={{ maxWidth: "12em" }}
            />
            <button disabled={busy || !profile.trim()} onClick={save}>
              接続
            </button>
          </div>
          <Hint>
            秘密は保存しません。ワークスペース内の AWS 資格（`aws sso
            login`済みのプロファイル）をそのまま読みます。ログの検索・アラーム履歴・メトリクス分析など読み取り専用ツールのみです。SSO
            セッションが切れているとツールがエラーになるので、その場合はセッションで `aws sso login` してください。
          </Hint>
        </div>
      )}
    </ProviderCard>
  );
}
