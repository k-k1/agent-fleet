// TrackerTab — 課題管理: the connections that feed the work item rail (docs/80).
//
// ★ Its own tab rather than a category inside 運用・監視. Those cards are monitoring /
// incident integrations the SRE assistant reads over MCP; this one feeds a rail the
// user looks at, and nothing else. Sitting under that tab's opening sentence ("接続すると
// 「SRE アシスタント」が…") it read as something it is not, and the disclaimer line the
// category needed was the tell.
//
// GitHub is deliberately absent: the rail reuses the Git connection (設定 > Git), because
// the token that clones is the token that lists issues. Only Jira needs its own.
import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, raw } from "../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConnections } from "./useConnections.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "./providerCard.tsx";
import { useT } from "../../lib/i18n/index.ts";

export function TrackerTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const { conns, reload } = useConnections();

  // 稼働してから（かつ停止→稼働の遷移でも）読み直す。資格情報はコンテナの中にあるので、
  // 止まっている間はカードの状態が分からない —— AgentsTab / OpsTab と同じ作法。
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  return (
    <div className="conns">
      {!running ? (
        <EmptyState icon="debug-disconnect" title={tr("ops.ws_required_title")} hint={tr("ops.ws_required_hint")}>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </EmptyState>
      ) : !conns ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <>
          <p className="muted ds-note">{tr("tracker.intro")}</p>
          <JiraCard st={conns.jira} reload={reload} />
        </>
      )}
    </div>
  );
}

// JiraCard (docs/80 P1). Three fields, because Jira's REST v3 is HTTP Basic over
// "<account email>:<API token>" — so the email is a credential too, and both stay
// container-side like every other connection.
//
// ★ The credentials are verified against /rest/api/3/myself before they are stored, and
// the card then names the account. Three fields are three chances to typo, and without
// the check the first sign of a bad paste would be an error on a rail row minutes later.
//
// ⚠️ A Jira MCP server does NOT remove the need for this: MCP only runs inside a
// conversation, so it cannot produce the rail's list. The two are complements — this
// feeds the list, the MCP reads the ticket body in-session.
function JiraCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [site, setSite] = useState("");
  const [email, setEmail] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const ready = !!site.trim() && !!email.trim() && !!token.trim();

  const save = async () => {
    if (!ready) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/jira", "PUT", {
        site: site.trim(),
        email: email.trim(),
        token: token.trim(),
      });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      setSite("");
      setEmail("");
      setToken("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/jira", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="jira"
      name="Jira"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em">{st.account || st.email}</span>
          {st.site && <span className="p-pl">{String(st.site).replace(/^https:\/\//, "")}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="url"
              placeholder={tr("ops.jira_site_placeholder")}
              value={site}
              onChange={(e) => setSite(e.target.value)}
            />
          </div>
          <div className="flow">
            <input
              className="cinput"
              type="email"
              placeholder={tr("ops.jira_email_placeholder")}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("ops.jira_token_placeholder")}
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button disabled={busy || !ready} onClick={save}>
              {busy ? tr("admin.checking") : tr("conn.connect")}
            </button>
          </div>
          <Hint>{tr("ops.jira_hint")}</Hint>
        </div>
      )}
    </ProviderCard>
  );
}
