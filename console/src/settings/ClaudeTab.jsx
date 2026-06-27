import { useCallback, useEffect, useState } from "react";
import { api, apiJSON } from "../api.js";

// ClaudeTab toggles a curated subset of the workspace's claude settings.json:
// Remote Control, push notifications, and the RTK token-saving hook. Reads/writes
// via the Agent, so it needs the workspace running. Changes apply to NEW sessions.
export default function ClaudeTab() {
  const [s, setS] = useState(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    setErr("");
    api("api/claude/settings")
      .then((d) => {
        if (d && d.error) {
          setErr(d.error.message || "取得に失敗しました");
          setS(null);
        } else setS(d);
      })
      .catch(() => setErr("Workspace が起動しているか確認してください"));
  }, []);
  useEffect(load, [load]);

  const update = async (patch) => {
    const d = await apiJSON("api/claude/settings", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setS(d);
  };

  if (err) return <p className="muted pad">{err}</p>;
  if (!s) return <p className="muted pad">読み込み中…</p>;

  return (
    <div className="display-settings">
      <p className="muted ds-note">変更は新しい claude セッションから反映されます。</p>
      <Row label="リモートコントロール">
        <OnOff value={s.remoteControlAtStartup} onChange={(v) => update({ remoteControlAtStartup: v })} />
      </Row>
      <Row label="通知">
        <OnOff value={s.agentPushNotifEnabled} onChange={(v) => update({ agentPushNotifEnabled: v })} />
      </Row>
      <Row label="RTK（トークン節約）">
        {s.rtk_available ? (
          <OnOff value={s.rtk_enabled} onChange={(v) => update({ rtk: v })} />
        ) : (
          <span className="muted">この workspace に rtk がありません</span>
        )}
      </Row>
    </div>
  );
}

function Row({ label, children }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}

function OnOff({ value, onChange }) {
  return (
    <div className="seg choice-seg">
      {[
        [true, "オン"],
        [false, "オフ"],
      ].map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          className={"seg-btn" + (!!value === v ? " active" : "")}
          onClick={() => onChange(v)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
