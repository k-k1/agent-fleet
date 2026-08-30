import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { apiJSON, errText, raw } from "../../core/api/client.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConnections } from "./useConnections.ts";
import { OnOff } from "./controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton, IssueLink } from "./providerCard.tsx";
import { useTenantStore } from "../../core/store/tenant.ts";
import { getLocale, useT } from "../../lib/i18n/index.ts";

// ChatTab (チャット連携) — the chat-bridge CONNECTIONS (Discord / Slack, docs/log/37), split
// out of 運用・監視 into their own 接続 tab: these are notification destinations, not
// monitoring providers. Each card separates CONNECT (token → verify → pick channel →
// 接続, minimal) from the detail SETTINGS (threads / mention / receive / mirror / events /
// full-text), which live in a collapsible 通知設定 disclosure that AUTO-SAVES each toggle
// (like the agent 動作設定) — no 編集/保存 button. The master 通知 ON/OFF lives in
// 個人設定 › 通知. Credentials are stored container-side (encrypted) and injected into the
// MCP/bridge at spawn; they never reach the CP.
export function ChatTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const { conns, reload } = useConnections();
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
          <p className="muted ds-note">{tr("chat.intro")}</p>
          <DiscordCard st={conns.discord} reload={reload} />
          <SlackCard st={conns.slack} reload={reload} />
        </>
      )}
    </div>
  );
}

// The toggleable notification event groups (mirror the backend's bridge.EventKeys).
const CHAT_EVENTS: [string, string][] = [
  ["answer-ready", "ops.ev_answer_ready"],
  ["question", "ops.ev_question"],
  ["permission-request", "ops.ev_permission"],
  ["exit", "ops.ev_exit"],
  ["session-report", "ops.ev_report"],
];
const ALL_EVENTS = CHAT_EVENTS.map(([k]) => k);

// SettingsPanel — the collapsible 通知設定 disclosure shown on a connected card, mirroring
// the agent 動作設定 (P2 CardSettings): collapsed by default, so the card reads "connect"
// first with the detail settings a deliberate second level.
function SettingsPanel({ children }: { children?: ReactNode }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className={"p-settings" + (open ? " open" : "")}>
      <button type="button" className="ps-disclosure" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="ps-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        {tr("chat.settings")}
      </button>
      {open && <div className="ps-body">{children}</div>}
    </div>
  );
}

// A labeled settings row (connection-card style: .ps-row).
function PsRow({ label, sub, children }: { label: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="ps-row">
      <span className="ps-label">
        {label}
        {sub && <span className="sub">{sub}</span>}
      </span>
      {children}
    </div>
  );
}

// DiscordCard — connect = token → verify → invite → pick channel → 接続 (adjacent). The
// detail settings appear only AFTER connect, in the collapsible SettingsPanel that
// auto-saves each toggle. docs/log/37 P1.
function DiscordCard({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [token, setToken] = useState("");
  const [insp, setInsp] = useState<{ botName: string; inviteUrl: string; token: string } | null>(null);
  const [chans, setChans] = useState<{ id: string; label: string; ownerId?: string; ownerName?: string }[] | null>(null);
  const [channel, setChannel] = useState("");
  const [dm, setDm] = useState(false);
  const [userId, setUserId] = useState("");
  // Detail settings — defaults applied on connect, then edited in the panel (auto-save).
  const [threads, setThreads] = useState(true);
  const [receive, setReceive] = useState(false);
  const [fullText, setFullText] = useState(false);
  const [mirror, setMirror] = useState(true);
  const [mention, setMention] = useState("");
  const [autoMention, setAutoMention] = useState<{ id: string; name: string } | null>(null);
  const [events, setEvents] = useState<string[]>(ALL_EVENTS);
  const [busy, setBusy] = useState(false);
  // The PUT response is the fresh status — trust it immediately (a transient parent
  // refetch failure otherwise flips the card back to 未接続 right after a change).
  const [localSt, setLocalSt] = useState<any>(null);
  const view = localSt?.connected ? localSt : st?.connected ? st : null;

  // Seed the detail settings from the live connection, re-seeding only when the
  // connection IDENTITY changes (mode/channel) — not on every status refresh, so a
  // just-toggled value isn't clobbered before the parent reload catches up.
  useEffect(() => {
    if (!view) return;
    setDm(view.mode === "dm");
    setThreads(!!view.threads);
    setReceive(!!view.receive);
    setMirror(view.mirrorInput !== false);
    setFullText(!!view.fullText);
    setMention(view.mentionUserId || "");
    setChannel(view.channelId || "");
    setEvents(Array.isArray(view.events) && view.events.length ? view.events : ALL_EVENTS);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view?.mode, view?.channelId]);

  const pickChannel = (id: string, opts?: typeof chans) => {
    setChannel(id);
    const opt = (opts ?? chans ?? []).find((c) => c.id === id);
    if (opt?.ownerId && (mention === "" || mention === autoMention?.id)) {
      setMention(opt.ownerId);
      setAutoMention({ id: opt.ownerId, name: opt.ownerName || "" });
    }
  };

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

  // Poll the bot's guilds while the invite is pending; fill the channel picker on arrival.
  const found = !!(chans && chans.length > 0);
  useEffect(() => {
    if (!insp || dm || found) return;
    let stopped = false;
    const load = async () => {
      const res = await apiJSON("api/connections/discord/guilds", "POST", { token: insp.token });
      if (stopped || !res || res.error) return;
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [insp, dm, found]);

  // Build the PUT body from the current settings; `token` empty = edit (reuse stored).
  const body = (token: string, over: Record<string, any> = {}) => ({
    token,
    channelId: dm ? "" : channel,
    userId: dm ? userId.trim() : "",
    events,
    threads: !dm && threads,
    receive: !dm && receive,
    mirrorInput: !dm && threads ? mirror : undefined,
    mentionUserId: dm ? "" : mention.trim(),
    fullText,
    lang: getLocale(),
    ...over,
  });

  // Fresh connect: sends the token + destination + the DEFAULT settings; the backend
  // fires one test notification so "did it arrive?" is answered on the spot.
  const connect = async () => {
    if (!insp || (dm ? !userId.trim() : !channel)) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/discord", "PUT", body(insp.token));
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      toast(res?.testError ? tr("ops.dc_test_failed", { msg: String(res.testError) }) : tr("ops.dc_test_sent"));
      setLocalSt(res);
      setInsp(null);
      setChans(null);
    } finally {
      setBusy(false);
    }
    reload();
  };

  // Auto-save a settings change on a live connection (edit: token omitted → reuse stored).
  // The full payload is sent so the backend's non-pointer fields aren't wiped; no test
  // notification fires on an edit (backend), so per-toggle saves don't spam the channel.
  const persist = async (over: Record<string, any> = {}) => {
    const res = await apiJSON("api/connections/discord", "PUT", body("", over));
    if (res && res.error) {
      toast(tr("conn.connect_failed", { msg: errText(res.error) }));
      return;
    }
    setLocalSt(res);
    reload();
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
      {view ? (
        <>
          <div className="p-who">
            <span className="p-em">{tr(view.mode === "channel" ? "ops.dc_connected_channel" : "ops.dc_connected_dm")}</span>
            {view.botName && <span className="p-pl">{view.botName}</span>}
            {view.operator && <span className="p-pl">{tr("ops.dc_pill_operator")}</span>}
            <DisconnectButton onClick={disconnect} />
          </div>
          <SettingsPanel>
            {view.mode === "channel" && (
              <>
                <PsRow label={tr("ops.dc_threads_label")} sub={tr("ops.dc_threads_sub")}>
                  <OnOff value={threads} onChange={(v) => (setThreads(v), persist({ threads: v }))} />
                </PsRow>
                <PsRow
                  label={tr("ops.dc_mention_label")}
                  sub={autoMention && mention === autoMention.id && autoMention.name ? tr("ops.dc_mention_auto", { name: autoMention.name }) : undefined}
                >
                  <input
                    className="cinput"
                    type="text"
                    style={{ maxWidth: "14em" }}
                    placeholder={tr("ops.dc_mention_placeholder")}
                    value={mention}
                    onChange={(e) => setMention(e.target.value)}
                    onBlur={() => persist()}
                  />
                </PsRow>
                <PsRow label={tr("ops.dc_receive_label")} sub={tr("ops.dc_receive_sub")}>
                  <OnOff value={receive} onChange={(v) => (setReceive(v), persist({ receive: v }))} />
                </PsRow>
                {threads && (
                  <PsRow label={tr("ops.dc_mirror_label")} sub={tr("ops.dc_mirror_sub")}>
                    <OnOff value={mirror} onChange={(v) => (setMirror(v), persist({ mirrorInput: v }))} />
                  </PsRow>
                )}
              </>
            )}
            {CHAT_EVENTS.map(([key, label]) => (
              <PsRow key={key} label={tr(label as Parameters<typeof tr>[0])}>
                <OnOff
                  value={events.includes(key)}
                  onChange={(on) => {
                    const next = on ? [...events.filter((k) => k !== key), key] : events.filter((k) => k !== key);
                    setEvents(next);
                    persist({ events: next });
                  }}
                />
              </PsRow>
            ))}
            <PsRow label={tr("ops.dc_fulltext_label")} sub={tr("ops.dc_fulltext_sub")}>
              <OnOff value={fullText} onChange={(v) => (setFullText(v), persist({ fullText: v }))} />
            </PsRow>
          </SettingsPanel>
        </>
      ) : !insp ? (
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
          <IssueLink url="https://discord.com/developers/applications" />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <span className="p-em">{insp.botName}</span>
            <button onClick={() => window.open(insp.inviteUrl, "_blank", "noopener")}>{tr("ops.dc_invite")}</button>
          </div>
          {dm ? (
            <div className="flow">
              <input
                className="cinput"
                type="text"
                placeholder={tr("ops.dc_user_placeholder")}
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
              />
              <button disabled={busy || !userId.trim()} onClick={connect}>
                {tr("conn.connect")}
              </button>
            </div>
          ) : !found ? (
            <p className="muted">{tr("ops.dc_waiting_guild")}</p>
          ) : (
            <div className="flow">
              <select className="cinput" value={channel} onChange={(e) => pickChannel(e.target.value)}>
                <option value="">{tr("ops.dc_channel_select")}</option>
                {chans!.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.label}
                  </option>
                ))}
              </select>
              <button disabled={busy || !channel} onClick={connect}>
                {tr("conn.connect")}
              </button>
            </div>
          )}
          <button className="linklike" onClick={() => setDm(!dm)}>
            {tr(dm ? "ops.dc_advanced_channel" : "ops.dc_advanced_dm")}
          </button>
        </div>
      )}
    </ProviderCard>
  );
}

// SlackCard — the Socket-Mode twin of DiscordCard (docs/log/37 Slack 追随). Two tokens (bot
// xoxb- + app-level xapp-) → verify → pick channel → 接続; details auto-save afterward.
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
  const [mirror, setMirror] = useState(true);
  const [events, setEvents] = useState<string[]>(ALL_EVENTS);
  const [busy, setBusy] = useState(false);
  const [localSt, setLocalSt] = useState<any>(null);
  const view = localSt?.connected ? localSt : st?.connected ? st : null;

  useEffect(() => {
    if (!view) return;
    setDm(view.mode === "dm");
    setThreads(!!view.threads);
    setReceive(!!view.receive);
    setMirror(view.mirrorInput !== false);
    setFullText(!!view.fullText);
    setUserId(view.userId || "");
    setChannel(view.channelId || "");
    setEvents(Array.isArray(view.events) && view.events.length ? view.events : ALL_EVENTS);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view?.mode, view?.channelId]);

  const verify = async () => {
    if (!botToken.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/slack/inspect", "POST", { botToken: botToken.trim(), appToken: appToken.trim() });
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [insp, dm, found, email, userId]);

  const body = (botToken: string, appToken: string, over: Record<string, any> = {}) => ({
    botToken,
    appToken,
    channelId: dm ? "" : channel,
    userId: userId.trim(),
    events,
    threads: !dm && threads,
    receive: !dm && receive,
    mirrorInput: !dm && threads ? mirror : undefined,
    fullText,
    lang: getLocale(),
    ...over,
  });

  const connect = async () => {
    if (!insp || (dm ? !userId.trim() : !channel) || (receive && !userId.trim())) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/slack", "PUT", body(insp.botToken, insp.appToken));
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      toast(res?.testError ? tr("ops.dc_test_failed", { msg: String(res.testError) }) : tr("ops.dc_test_sent"));
      setLocalSt(res);
      setInsp(null);
      setChans(null);
    } finally {
      setBusy(false);
    }
    reload();
  };

  const persist = async (over: Record<string, any> = {}) => {
    const res = await apiJSON("api/connections/slack", "PUT", body("", "", over));
    if (res && res.error) {
      toast(tr("conn.connect_failed", { msg: errText(res.error) }));
      return;
    }
    setLocalSt(res);
    reload();
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
      {view ? (
        <>
          <div className="p-who">
            <span className="p-em">{tr(view.mode === "channel" ? "ops.dc_connected_channel" : "ops.dc_connected_dm")}</span>
            {view.botName && <span className="p-pl">{view.botName}</span>}
            {view.teamName && <span className="p-pl">{view.teamName}</span>}
            {view.operator && <span className="p-pl">{tr("ops.dc_pill_operator")}</span>}
            <DisconnectButton onClick={disconnect} />
          </div>
          <SettingsPanel>
            {view.mode === "channel" && (
              <>
                <PsRow label={tr("ops.dc_threads_label")} sub={tr("ops.dc_threads_sub")}>
                  <OnOff value={threads} onChange={(v) => (setThreads(v), persist({ threads: v }))} />
                </PsRow>
                <PsRow label={tr("ops.sl_user_label")} sub={tr("ops.sl_user_sub")}>
                  <input
                    className="cinput"
                    type="text"
                    style={{ maxWidth: "12em" }}
                    placeholder={tr("ops.sl_user_placeholder")}
                    value={userId}
                    onChange={(e) => setUserId(e.target.value)}
                    onBlur={() => persist()}
                  />
                </PsRow>
                <PsRow label={tr("ops.dc_receive_label")} sub={tr("ops.sl_receive_sub")}>
                  <OnOff value={receive} onChange={(v) => (setReceive(v), persist({ receive: v }))} />
                </PsRow>
                {threads && (
                  <PsRow label={tr("ops.dc_mirror_label")} sub={tr("ops.dc_mirror_sub")}>
                    <OnOff value={mirror} onChange={(v) => (setMirror(v), persist({ mirrorInput: v }))} />
                  </PsRow>
                )}
              </>
            )}
            {CHAT_EVENTS.map(([key, label]) => (
              <PsRow key={key} label={tr(label as Parameters<typeof tr>[0])}>
                <OnOff
                  value={events.includes(key)}
                  onChange={(on) => {
                    const next = on ? [...events.filter((k) => k !== key), key] : events.filter((k) => k !== key);
                    setEvents(next);
                    persist({ events: next });
                  }}
                />
              </PsRow>
            ))}
            <PsRow label={tr("ops.dc_fulltext_label")} sub={tr("ops.dc_fulltext_sub")}>
              <OnOff value={fullText} onChange={(v) => (setFullText(v), persist({ fullText: v }))} />
            </PsRow>
          </SettingsPanel>
        </>
      ) : !insp ? (
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
          <IssueLink url="https://api.slack.com/apps" />
        </div>
      ) : (
        <div className="p-body">
          <div className="flow">
            <span className="p-em">{insp.botName}</span>
            {insp.teamName && <span className="p-pl">{insp.teamName}</span>}
          </div>
          {dm ? (
            <div className="flow">
              <input
                className="cinput"
                type="text"
                placeholder={tr("ops.sl_user_placeholder")}
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
              />
              <button disabled={busy || !userId.trim()} onClick={connect}>
                {tr("conn.connect")}
              </button>
            </div>
          ) : !found ? (
            <p className="muted">{tr("ops.sl_waiting_channel")}</p>
          ) : (
            <div className="flow">
              <select className="cinput" value={channel} onChange={(e) => setChannel(e.target.value)}>
                <option value="">{tr("ops.dc_channel_select")}</option>
                {chans!.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.label}
                  </option>
                ))}
              </select>
              <button disabled={busy || !channel} onClick={connect}>
                {tr("conn.connect")}
              </button>
            </div>
          )}
          <button className="linklike" onClick={() => setDm(!dm)}>
            {tr(dm ? "ops.dc_advanced_channel" : "ops.dc_advanced_dm")}
          </button>
        </div>
      )}
    </ProviderCard>
  );
}
