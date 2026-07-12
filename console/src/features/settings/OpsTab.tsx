import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, raw } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useConnections } from "./useConnections.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "./providerCard.tsx";

// OpsTab is the home for service-operations connections (docs/25 Phase 1): external
// monitoring / incident tools the SRE assistant talks to over MCP. Today: PagerDuty.
// The API key is stored container-side (encrypted secrets) and injected into the
// PagerDuty MCP at spawn by `workspace-agent mcp-run` — it never reaches the CP.
// Grafana / CloudWatch land here later as sibling cards.
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
