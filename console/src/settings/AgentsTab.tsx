import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, ocwebURL } from "../api.js";
import { useApp } from "../state.jsx";

// AgentsTab consolidates per-agent settings. claude: Remote Control / push
// notifications / RTK hook (settings.json, via api/claude/settings). codex &
// opencode: the RTK token-saving toggle (durable pref → on-disk artifacts, via
// api/agents/rtk). Reads/writes go through the Agent, so the workspace must be
// running; changes apply to NEW sessions of each agent.
export default function AgentsTab() {
  // opencode web state is shared with the WS bar via the app context, so toggling
  // here updates the bar's "open" entry immediately (and vice-versa).
  const { ocweb, setOcweb, refreshOcweb } = useApp();
  // API-driven config blobs; shapes are per-endpoint so kept loose. agents is
  // tri-state: null = loading, false = endpoint missing (old image), object = ready.
  const [claude, setClaude] = useState<any>(null);
  const [agents, setAgents] = useState<any>(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    setErr("");
    // claude/settings is the long-present baseline endpoint — only its failure
    // means the workspace/agent is unreachable. rtk + opencode-web are newer agent
    // routes that an older image may lack; their failure must NOT blank the whole
    // tab (it would hide the working Claude controls), so they load independently
    // and degrade in place.
    api("api/claude/settings")
      .then((c) => {
        if (c && c.error) setErr(c.error.message || "取得に失敗しました");
        else setClaude(c);
      })
      .catch(() => setErr("Workspace が起動しているか確認してください"));
    api("api/agents/rtk")
      .then((a) => setAgents(a && !a.error ? a : false))
      .catch(() => setAgents(false));
    refreshOcweb();
  }, [refreshOcweb]);
  useEffect(load, [load]);

  const updateClaude = async (patch: unknown) => {
    const d = await apiJSON("api/claude/settings", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setClaude(d);
  };
  const updateAgents = async (patch: unknown) => {
    const d = await apiJSON("api/agents/rtk", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setAgents(d);
  };
  const updateOcweb = async (patch: unknown) => {
    const d = await apiJSON("api/agents/opencode-web", "PUT", patch);
    if (d && d.error) {
      alert("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setOcweb(d);
  };

  if (err) return <p className="muted pad">{err}</p>;
  if (!claude) return <p className="muted pad">読み込み中…</p>;

  // agents tri-state: null = still loading (render nothing for the section yet,
  // so a supported image never flashes the "unsupported" message); false = the
  // rtk endpoint is missing (older image without the route) → degraded state that
  // points at the fix rather than hiding the sections; object = ready.
  const rtkStale = (
    <div className="ds-row ds-row-wrap">
      <span className="ds-hint">
        このワークスペースのイメージはエージェント設定 API（rtk / opencode web）に未対応です。
        イメージを再ビルドして「作り直す」と有効になります。
      </span>
    </div>
  );

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

      {agents === false ? (
        rtkStale
      ) : agents == null ? null : (
        <>
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
        </>
      )}
    </div>
  );
}

interface RTKRowProps {
  available?: boolean;
  value?: boolean;
  onChange: (v: boolean) => void;
  note?: string;
}

function RTKRow({ available, value, onChange, note }: RTKRowProps) {
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

function Row({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}

function OnOff({ value, onChange }: { value?: boolean; onChange: (v: boolean) => void }) {
  const opts: [boolean, string][] = [
    [true, "オン"],
    [false, "オフ"],
  ];
  return (
    <div className="seg choice-seg">
      {opts.map(([v, label]) => (
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
