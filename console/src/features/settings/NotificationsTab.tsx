import { useSettings, setSetting } from "../../lib/settings.ts";
import { OnOff, Row } from "./controls.tsx";
import { useT } from "../../lib/i18n/index.ts";

// NotificationsTab — 通知（音声）系のクライアント設定。読み上げ(TtsTab)から分離した：
// セッション完了の音声通知(ttsSessionNotify) と 使用量リセットのお知らせ
// (usageResetNotify) は読み上げ本体の ON/OFF とは独立して効く通知プリファレンスなので、
// 読み上げタブに間借りするのをやめ「通知」として集約。ttsSessionNotify は通知センターの
// ミュートボタンからもトグルできる（ここが完全な一覧）。すべて端末ローカルの設定。
export function NotificationsTab() {
  const tr = useT();
  const s = useSettings();
  return (
    <div className="display-settings">
      <section className="ds-group">
        <Row label={tr("tts.session_notify")}>
          <OnOff value={s.ttsSessionNotify} onChange={(v) => setSetting("ttsSessionNotify", v)} />
        </Row>
        <p className="muted ds-note">{tr("tts.note_session_notify")}</p>
        <Row label={tr("tts.usage_reset_notify")}>
          <OnOff value={s.usageResetNotify} onChange={(v) => setSetting("usageResetNotify", v)} />
        </Row>
        <p className="muted ds-note">{tr("tts.note_usage_reset_notify")}</p>
      </section>
    </div>
  );
}
