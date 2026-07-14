import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, raw } from "../../core/api/client.ts";
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

// CloudWatchCard: point the CloudWatch MCP at an AWS SSO profile. The picker
// lists the user's SSM connection profiles (GET /api/ssm/profiles) and sends
// their non-secret SSO meta along — SSM profiles live in per-session isolated
// aws configs, so the agent generates a durable ops config from the meta
// (~/.aws/af-ops/cloudwatch.config) instead of relying on ~/.aws/config. A
// manual entry remains for members who maintain their own ~/.aws. No secret is
// stored either way; an expired SSO session just makes the tools error.
function CloudWatchCard({ st, reload }: { st: any; reload: () => void }) {
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
        toast("接続に失敗: " + (res.error.message || res.error));
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.profile}</span>
          {st.region && <span className="p-pl">{st.region}</span>}
          {st.sso && <span className="p-pl">SSO</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : profiles === null ? (
        <p className="muted pad">読み込み中…</p>
      ) : (
        <div className="p-body">
          <div className="flow">
            {profiles.length > 0 && (
              <select className="cinput" value={sel} onChange={(e) => pick(e.target.value)}>
                <option value="">プロファイルを選択…</option>
                {profiles.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                    {p.accountId ? `（${p.accountId}${p.roleName ? " / " + p.roleName : ""}）` : ""}
                  </option>
                ))}
                <option value="manual">手動入力（自分の ~/.aws のプロファイル）</option>
              </select>
            )}
            {manual && (
              <input
                className="cinput"
                type="text"
                placeholder="~/.aws のプロファイル名"
                value={manualProfile}
                onChange={(e) => setManualProfile(e.target.value)}
              />
            )}
            <input
              className="cinput"
              type="text"
              placeholder="リージョン（任意）"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              style={{ maxWidth: "12em" }}
            />
            <button disabled={busy || !ok} onClick={save}>
              接続
            </button>
          </div>
          <Hint>
            秘密は保存しません。SSM 接続のプロファイルを選ぶと、その SSO
            設定（非秘密）から専用の設定ファイルを生成して使います。ログの検索・アラーム履歴・メトリクス分析など読み取り専用ツールのみです。SSO
            ログインがまだ（または期限切れ）の場合は、該当の SSM セッションを一度開くか、ターミナルで
            `AWS_CONFIG_FILE=~/.aws/af-ops/cloudwatch.config aws sso login --profile
            プロファイル名` を実行してください。
          </Hint>
        </div>
      )}
    </ProviderCard>
  );
}
