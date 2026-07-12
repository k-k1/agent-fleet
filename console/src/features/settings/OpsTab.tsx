import { useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, raw } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useConnections } from "./useConnections.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "./providerCard.tsx";

// OpsTab is the home for service-operations connections (docs/25 Phase 1): external
// monitoring / incident tools the SRE assistant talks to over MCP. Today: PagerDuty
// and Grafana. Credentials are stored container-side (encrypted secrets) and injected
// into the MCP server at spawn by `workspace-agent mcp-run` — they never reach the CP.
// CloudWatch lands here later as a sibling card.
export function OpsTab() {
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const { conns, reload } = useConnections();

  if (!running) {
    return (
      <div className="conns">
        <p className="muted pad">
          運用ツールの接続はワークスペース内に保存されるため、ワークスペースの起動が必要です。
        </p>
        <button onClick={() => startWs()}>ワークスペースを起動</button>
      </div>
    );
  }
  if (!conns) return <p className="muted pad">読み込み中…</p>;

  return (
    <div className="conns">
      <p className="muted ds-note">
        インシデント対応・監視運用の連携です。接続すると「SRE アシスタント」がこれらを読み取り専用で参照して壁打ちに使います。接続の変更は次のチャット送信から反映されます（ワークスペースの再起動は不要）。
      </p>
      <PagerDutyCard st={conns.pagerduty} reload={reload} />
      <GrafanaCard st={conns.grafana} reload={reload} />
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
              autoFocus
            />
            <button disabled={busy || !key.trim()} onClick={save}>
              接続
            </button>
          </div>
          <label className="p-check">
            <input type="checkbox" checked={eu} onChange={(e) => setEu(e.target.checked)} />{" "}
            EU アカウント（api.eu.pagerduty.com）
          </label>
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
