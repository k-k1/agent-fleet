import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText, raw } from "../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConnections } from "./useConnections.ts";
import { OnOff } from "./controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "./providerCard.tsx";
import { getLocale, useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";

// OpsTab is the home for service-operations connections (docs/25 Phase 1): external
// monitoring / incident tools the SRE assistant talks to over MCP. Today: PagerDuty,
// Grafana and CloudWatch. Credentials are stored container-side (encrypted secrets)
// and injected into the MCP server at spawn by `workspace-agent mcp-run` — they never
// reach the CP. (CloudWatch stores no secret at all: it rides the AWS cred chain.)
// The chat-bridge connection (docs/37 P1: Discord push notifications) lives here too
// — same encrypted store, same card pattern.
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
          <div className="conn-cat">{tr("ops.cat_chat")}</div>
          <DiscordCard st={conns.discord} reload={reload} />
          <SlackCard st={conns.slack} reload={reload} />
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

// DC_EVENTS: the toggleable notification groups (must mirror the backend's
// bridge.EventKeys — docs/37 P1).
const DC_EVENTS: [string, string][] = [
  ["answer-ready", "ops.ev_answer_ready"],
  ["question", "ops.ev_question"],
  ["permission-request", "ops.ev_permission"],
  ["exit", "ops.ev_exit"],
  ["session-report", "ops.ev_report"],
];

// DiscordCard: the chat-bridge connection (docs/37 P1) as a 3-step wizard. The
// user pastes their OWN bot's token (private guild — no central shared app);
// everything else is derived from it: the invite URL is generated from the
// token's application id (no OAuth2 URL Generator), the destination is picked
// from the bot's guild channels (no Developer Mode / numeric IDs), and connect
// fires a test notification so "did it arrive?" is answered on the spot. DM
// mode (manual user ID) remains as the advanced fallback.
function DiscordCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [token, setToken] = useState("");
  const [insp, setInsp] = useState<{ botName: string; inviteUrl: string; token: string } | null>(null);
  const [chans, setChans] = useState<
    { id: string; label: string; ownerId?: string; ownerName?: string }[] | null
  >(null);
  const [channel, setChannel] = useState("");
  const [dm, setDm] = useState(false);
  const [userId, setUserId] = useState("");
  const [threads, setThreads] = useState(true);
  const [receive, setReceive] = useState(false);
  const [fullText, setFullText] = useState(false); // 全文ブリッジ: post the answer body (docs/37)
  const [mirrorInput, setMirrorInput] = useState(true); // Fix ②: echo Console input to the thread (default on)
  const [mentionId, setMentionId] = useState("");
  const [autoMention, setAutoMention] = useState<{ id: string; name: string } | null>(null);
  const [events, setEvents] = useState<string[]>(DC_EVENTS.map(([k]) => k));
  const [showEvents, setShowEvents] = useState(false); // notification list collapsed by default (keeps 接続 in view)
  const [editing, setEditing] = useState(false); // edit an existing connection's settings without re-pasting the token
  const [busy, setBusy] = useState(false);
  // The PUT response IS the fresh connection status — trust it immediately
  // instead of waiting on the parent's api/connections refetch (a transient CP
  // failure there left the card on 未接続 right after a successful connect).
  const [localSt, setLocalSt] = useState<any>(null);
  const view = st?.connected ? st : localSt?.connected ? localSt : null;
  // Editing an existing connection reuses the stored token + destination server-side, so the
  // usual token/destination gate doesn't apply — only a fresh connect needs them.
  const ok = editing || (!!insp && (dm ? userId.trim() !== "" : channel !== "") && events.length > 0);

  // Auto-fill the mention target with the picked channel's guild owner (= the
  // user themself in the recommended private-server setup) unless the user has
  // typed their own value over a previous auto-fill.
  const pickChannel = (id: string, opts?: { id: string; ownerId?: string; ownerName?: string }[]) => {
    setChannel(id);
    const opt = (opts ?? chans ?? []).find((c) => c.id === id);
    if (opt?.ownerId && (mentionId === "" || mentionId === autoMention?.id)) {
      setMentionId(opt.ownerId);
      setAutoMention({ id: opt.ownerId, name: opt.ownerName || "" });
    }
  };

  const toggle = (key: string, on: boolean) =>
    setEvents((prev) => (on ? [...prev.filter((k) => k !== key), key] : prev.filter((k) => k !== key)));

  const verify = async () => {
    if (!token.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/discord/inspect", "POST", { token: token.trim() });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      setInsp({ botName: res.botName, inviteUrl: res.inviteUrl, token: token.trim() });
      setToken("");
    } finally {
      setBusy(false);
    }
  };

  // Poll the bot's guilds while the invite is pending; the moment it lands in a
  // server the channel picker fills in and the poll stops.
  const found = !!(chans && chans.length > 0);
  useEffect(() => {
    if (!insp || dm || found) return;
    let stopped = false;
    const load = async () => {
      const res = await apiJSON("api/connections/discord/guilds", "POST", { token: insp.token });
      if (stopped || !res || res.error) return; // unreachable etc. — keep waiting
      const guilds = Array.isArray(res.guilds) ? res.guilds : [];
      const opts = guilds.flatMap((g: any) =>
        (g.channels || []).map((c: any) => ({
          id: c.id,
          label: (guilds.length > 1 ? g.name + " / " : "") + "#" + c.name,
          ownerId: g.ownerId as string | undefined,
          ownerName: g.ownerName as string | undefined,
        })),
      );
      setChans(opts);
      if (opts.length === 1) pickChannel(opts[0].id, opts);
    };
    void load();
    const t = setInterval(load, 3000);
    return () => {
      stopped = true;
      clearInterval(t);
    };
  }, [insp, dm, found]);

  const save = async () => {
    if (!ok || (!insp && !editing)) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/discord", "PUT", {
        // Editing omits the token: the server reuses the stored one (and destination).
        token: editing ? "" : insp!.token,
        channelId: dm ? "" : channel,
        userId: dm ? userId.trim() : "",
        events,
        threads: !dm && threads,
        receive: !dm && receive, // P2a inbound: route thread replies back into the session
        mirrorInput: !dm && threads ? mirrorInput : undefined, // Fix ②: echo Console input (thread mode only)
        fullText, // 全文ブリッジ: applies to either destination mode (docs/37)
        mentionUserId: dm ? "" : mentionId.trim(),
        lang: getLocale(), // notifications follow the Console language at connect time
      });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      toast(res?.testError ? tr("ops.dc_test_failed", { msg: String(res.testError) }) : tr("ops.dc_test_sent"));
      setLocalSt(res);
      setInsp(null);
      setEditing(false);
      setChans(null);
      setChannel("");
      setUserId("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  // startEdit reopens the settings form for a live connection, prefilled from its status —
  // no token re-entry, no channel re-pick (docs/37: 接続後に通知設定を変えられるように).
  const startEdit = () => {
    if (!view) return;
    setDm(view.mode === "dm");
    setThreads(!!view.threads);
    setReceive(!!view.receive);
    setMirrorInput(view.mirrorInput !== false); // default on when unset
    setFullText(!!view.fullText);
    setMentionId(view.mentionUserId || "");
    setChannel(view.channelId || "");
    setEvents(Array.isArray(view.events) && view.events.length ? view.events : DC_EVENTS.map(([k]) => k));
    setEditing(true);
  };
  const disconnect = async () => {
    await raw("api/connections/discord", { method: "DELETE" });
    setLocalSt(null);
    reload();
  };

  return (
    <ProviderCard
      id="discord"
      name="Discord"
      status={<StatusPill on={!!view}>{view ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {view && !editing ? (
        <div className="p-who">
          <span className="p-em">
            {tr(view.mode === "channel" ? "ops.dc_connected_channel" : "ops.dc_connected_dm")}
          </span>
          {view.botName && <span className="p-pl">{view.botName}</span>}
          {view.threads && <span className="p-pl">{tr("ops.dc_pill_threads")}</span>}
          {view.mention && <span className="p-pl">@</span>}
          {view.receive && <span className="p-pl">{tr("ops.dc_pill_receive")}</span>}
          {view.mirrorInput && <span className="p-pl">{tr("ops.dc_pill_mirror")}</span>}
          {view.operator && <span className="p-pl">{tr("ops.dc_pill_operator")}</span>}
          {view.fullText && <span className="p-pl">{tr("ops.dc_pill_fulltext")}</span>}
          {Array.isArray(view.events) && view.events.length < DC_EVENTS.length && (
            <span className="p-pl">
              {DC_EVENTS.filter(([k]) => view.events.includes(k))
                .map(([, l]) => tr(l as Parameters<typeof tr>[0]))
                .join(" / ")}
            </span>
          )}
          <button className="ghost" onClick={startEdit}>
            {tr("ops.dc_edit")}
          </button>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : !insp && !editing ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("ops.dc_token_placeholder")}
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button disabled={busy || !token.trim()} onClick={verify}>
              {tr("ops.dc_verify")}
            </button>
          </div>
          <Hint>{tr("ops.dc_hint")}</Hint>
        </div>
      ) : (
        <div className="p-body">
          {/* Invite + destination picking only when connecting fresh; an edit keeps them. */}
          {insp && (
            <div className="flow">
              <span className="p-em">{insp.botName}</span>
              <button onClick={() => window.open(insp.inviteUrl, "_blank", "noopener")}>
                {tr("ops.dc_invite")}
              </button>
            </div>
          )}
          {insp && dm && (
            <div className="flow">
              <input
                className="cinput"
                type="text"
                placeholder={tr("ops.dc_user_placeholder")}
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
              />
            </div>
          )}
          {insp && !dm && !found && <p className="muted">{tr("ops.dc_waiting_guild")}</p>}
          {insp && !dm && found && (
            <div className="flow">
              <select className="cinput" value={channel} onChange={(e) => pickChannel(e.target.value)}>
                <option value="">{tr("ops.dc_channel_select")}</option>
                {chans!.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.label}
                  </option>
                ))}
              </select>
            </div>
          )}
          {/* Channel-mode settings: after a channel is picked (wizard) or always (edit). */}
          {!dm && (found || editing) && (
            <>
              <div className="ps-row">
                <span className="ps-label">
                  {tr("ops.dc_threads_label")}
                  <span className="sub">{tr("ops.dc_threads_sub")}</span>
                </span>
                <OnOff value={threads} onChange={setThreads} />
              </div>
              <div className="ps-row">
                <span className="ps-label">
                  {tr("ops.dc_mention_label")}
                  {autoMention && mentionId === autoMention.id && autoMention.name && (
                    <span className="sub">{tr("ops.dc_mention_auto", { name: autoMention.name })}</span>
                  )}
                </span>
                <input
                  className="cinput"
                  type="text"
                  style={{ maxWidth: "14em" }}
                  placeholder={tr("ops.dc_mention_placeholder")}
                  value={mentionId}
                  onChange={(e) => setMentionId(e.target.value)}
                />
              </div>
              <div className="ps-row">
                <span className="ps-label">
                  {tr("ops.dc_receive_label")}
                  <span className="sub">{tr("ops.dc_receive_sub")}</span>
                </span>
                <OnOff value={receive} onChange={setReceive} />
              </div>
              {/* Fix ②: mirror Console input into the thread — only meaningful with threads. */}
              {threads && (
                <div className="ps-row">
                  <span className="ps-label">
                    {tr("ops.dc_mirror_label")}
                    <span className="sub">{tr("ops.dc_mirror_sub")}</span>
                  </span>
                  <OnOff value={mirrorInput} onChange={setMirrorInput} />
                </div>
              )}
            </>
          )}
          {/* Notification list collapsed by default so the primary action stays in view. */}
          <div className="ps-row ps-clickable" onClick={() => setShowEvents((v) => !v)}>
            <span className="ps-label">
              {tr("ops.dc_events_label")}
              <span className="sub">
                {events.length === DC_EVENTS.length
                  ? tr("ops.dc_events_all")
                  : `${events.length} / ${DC_EVENTS.length}`}
              </span>
            </span>
            <span className="muted">{showEvents ? "▾" : "▸"}</span>
          </div>
          {showEvents &&
            DC_EVENTS.map(([key, label]) => (
              <div className="ps-row" key={key}>
                <span className="ps-label">{tr(label as Parameters<typeof tr>[0])}</span>
                <OnOff value={events.includes(key)} onChange={(on) => toggle(key, on)} />
              </div>
            ))}
          {/* 全文ブリッジ (docs/37): works in either mode, once a destination is chosen. */}
          {(found || editing || dm) && (
            <div className="ps-row">
              <span className="ps-label">
                {tr("ops.dc_fulltext_label")}
                <span className="sub">{tr("ops.dc_fulltext_sub")}</span>
              </span>
              <OnOff value={fullText} onChange={setFullText} />
            </div>
          )}
          <div className="flow">
            <button disabled={busy || !ok} onClick={save}>
              {tr(editing ? "common.save" : "conn.connect")}
            </button>
            {editing ? (
              <button className="ghost" onClick={() => setEditing(false)}>
                {tr("common.cancel")}
              </button>
            ) : (
              <button className="ghost" onClick={() => setDm(!dm)}>
                {tr(dm ? "ops.dc_advanced_channel" : "ops.dc_advanced_dm")}
              </button>
            )}
          </div>
        </div>
      )}
    </ProviderCard>
  );
}

// SlackCard: the chat-bridge Slack connection (docs/37 Slack 追随), the Socket-Mode twin of
// DiscordCard. The user pastes their OWN app's two tokens (bot xoxb- for the Web API + app-
// level xapp- for Socket-Mode receive); Verify validates them and returns the workspace +
// bot identity. The destination is picked from the channels the bot was invited to
// (users.conversations), and the bound user id (mention + identity binding) is auto-resolved
// from the member's email — no Copy-Member-ID. DM mode (manual user id) is the advanced
// fallback. Connect fires a test notification so "did it arrive?" is answered on the spot.
function SlackCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const email = useTenantStore((s) => s.whoami?.email || "");
  const [botToken, setBotToken] = useState("");
  const [appToken, setAppToken] = useState("");
  const [insp, setInsp] = useState<{ botName: string; teamName: string; botToken: string; appToken: string } | null>(null);
  const [chans, setChans] = useState<{ id: string; label: string }[] | null>(null);
  const [channel, setChannel] = useState("");
  const [dm, setDm] = useState(false);
  const [userId, setUserId] = useState("");
  const [threads, setThreads] = useState(true);
  const [receive, setReceive] = useState(false);
  const [fullText, setFullText] = useState(false);
  const [mirrorInput, setMirrorInput] = useState(true);
  const [events, setEvents] = useState<string[]>(DC_EVENTS.map(([k]) => k));
  const [showEvents, setShowEvents] = useState(false);
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [localSt, setLocalSt] = useState<any>(null);
  const view = st?.connected ? st : localSt?.connected ? localSt : null;
  const ok = editing || (!!insp && (dm ? userId.trim() !== "" : channel !== "") && events.length > 0 && (!receive || userId.trim() !== ""));

  const toggle = (key: string, on: boolean) =>
    setEvents((prev) => (on ? [...prev.filter((k) => k !== key), key] : prev.filter((k) => k !== key)));

  const verify = async () => {
    if (!botToken.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/slack/inspect", "POST", {
        botToken: botToken.trim(),
        appToken: appToken.trim(),
      });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      setInsp({ botName: res.botName, teamName: res.teamName, botToken: botToken.trim(), appToken: appToken.trim() });
      setBotToken("");
      setAppToken("");
    } finally {
      setBusy(false);
    }
  };

  // Poll the channels the bot was invited to; auto-resolve the bound user id from the email.
  const found = !!(chans && chans.length > 0);
  useEffect(() => {
    if (!insp || dm || found) return;
    let stopped = false;
    const load = async () => {
      const res = await apiJSON("api/connections/slack/channels", "POST", { botToken: insp.botToken, email });
      if (stopped || !res || res.error) return;
      const opts = (Array.isArray(res.channels) ? res.channels : []).map((c: any) => ({ id: c.id, label: "#" + c.name }));
      setChans(opts);
      if (res.resolvedUserId && userId === "") setUserId(res.resolvedUserId);
      if (opts.length === 1) setChannel(opts[0].id);
    };
    void load();
    const t = setInterval(load, 3000);
    return () => {
      stopped = true;
      clearInterval(t);
    };
  }, [insp, dm, found, email, userId]);

  const save = async () => {
    if (!ok || (!insp && !editing)) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/slack", "PUT", {
        botToken: editing ? "" : insp!.botToken,
        appToken: editing ? "" : insp!.appToken,
        channelId: dm ? "" : channel,
        userId: userId.trim(),
        events,
        threads: !dm && threads,
        receive: !dm && receive,
        mirrorInput: !dm && threads ? mirrorInput : undefined,
        fullText,
        lang: getLocale(),
      });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      toast(res?.testError ? tr("ops.dc_test_failed", { msg: String(res.testError) }) : tr("ops.dc_test_sent"));
      setLocalSt(res);
      setInsp(null);
      setEditing(false);
      setChans(null);
      setChannel("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const startEdit = () => {
    if (!view) return;
    setDm(view.mode === "dm");
    setThreads(!!view.threads);
    setReceive(!!view.receive);
    setMirrorInput(view.mirrorInput !== false);
    setFullText(!!view.fullText);
    setUserId(view.userId || "");
    setChannel(view.channelId || "");
    setEvents(Array.isArray(view.events) && view.events.length ? view.events : DC_EVENTS.map(([k]) => k));
    setEditing(true);
  };
  const disconnect = async () => {
    await raw("api/connections/slack", { method: "DELETE" });
    setLocalSt(null);
    reload();
  };

  return (
    <ProviderCard
      id="slack"
      name="Slack"
      status={<StatusPill on={!!view}>{view ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {view && !editing ? (
        <div className="p-who">
          <span className="p-em">
            {tr(view.mode === "channel" ? "ops.dc_connected_channel" : "ops.dc_connected_dm")}
          </span>
          {view.botName && <span className="p-pl">{view.botName}</span>}
          {view.teamName && <span className="p-pl">{view.teamName}</span>}
          {view.threads && <span className="p-pl">{tr("ops.dc_pill_threads")}</span>}
          {view.receive && <span className="p-pl">{tr("ops.dc_pill_receive")}</span>}
          {view.mirrorInput && <span className="p-pl">{tr("ops.dc_pill_mirror")}</span>}
          {view.operator && <span className="p-pl">{tr("ops.dc_pill_operator")}</span>}
          {view.fullText && <span className="p-pl">{tr("ops.dc_pill_fulltext")}</span>}
          {Array.isArray(view.events) && view.events.length < DC_EVENTS.length && (
            <span className="p-pl">
              {DC_EVENTS.filter(([k]) => view.events.includes(k))
                .map(([, l]) => tr(l as Parameters<typeof tr>[0]))
                .join(" / ")}
            </span>
          )}
          <button className="ghost" onClick={startEdit}>
            {tr("ops.dc_edit")}
          </button>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : !insp && !editing ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("ops.sl_bot_placeholder")}
              value={botToken}
              onChange={(e) => setBotToken(e.target.value)}
            />
          </div>
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("ops.sl_app_placeholder")}
              value={appToken}
              onChange={(e) => setAppToken(e.target.value)}
            />
            <button disabled={busy || !botToken.trim()} onClick={verify}>
              {tr("ops.dc_verify")}
            </button>
          </div>
          <Hint>{tr("ops.sl_hint")}</Hint>
        </div>
      ) : (
        <div className="p-body">
          {insp && (
            <div className="flow">
              <span className="p-em">{insp.botName}</span>
              {insp.teamName && <span className="p-pl">{insp.teamName}</span>}
            </div>
          )}
          {insp && dm && (
            <div className="flow">
              <input
                className="cinput"
                type="text"
                placeholder={tr("ops.sl_user_placeholder")}
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
              />
            </div>
          )}
          {insp && !dm && !found && <p className="muted">{tr("ops.sl_waiting_channel")}</p>}
          {insp && !dm && found && (
            <div className="flow">
              <select className="cinput" value={channel} onChange={(e) => setChannel(e.target.value)}>
                <option value="">{tr("ops.dc_channel_select")}</option>
                {chans!.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.label}
                  </option>
                ))}
              </select>
            </div>
          )}
          {!dm && (found || editing) && (
            <>
              <div className="ps-row">
                <span className="ps-label">
                  {tr("ops.dc_threads_label")}
                  <span className="sub">{tr("ops.dc_threads_sub")}</span>
                </span>
                <OnOff value={threads} onChange={setThreads} />
              </div>
              <div className="ps-row">
                <span className="ps-label">
                  {tr("ops.sl_user_label")}
                  <span className="sub">{tr("ops.sl_user_sub")}</span>
                </span>
                <input
                  className="cinput"
                  type="text"
                  style={{ maxWidth: "12em" }}
                  placeholder={tr("ops.sl_user_placeholder")}
                  value={userId}
                  onChange={(e) => setUserId(e.target.value)}
                />
              </div>
              <div className="ps-row">
                <span className="ps-label">
                  {tr("ops.dc_receive_label")}
                  <span className="sub">{tr("ops.sl_receive_sub")}</span>
                </span>
                <OnOff value={receive} onChange={setReceive} />
              </div>
              {threads && (
                <div className="ps-row">
                  <span className="ps-label">
                    {tr("ops.dc_mirror_label")}
                    <span className="sub">{tr("ops.dc_mirror_sub")}</span>
                  </span>
                  <OnOff value={mirrorInput} onChange={setMirrorInput} />
                </div>
              )}
            </>
          )}
          <div className="ps-row ps-clickable" onClick={() => setShowEvents((v) => !v)}>
            <span className="ps-label">
              {tr("ops.dc_events_label")}
              <span className="sub">
                {events.length === DC_EVENTS.length ? tr("ops.dc_events_all") : `${events.length} / ${DC_EVENTS.length}`}
              </span>
            </span>
            <span className="muted">{showEvents ? "▾" : "▸"}</span>
          </div>
          {showEvents &&
            DC_EVENTS.map(([key, label]) => (
              <div className="ps-row" key={key}>
                <span className="ps-label">{tr(label as Parameters<typeof tr>[0])}</span>
                <OnOff value={events.includes(key)} onChange={(on) => toggle(key, on)} />
              </div>
            ))}
          {(found || editing || dm) && (
            <div className="ps-row">
              <span className="ps-label">
                {tr("ops.dc_fulltext_label")}
                <span className="sub">{tr("ops.dc_fulltext_sub")}</span>
              </span>
              <OnOff value={fullText} onChange={setFullText} />
            </div>
          )}
          <div className="flow">
            <button disabled={busy || !ok} onClick={save}>
              {tr(editing ? "common.save" : "conn.connect")}
            </button>
            {editing ? (
              <button className="ghost" onClick={() => setEditing(false)}>
                {tr("common.cancel")}
              </button>
            ) : (
              <button className="ghost" onClick={() => setDm(!dm)}>
                {tr(dm ? "ops.dc_advanced_channel" : "ops.dc_advanced_dm")}
              </button>
            )}
          </div>
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
