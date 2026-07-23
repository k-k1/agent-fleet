import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { api, apiJSON, getTenant } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { OnOff, Row } from "./controls.tsx";
import { useT } from "../../lib/i18n/index.ts";

// EnvTab (ツールチェーン) selects the workspace toolchains: timezone, node (via nvm),
// go, and java (a pre-baked Temurin JDK), plus the read-only bundled-tool versions and
// the agent-CLI self-update opt-in. Reads/writes via the Agent, so the workspace must
// be running. Changes apply to sessions/shells started AFTER the change (the Agent
// injects the selection at launch); already-running ones and the agent process itself
// pick it up on the next Stop → Start. The destructive lifecycle actions (recreate /
// clean home) live in their own 危険な操作 tab (DangerTab).
export function EnvTab() {
  const tr = useT();
  const toast = useToast();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const [d, setD] = useState<any>(null);
  const [err, setErr] = useState("");
  // CP-owned per-workspace settings: the agent CLI self-update opt-in + its operator
  // gate. DB-backed, so it loads/saves whether the workspace is running or stopped.
  const [au, setAu] = useState<any>(null); // { agentUpdate, allowAgentUpdate } | null

  // Cache the last good toolchains payload per tenant, so while the workspace is
  // stopped (Agent unreachable) we can still SHOW the form — disabled — from the
  // last-known values, instead of only an error. Options are static/host-baked, so
  // a cached copy stays accurate across a Stop → Start.
  const cacheKey = "af.toolchains." + getTenant();
  const load = useCallback(() => {
    setErr("");
    api("api/env/toolchains")
      .then((res) => {
        if (res && res.error) throw new Error(res.error.message || "");
        setD(res);
        try {
          localStorage.setItem(cacheKey, JSON.stringify(res));
        } catch {
          /* storage unavailable — just don't cache */
        }
      })
      .catch(() => {
        let cached = null;
        try {
          cached = JSON.parse(localStorage.getItem(cacheKey) || "null");
        } catch {
          /* ignore */
        }
        setD(cached);
        if (!cached) setErr(tr("env.load_ws_stopped"));
      });
  }, [cacheKey, tr]);
  useEffect(load, [load]);

  useEffect(() => {
    api("api/env/ws-settings")
      .then((res) => setAu(res && !res.error ? res : { agentUpdate: false, allowAgentUpdate: false }))
      .catch(() => setAu({ agentUpdate: false, allowAgentUpdate: false }));
  }, []);

  const setAgentUpdate = async (on: boolean) => {
    const res = await apiJSON("api/env/ws-settings", "PUT", { agentUpdate: on });
    if (res && !res.error) setAu(res);
    else toast(tr("common.save_failed"));
  };

  const update = async (patch: Record<string, string>) => {
    const next = {
      node: d.node || "",
      java: d.java || "",
      go: d.go || "",
      timezone: d.timezone || "",
      ...patch,
    };
    const res = await apiJSON("api/env/toolchains", "PUT", next);
    if (res && res.error) {
      toast(tr("env.save_failed_msg", { msg: res.error.message || "" }));
      return;
    }
    setD(res);
  };

  return (
    <div className="display-settings">
      <HostUpdateSection />
      {d ? (
        <Toolchains d={d} update={update} running={running} />
      ) : err ? (
        <p className="muted pad">{err}</p>
      ) : (
        <p className="muted pad">{tr("common.loading")}</p>
      )}
      {au && au.allowAgentUpdate && <AgentUpdateRow au={au} onChange={setAgentUpdate} />}
      <ToolVersions running={running} />
    </div>
  );
}

// HostUpdateSection surfaces the native host self-update (docs/42). GET
// /api/update/status is native-only; on any other deployment (Docker/ECS, dev)
// the CP does not register the route, api() returns an http_404 error, and this
// renders nothing. When a newer version has been staged on disk (by `af update`
// / the daily timer) the running control-plane still serves the OLD version
// until restarted — so we offer a "restart to apply" that warns how many live
// sessions the restart would interrupt before it fires.
function HostUpdateSection() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [st, setSt] = useState<any>(null); // { current, installed, restartRequired, systemd } | null
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api("api/update/status")
      .then((res) => setSt(res && !res.error ? res : null))
      .catch(() => setSt(null));
  }, []);

  if (!st) return null; // non-native deployment (or status unavailable)

  const apply = async () => {
    const running = useSessionsStore.getState().sessions.filter((s) => s.alive).length;
    const ok = await askConfirm({
      title: tr("env.update_apply_title", { v: st.installed }),
      body: running > 0 ? tr("env.update_apply_warn", { n: running }) : tr("env.update_apply_confirm"),
      confirmLabel: tr("env.update_apply_cta"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    const res = await apiJSON("api/update/apply", "POST", {});
    if (res && res.error) {
      toast(tr("env.update_apply_failed"));
      setBusy(false);
      return;
    }
    // The service is restarting; the CP connection will drop and reconnect on the
    // new version. Leave the button disabled and let the reconnect refresh state.
    toast(tr("env.update_applying"));
  };

  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("env.update_title")}</h4>
      <Row label={tr("env.update_current")}>
        <span className="muted">v{st.current}</span>
      </Row>
      {st.restartRequired ? (
        <>
          <Row label={tr("env.update_staged")}>
            <span>
              v{st.installed} <span className="tool-ver-badge">{tr("env.update_ready")}</span>
            </span>
          </Row>
          <Row label={tr("env.update_apply_row")}>
            <button className="danger-btn" disabled={busy} onClick={apply}>
              {busy ? tr("env.update_applying") : tr("env.update_apply_cta")}
            </button>
          </Row>
          <p className="muted ds-sub">{tr("env.update_apply_note")}</p>
        </>
      ) : (
        <p className="muted ds-sub">{tr("env.update_uptodate")}</p>
      )}
    </section>
  );
}

// ToolVersions: バンドルツール（claude / opencode / codex / rtk / gh / go / node /
// python）の版を「実効（PATH 解決）/ イメージ焼き込み / ~/.local override」の 3 観点で
// 表示する read-only セクション。PATH は ~/.local/bin が優先なので実効≠イメージが
// 起こり得る（override バッジ）。イメージ列にはビルド時ピンとのずれも出す（自己更新
// opt-in で上がった場合など）。Agent 経由なので workspace が起動中のときだけ取れる。
function ToolVersions({ running }: { running: boolean }) {
  const tr = useT();
  const [tv, setTv] = useState<any>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(
    (refresh = false) => {
      if (!running) return;
      setBusy(true);
      api("api/env/tool-versions" + (refresh ? "?refresh=1" : ""))
        .then((res) => setTv(res && !res.error ? res : null))
        .catch(() => setTv(null))
        .finally(() => setBusy(false));
    },
    [running],
  );
  useEffect(() => load(), [load]);

  const cell = (bin: any) => {
    if (!bin) return <span className="muted">—</span>;
    return <span title={bin.path + (bin.raw ? "\n" + bin.raw : "")}>{bin.version || bin.raw || "?"}</span>;
  };

  return (
    <section className="ds-group">
      <h4 className="ds-title">
        {tr("env.tool_versions")}
        {running && (
          <button className="tool-ver-reload" disabled={busy} onClick={() => load(true)}>
            {busy ? tr("env.fetching") : tr("env.refetch")}
          </button>
        )}
      </h4>
      {!running ? (
        <p className="muted ds-sub">{tr("env.tv_ws_stopped")}</p>
      ) : !tv ? (
        <p className="muted ds-sub">{busy ? tr("common.loading") : tr("env.fetch_failed")}</p>
      ) : (
        <table className="tool-ver">
          <thead>
            <tr>
              <th>{tr("env.th_tool")}</th>
              <th>{tr("env.th_effective")}</th>
              <th>{tr("env.th_image")}</th>
              <th>~/.local</th>
            </tr>
          </thead>
          <tbody>
            {(tv.tools || []).map((t: any) => (
              <tr key={t.name}>
                <td className="tool-ver-name">{t.name}</td>
                <td>
                  {cell(t.effective)}
                  {t.overridden && (
                    <span className="tool-ver-badge" title={tr("env.override_title")}>
                      override
                    </span>
                  )}
                </td>
                <td>
                  {cell(t.baked)}
                  {t.pin && t.baked && t.baked.version !== t.pin && (
                    <span className="tool-ver-pin" title={tr("env.pin_title")}>
                      {tr("env.pin_label", { pin: t.pin })}
                    </span>
                  )}
                </td>
                <td>{cell(t.userLocal)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p className="muted ds-sub">{tr("env.tv_note")}</p>
    </section>
  );
}

function Toolchains({ d, update, running }: { d: any; update: (patch: Record<string, string>) => void; running: boolean }) {
  const tr = useT();
  const nodeOpts: string[] = d.node_options || ["system"];
  const javaOpts: string[] = d.java_available || [];
  const goOpts: string[] = d.go_options || ["system"];
  const tz = d.timezone || "Asia/Tokyo";
  const tzOpts: string[] = d.tz_options && d.tz_options.length ? d.tz_options : [tz];
  const tzList = tzOpts.includes(tz) ? tzOpts : [tz, ...tzOpts];

  return (
    <>
      <p className="muted ds-note">
        {tr("env.tc_note_1")}
        <strong>{tr("env.tc_note_strong")}</strong>
        {tr("env.tc_note_2")}
      </p>
      {!running && <p className="muted ds-note">{tr("env.tc_ws_stopped")}</p>}
      <Row label={tr("env.timezone")}>
        <select value={tz} disabled={!running} onChange={(e) => update({ timezone: e.target.value })}>
          {tzList.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      </Row>
      <Row label="Node.js">
        <select value={d.node || "system"} disabled={!running} onChange={(e) => update({ node: e.target.value })}>
          {nodeOpts.map((v) => (
            <option key={v} value={v}>
              {v === "system" ? tr("env.node_default") : "v" + v}
            </option>
          ))}
        </select>
      </Row>
      <Row label="Go (GOROOT)">
        <select value={d.go || "system"} disabled={!running} onChange={(e) => update({ go: e.target.value })}>
          {goOpts.map((v) => (
            <option key={v} value={v}>
              {v === "system" ? tr("env.go_default") : "go " + v}
            </option>
          ))}
        </select>
      </Row>
      <Row label="Java (JAVA_HOME)">
        {javaOpts.length === 0 ? (
          <span className="muted">{tr("env.no_jdk")}</span>
        ) : (
          <select value={d.java || ""} disabled={!running} onChange={(e) => update({ java: e.target.value })}>
            <option value="">{tr("env.unselected")}</option>
            {javaOpts.map((v) => (
              <option key={v} value={v}>
                Temurin {v}
              </option>
            ))}
          </select>
        )}
      </Row>
    </>
  );
}

// AgentUpdateRow is the CLI self-update opt-in. It's CP-owned (per-workspace, stored
// in the CP DB), so — unlike the toolchains above — it can be toggled even while the
// workspace is STOPPED; the value is applied at the next container start. Shown only
// when the operator allows it (tenant policy).
function AgentUpdateRow({ au, onChange }: { au: any; onChange: (on: boolean) => void }) {
  const tr = useT();
  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("env.agent_update_title")}</h4>
      {/* segmented OnOff, like every other toggle in the modal (was a bare checkbox). */}
      <Row label={tr("env.agent_update_label")}>
        <OnOff value={!!au.agentUpdate} onChange={onChange} />
      </Row>
      <p className="muted ds-sub">{tr("env.agent_update_note")}</p>
    </section>
  );
}
