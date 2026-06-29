import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, ocwebURL } from "../api.js";

// AgentsTab consolidates per-agent settings. claude: Remote Control / push
// notifications / RTK hook (settings.json, via api/claude/settings). codex &
// opencode: the RTK token-saving toggle (durable pref → on-disk artifacts, via
// api/agents/rtk). Reads/writes go through the Agent, so the workspace must be
// running; changes apply to NEW sessions of each agent.
export default function AgentsTab() {
  const [claude, setClaude] = useState(null);
  const [agents, setAgents] = useState(null);
  const [ocweb, setOcweb] = useState(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    setErr("");
    Promise.all([api("api/claude/settings"), api("api/agents/rtk")])
      .then(([c, a]) => {
        if ((c && c.error) || (a && a.error)) {
          setErr(c?.error?.message || a?.error?.message || "取得に失敗しました");
          return;
        }
        setClaude(c);
        setAgents(a);
      })
      .catch(() => setErr("Workspace が起動しているか確認してください"));
    // opencode web is optional (older images lack it) — never blocks the tab.
    api("api/agents/opencode-web")
      .then((d) => {
        if (d && !d.error) setOcweb(d);
      })
      .catch(() => {});
  }, []);
  useEffect(load, [load]);

  const updateClaude = async (patch) => {
    const d = await apiJSON("api/claude/settings", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setClaude(d);
  };
  const updateAgents = async (patch) => {
    const d = await apiJSON("api/agents/rtk", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setAgents(d);
  };
  const updateOcweb = async (patch) => {
    const d = await apiJSON("api/agents/opencode-web", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setOcweb(d);
  };

  if (err) return <p className="muted pad">{err}</p>;
  if (!claude || !agents) return <p className="muted pad">読み込み中…</p>;

  return (
    <div className="display-settings">
      <p className="muted ds-note">変更は各エージェントの新しいセッションから反映されます。</p>

      <div className="conn-cat">Claude</div>
      <Row label="リモートコントロール">
        <OnOff value={claude.remoteControlAtStartup} onChange={(v) => updateClaude({ remoteControlAtStartup: v })} />
      </Row>
      <Row label="通知">
        <OnOff value={claude.agentPushNotifEnabled} onChange={(v) => updateClaude({ agentPushNotifEnabled: v })} />
      </Row>
      <RTKRow available={claude.rtk_available} value={claude.rtk_enabled} onChange={(v) => updateClaude({ rtk: v })} />

      <div className="conn-cat">Codex</div>
      <RTKRow
        available={agents.rtk_available}
        value={agents.codex_rtk}
        onChange={(v) => updateAgents({ codex_rtk: v })}
        note="codex はコマンド書換フックを持たないため指示ベース（ベストエフォート）。AGENTS.md で rtk 利用を促すだけで、強制ではありません。"
      />

      <div className="conn-cat">opencode</div>
      <RTKRow
        available={agents.rtk_available}
        value={agents.opencode_rtk}
        onChange={(v) => updateAgents({ opencode_rtk: v })}
      />
      {ocweb && ocweb.available && (
        <div className="ds-row ds-row-wrap">
          <span className="ds-label">Web UI</span>
          <OnOff value={ocweb.enabled} onChange={(v) => updateOcweb({ enabled: v })} />
          {ocweb.enabled &&
            (ocweb.running ? (
              <button className="ghost" onClick={() => window.open(ocwebURL(), "_blank", "noopener")}>
                開く ↗
              </button>
            ) : (
              <span className="muted">起動中…</span>
            ))}
          <span className="ds-hint">
            ブラウザ版 opencode（pk-opencode-webui）。オンにすると opencode serve とともに起動し、「開く」で
            新しいタブに表示します。全セッションを Web UI で扱えます。
          </span>
        </div>
      )}
    </div>
  );
}

function RTKRow({ available, value, onChange, note }) {
  return (
    <div className="ds-row ds-row-wrap">
      <span className="ds-label">RTK（トークン節約）</span>
      {available ? (
        <OnOff value={value} onChange={onChange} />
      ) : (
        <span className="muted">この workspace に rtk がありません</span>
      )}
      {note && <span className="ds-hint">{note}</span>}
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
