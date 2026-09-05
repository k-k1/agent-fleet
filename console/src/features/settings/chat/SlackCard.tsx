import { useEffect, useState } from "react";
import { apiJSON, errText, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { OnOff } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton, IssueLink } from "../parts/providerCard.tsx";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { getLocale, useT } from "../../../lib/i18n/index.ts";
import { SettingsPanel, PsRow, CHAT_EVENTS, ALL_EVENTS } from "./chatCardParts.tsx";

// SlackCard — the Socket-Mode twin of DiscordCard (docs/log/37 Slack follow-up). Two tokens (bot
// xoxb- + app-level xapp-) → verify → pick channel → connect; details auto-save afterward.
export function SlackCard({ st, reload }: { st: any; reload: () => void }) {
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
