import { NO_AUTOFILL } from "../../../ui/noAutofill.ts";
import { useEffect, useState } from "react";
import { errText, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { OnOff } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton, IssueLink } from "../parts/providerCard.tsx";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { getLocale, useT } from "../../../lib/i18n/index.ts";
import { assertBody } from "../workspace/workspaceApi.ts";
import { inspectSlack, listSlackChannels, putSlackConn, type ChatConn } from "./chatApi.ts";
import { SettingsPanel, PsRow, CHAT_EVENTS, ALL_EVENTS } from "./chatCardParts.tsx";
import { useGuardedPoll } from "../../../lib/readGuard.ts";

// SlackCard — the Socket-Mode twin of DiscordCard (docs/log/37 Slack follow-up). Two tokens (bot
// xoxb- + app-level xapp-) → verify → pick channel → connect; details auto-save afterward.
export function SlackCard({ st, reload }: { st: ChatConn | undefined; reload: () => void }) {
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
  const [localSt, setLocalSt] = useState<ChatConn | null>(null);
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
      const res = await inspectSlack(botToken.trim(), appToken.trim());
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      assertBody(res);
      setInsp({ botName: res.botName, teamName: res.teamName, botToken: botToken.trim(), appToken: appToken.trim() });
      setBotToken("");
      setAppToken("");
    } finally {
      setBusy(false);
    }
  };

  // Poll the bot's channels while the app is being added to one. The period does not restart with
  // each answer, so reads can overlap: only an answer newer than the last one landed fills the
  // picker, and one still in flight when the poll stops (a list arrived, DM chosen, connected) or
  // the bot token changes is dropped. A user id the first answer resolves reads again at once: the
  // Agent looks the user up after listing the channels, so the bot may have been invited since.
  const found = !!(chans && chans.length > 0);
  useGuardedPoll(
    (tk) => {
      if (!insp) return;
      void listSlackChannels(insp.botToken, email).then((res) => {
        if (!res || res.error || !tk.land()) return;
        const opts = (Array.isArray(res.channels) ? res.channels : []).map((c) => ({ id: c.id, label: "#" + c.name }));
        setChans(opts);
        const resolved = res.resolvedUserId;
        // An update, not the closure's userId: the answer may arrive after a render that set one.
        if (resolved) setUserId((cur) => (cur === "" ? resolved : cur));
        if (opts.length === 1) setChannel(opts[0].id);
      });
    },
    insp && !dm && !found ? 3000 : null,
    [insp, email, userId],
  );

  const body = (botToken: string, appToken: string, over: Record<string, unknown> = {}) => ({
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
      const res = await putSlackConn(body(insp.botToken, insp.appToken));
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errText(res.error) }));
        return;
      }
      toast(res?.testError ? tr("ops.dc_test_failed", { msg: String(res.testError) }) : tr("ops.sl_test_sent"));
      setLocalSt(res);
      setInsp(null);
      setChans(null);
    } finally {
      setBusy(false);
    }
    reload();
  };

  const persist = async (over: Record<string, unknown> = {}) => {
    const res = await putSlackConn(body("", "", over));
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
                  <input {...NO_AUTOFILL}
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
              <input {...NO_AUTOFILL}
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
