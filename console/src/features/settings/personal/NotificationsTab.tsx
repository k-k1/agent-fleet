import { useEffect } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useConnections } from "../parts/useConnections.ts";
import { useSettingsUI } from "../store.ts";
import { OnOff, Row } from "../parts/controls.tsx";
import { getLocale, useT } from "../../../lib/i18n/index.ts";

// NotificationsTab — 通知プリファレンス。上段は端末側の音声通知（読み上げから分離した
// ttsSessionNotify / usageResetNotify）。下段は チャット連携（Discord / Slack）への通知
// マスタ ON/OFF：接続済みのサービスだけ操作可（未接続はチャット連携への導線を出す）。
// マスタは接続の notifyOff（バックエンド）を切り替える — 切断せず送信だけ止める。他の
// 詳細設定を消さないよう、現在の status から全 payload を再構成して notifyOff だけ差し替える。
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
      // lang は非ポインタ扱いで、省略すると保存値が既定（日本語）へ戻る — 接続カードと
      // 同じく現ロケールを毎回載せる（通知言語は Console の表示言語に追随する仕様）。
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
