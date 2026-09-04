import { useEffect } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useConnections } from "../parts/useConnections.ts";
import { useSettingsUI } from "../store.ts";
import { OnOff, Row } from "../parts/controls.tsx";
import { getLocale, useT } from "../../../lib/i18n/index.ts";

// NotificationsTab — notification preferences. The upper section is the device-side audio
// notification (ttsSessionNotify / usageResetNotify, split off from text-to-speech). The lower
// one is the master on/off for notifications to the chat integrations (Discord / Slack): only
// a connected service is operable, and an unconnected one offers a link to the chat settings.
// The master toggles the connection's notifyOff on the backend, which stops sending without
// disconnecting. To avoid wiping the other detailed settings, the whole payload is rebuilt
// from the current status and only notifyOff is replaced.
export function NotificationsTab() {
  const tr = useT();
  const toast = useToast();
  const s = useSettings();
  const running = useWorkspaceStore((st) => st.state) === "running";
  const openSettings = useSettingsUI((st) => st.openSettings);
  const { conns, reload } = useConnections();
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  // Flip only the master mute, preserving the connection's other settings (the backend
  // PUT overwrites non-pointer fields, so resend them from the current status).
  const setNotify = async (kind: "discord" | "slack", st: any, on: boolean) => {
    const res = await apiJSON(`api/connections/${kind}`, "PUT", {
      // token + destination omitted → the backend reuses the stored connection.
      events: Array.isArray(st?.events) ? st.events : [],
      threads: !!st?.threads,
      receive: !!st?.receive,
      fullText: !!st?.fullText,
      mirrorInput: st?.mirrorInput !== false,
      mentionUserId: st?.mentionUserId || "",
      // lang is a non-pointer field: omitting it resets the stored value to the default
      // (Japanese). Send the current locale every time, as the connection card does — the
      // notification language follows the Console's display language by design.
      lang: getLocale(),
      notifyOff: !on,
    });
    if (res && res.error) {
      toast(tr("common.save_failed_msg", { msg: String(res.error.message || res.error) }));
      return;
    }
    reload();
  };

  const services: ["discord" | "slack", string][] = [
    ["discord", "Discord"],
    ["slack", "Slack"],
  ];

  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">{tr("noti.audio_title")}</h4>
        <Row label={tr("tts.session_notify")}>
          <OnOff value={s.ttsSessionNotify} onChange={(v) => setSetting("ttsSessionNotify", v)} />
        </Row>
        <p className="muted ds-note">{tr("tts.note_session_notify")}</p>
        <Row label={tr("tts.usage_reset_notify")}>
          <OnOff value={s.usageResetNotify} onChange={(v) => setSetting("usageResetNotify", v)} />
        </Row>
        <p className="muted ds-note">{tr("tts.note_usage_reset_notify")}</p>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("noti.svc_title")}</h4>
        {!running ? (
          <p className="muted ds-note">{tr("noti.svc_ws_stopped")}</p>
        ) : !conns ? (
          <p className="muted pad">{tr("common.loading")}</p>
        ) : (
          services.map(([kind, name]) => {
            const st = conns[kind];
            return (
              <Row key={kind} label={name}>
                {st?.connected ? (
                  <OnOff value={st.notify !== false} onChange={(v) => setNotify(kind, st, v)} />
                ) : (
                  <button type="button" className="linklike" onClick={() => openSettings("chat")}>
                    {tr("noti.svc_open_chat")}
                  </button>
                )}
              </Row>
            );
          })
        )}
        <p className="muted ds-note">{tr("noti.svc_note")}</p>
      </section>
    </div>
  );
}
