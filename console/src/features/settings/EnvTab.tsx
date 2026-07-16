import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import type { ReactNode } from "react";
import { api, apiJSON, getTenant } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";

// EnvTab selects the workspace toolchains: node (via nvm) and java (a pre-baked
// Temurin JDK). Reads/writes via the Agent, so the workspace must be running.
// Changes apply to sessions/shells started AFTER the change (the Agent injects the
// selection at launch); already-running ones and the agent process itself pick it
// up on the next Stop → Start.
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
      {d ? (
        <Toolchains d={d} update={update} running={running} />
      ) : err ? (
        <p className="muted pad">{err}</p>
      ) : (
        <p className="muted pad">{tr("common.loading")}</p>
      )}
      {au && au.allowAgentUpdate && <AgentUpdateRow au={au} onChange={setAgentUpdate} />}
      <ToolVersions running={running} />
      <WorkspaceDangerZone />
    </div>
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
      <label className="ds-check">
        <input type="checkbox" checked={!!au.agentUpdate} onChange={(e) => onChange(e.target.checked)} />
        <span>{tr("env.agent_update_label")}</span>
      </label>
      <p className="muted ds-sub">{tr("env.agent_update_note")}</p>
    </section>
  );
}

// WorkspaceDangerZone: the destructive "作り直す" is tucked away here — deep in
// 設定 > 環境, behind a warning dialog — rather than on the always-visible WS bar,
// since recreating discards sessions and cloned repos (logins/connections survive).
function WorkspaceDangerZone() {
  const tr = useT();
  const toast = useToast();
  // Recreate = reset the layout up front (everything the views point at is about
  // to go away; the terminal reconciler then disposes the other panes' xterms),
  // then tear down + refresh. Old state.tsx recreateWs, orchestrated here.
  // Both destructive actions share the same post-teardown refresh: everything the
  // views point at is about to go away (the terminal reconciler disposes the other
  // panes' xterms after resetToTerminal), then we refresh sessions/repos/files.
  const runDestructive = async (
    action: () => Promise<string | null>,
    failMsg: string,
  ) => {
    useLayoutStore.getState().resetToTerminal();
    const err = await action();
    if (err) toast(failMsg + err);
    void useSessionsStore.getState().refresh();
    void useReposStore.getState().refresh();
    useFilesStore.getState().bump();
  };
  const [confirm, setConfirm] = useState<null | "recreate" | "cleanHome">(null);
  const [busy, setBusy] = useState(false);

  const run = async (action: () => Promise<string | null>, failMsg: string) => {
    setBusy(true);
    try {
      await runDestructive(action, failMsg);
      setConfirm(null);
    } finally {
      setBusy(false);
    }
  };
  const doRecreate = () => run(() => useWorkspaceStore.getState().recreate(), tr("env.recreate_failed"));
  const doCleanHome = () => run(() => useWorkspaceStore.getState().cleanHome(), tr("env.cleanhome_failed"));

  return (
    <section className="danger-zone">
      <h4 className="danger-zone-title">
        <Icon name="warning" /> {tr("env.danger_zone")}
      </h4>
      <div className="danger-zone-row">
        <div className="danger-zone-text">
          <strong>{tr("env.recreate_head")}</strong>
          <span className="muted">
            {tr("env.recreate_desc_1")}
            <code>~/repos</code>
            {tr("env.recreate_desc_2")}
          </span>
        </div>
        <button className="danger-btn" onClick={() => setConfirm("recreate")}>
          {tr("env.recreate_btn")}
        </button>
      </div>
      <div className="danger-zone-row">
        <div className="danger-zone-text">
          <strong>{tr("env.cleanhome_head")}</strong>
          <span className="muted">
            {tr("env.cleanhome_desc_1")}
            <code>~/repos</code>・<code>~/.local</code>
            {tr("env.cleanhome_desc_2")}
          </span>
        </div>
        <button className="danger-btn" onClick={() => setConfirm("cleanHome")}>
          {tr("env.cleanhome_btn")}
        </button>
      </div>
      {confirm === "recreate" && (
        <ConfirmDialog
          title={tr("env.recreate_confirm_title")}
          confirmLabel={tr("env.recreate_btn")}
          busy={busy}
          onConfirm={doRecreate}
          onCancel={() => setConfirm(null)}
        >
          <p>{tr("env.recreate_confirm_body")}</p>
          <ul className="confirm-list">
            <li className="keep"><Icon name="check" /> {tr("env.dz_keep_login")}</li>
            <li className="keep"><Icon name="check" /> <code>~/repos</code>{tr("env.dz_keep_home_1")}<code>~/.local</code>{tr("env.dz_keep_home_2")}</li>
            <li className="lose"><Icon name="close" /> {tr("env.dz_lose_sessions")}</li>
            <li className="lose"><Icon name="close" /> {tr("env.dz_lose_repos")}</li>
          </ul>
        </ConfirmDialog>
      )}
      {confirm === "cleanHome" && (
        <ConfirmDialog
          title={tr("env.cleanhome_confirm_title")}
          confirmLabel={tr("env.cleanhome_btn")}
          busy={busy}
          onConfirm={doCleanHome}
          onCancel={() => setConfirm(null)}
        >
          <p>{tr("env.cleanhome_confirm_body")}</p>
          <ul className="confirm-list">
            <li className="keep"><Icon name="check" /> {tr("env.dz_keep_login")}</li>
            <li className="lose"><Icon name="close" /> {tr("env.dz_lose_sessions")}</li>
            <li className="lose"><Icon name="close" /> {tr("env.dz_lose_repos")}</li>
            <li className="lose"><Icon name="close" /> <code>~/.local</code>{tr("env.dz_lose_home_rest")}</li>
          </ul>
        </ConfirmDialog>
      )}
    </section>
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
