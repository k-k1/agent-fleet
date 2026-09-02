import { useEffect, useState } from "react";
import { apiJSON, errText, raw } from "../../core/api/client.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { OnOff } from "./controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton, IssueLink } from "./providerCard.tsx";
import { getLocale, useT } from "../../lib/i18n/index.ts";
import { SettingsPanel, PsRow, CHAT_EVENTS, ALL_EVENTS } from "./chatCardParts.tsx";

// DiscordCard — connect = token → verify → invite → pick channel → 接続 (adjacent). The
// detail settings appear only AFTER connect, in the collapsible SettingsPanel that
// auto-saves each toggle. docs/log/37 P1.
export function DiscordCard({ st, reload }: { st: any; reload: () => void }) {
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
