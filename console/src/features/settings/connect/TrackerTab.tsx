// TrackerTab — issue tracking: the connections that feed the work item rail (docs/log/80).
// The tab name and the left-pane rail name (wi.title) are the same word (「課題管理」). Only
// the surfaces a user sees were aligned; code and docs keep "work item" as the internal name,
// because renaming type names and APIs would cost more than it is worth.
//
// Its own tab rather than a category inside ops monitoring. Those cards are monitoring /
// incident integrations the SRE assistant reads over MCP; this one feeds a rail the
// user looks at, and nothing else. Sitting under that tab's opening sentence ("connect this
// and the SRE assistant …") it read as something it is not, and the disclaimer line the
// category needed was the tell.
//
// GitHub is deliberately absent: the rail reuses the Git connection (Settings > Git), because
// the token that clones is the token that lists issues. Only Jira needs its own.
import { useEffect, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { api, apiJSON, raw } from "../../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useConnections } from "../parts/useConnections.ts";
import { usePolling } from "../parts/usePolling.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "../parts/providerCard.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export function TrackerTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const { conns, reload } = useConnections();
  // Whether the "connect with OAuth" button may be offered at all. It must never be a press
  // that then returns not_configured: registering the app is the tenant admin's job and the
  // person who pressed cannot fix it (docs/log/71 §71.4). The answer is in the CP database,
  // so it can be read while the workspace is stopped.
  const [oauthOk, setOauthOk] = useState<boolean | null>(null);
  useEffect(() => {
    api("api/git-oauth")
      .then((d) => {
        if (d && !d.error) setOauthOk(d.jira?.configured !== false);
      })
      .catch(() => {});
  }, []);

  // Re-read once running, including on a stopped -> running transition: the credentials live
  // inside the container, so while it is stopped the card's state is unknown. Same as
  // AgentsTab and OpsTab.
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
          <JiraCard st={conns.jira} reload={reload} oauthAvailable={oauthOk !== false} />
        </>
      )}
    </div>
  );
}

// JiraCard (docs/log/80 P1 / §80.17). Two ways in:
//
//   - OAuth (Atlassian 3LO) — one button, scoped access, nothing to paste. Needs the
//     tenant administrator to have registered an app, which is why the button is gated
//     on /api/git-oauth rather than discovered by pressing it.
//   - An API token — three fields, because Jira's REST v3 is HTTP Basic over
//     "<account email>:<API token>", so the email is a credential too.
//
// The token path is kept, not replaced: a tenant with no registered app has no other
// way in, and that is the state every deployment starts in.
//
// Either way the credentials stay container-side; the CP only ever passes them through.
function JiraCard({ st, reload, oauthAvailable }: { st: any; reload: () => void; oauthAvailable: boolean }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState<"idle" | "waiting" | "token">("idle");
  const [status, setStatus] = useState("");
  const [site, setSite] = useState("");
  const [email, setEmail] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const ready = !!site.trim() && !!email.trim() && !!token.trim();

  const startOAuth = async () => {
    const res = await apiJSON("api/connections/jira/oauth/start", "POST");
    if (!res || res.error || !res.authorize_url) {
      if (res?.error?.code === "not_configured") toast(tr("tracker.jira_oauth_unconfigured"), { kind: "warn" });
      else toast(tr("git.oauth_start_failed", { msg: res?.error?.message || "" }));
      return;
    }
    window.open(res.authorize_url, "_blank", "noopener");
    setMode("waiting");
    setStatus(tr("tracker.jira_waiting"));
    // Authorization happens in another browser tab and the save happens CP -> Agent, so all
    // this side can do is poll for the connection turning connected (as Bitbucket does).
    poll({
      deadlineMs: 5 * 60 * 1000,
      firstDelayMs: 2500,
      onExpire: () => setStatus(tr("tracker.jira_timeout")),
      step: async () => {
        let d;
        try {
          d = await api("api/connections");
        } catch {
          d = null;
        }
        if (d && d.jira && d.jira.connected) {
          setMode("idle");
          reload();
          return { stop: true };
        }
        return { stop: false, nextMs: 2000 };
      },
    });
  };

  const saveToken = async () => {
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
      setMode("idle");
      reload();
    } finally {
      setBusy(false);
    }
  };

  const pickSite = async (cloudId: string) => {
    const res = await apiJSON("api/connections/jira/site", "PUT", { cloudId });
    if (res && res.error) {
      toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
      return;
    }
    reload();
  };

  const disconnect = async () => {
    await raw("api/connections/jira", { method: "DELETE" });
    reload();
  };

  const sites: { cloudId: string; url: string; name?: string }[] = st?.sites || [];

  return (
    <ProviderCard
      id="jira"
      name="Jira"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-body">
          <div className="p-who">
            <span className="p-em">{st.account || st.email}</span>
            <span className="p-pl">{st.authKind === "oauth" ? tr("tracker.jira_via_oauth") : tr("tracker.jira_via_token")}</span>
            {st.site && <span className="p-pl">{String(st.site).replace(/^https:\/\//, "")}</span>}
            <DisconnectButton onClick={disconnect} />
          </div>
          {/* One authorization can cover several sites, and only the user knows which one
              holds their work — so do not silently settle on the first (docs/log/80 §80.17). */}
          {sites.length > 1 && (
            <label className="ps-row">
              <span className="ps-label">{tr("tracker.jira_site")}</span>
              <select value={st.cloudId || ""} onChange={(e) => void pickSite(e.target.value)}>
                {sites.map((s) => (
                  <option key={s.cloudId} value={s.cloudId}>
                    {s.name || s.url.replace(/^https:\/\//, "")}
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
      ) : mode === "waiting" ? (
        <div className="p-body">
          <p className="muted">{status}</p>
          <button onClick={() => setMode("idle")}>{tr("common.cancel")}</button>
        </div>
      ) : (
        <div className="p-body">
          {mode !== "token" && (
            <div className="flow">
              <button disabled={!oauthAvailable} onClick={() => void startOAuth()}>
                {tr("tracker.jira_connect_oauth")}
              </button>
              <button onClick={() => setMode("token")}>{tr("tracker.jira_use_token")}</button>
            </div>
          )}
          {!oauthAvailable && mode !== "token" && <Hint>{tr("tracker.jira_oauth_unconfigured")}</Hint>}
          {mode === "token" && (
            <>
              <div className="flow">
                <input className="cinput" type="url" placeholder={tr("ops.jira_site_placeholder")} value={site} onChange={(e) => setSite(e.target.value)} />
              </div>
              <div className="flow">
                <input className="cinput" type="email" placeholder={tr("ops.jira_email_placeholder")} value={email} onChange={(e) => setEmail(e.target.value)} />
              </div>
              <div className="flow">
                <input className="cinput" type="password" placeholder={tr("ops.jira_token_placeholder")} value={token} onChange={(e) => setToken(e.target.value)} />
                <button disabled={busy || !ready} onClick={() => void saveToken()}>
                  {busy ? tr("admin.checking") : tr("conn.connect")}
                </button>
              </div>
              <Hint>{tr("ops.jira_hint")}</Hint>
            </>
          )}
        </div>
      )}
    </ProviderCard>
  );
}
