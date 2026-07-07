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

// EnvTab selects the workspace toolchains: node (via nvm) and java (a pre-baked
// Temurin JDK). Reads/writes via the Agent, so the workspace must be running.
// Changes apply to sessions/shells started AFTER the change (the Agent injects the
// selection at launch); already-running ones and the agent process itself pick it
// up on the next Stop → Start.
export function EnvTab() {
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
        if (!cached) setErr("Workspace を起動すると編集できます");
      });
  }, [cacheKey]);
  useEffect(load, [load]);

  useEffect(() => {
    api("api/env/ws-settings")
      .then((res) => setAu(res && !res.error ? res : { agentUpdate: false, allowAgentUpdate: false }))
      .catch(() => setAu({ agentUpdate: false, allowAgentUpdate: false }));
  }, []);

  const setAgentUpdate = async (on: boolean) => {
    const res = await apiJSON("api/env/ws-settings", "PUT", { agentUpdate: on });
    if (res && !res.error) setAu(res);
    else toast("保存に失敗しました");
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
      toast("保存に失敗: " + (res.error.message || ""));
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
        <p className="muted pad">読み込み中…</p>
      )}
      {au && au.allowAgentUpdate && <AgentUpdateRow au={au} onChange={setAgentUpdate} />}
      <WorkspaceDangerZone />
    </div>
  );
}

function Toolchains({ d, update, running }: { d: any; update: (patch: Record<string, string>) => void; running: boolean }) {
  const nodeOpts: string[] = d.node_options || ["system"];
  const javaOpts: string[] = d.java_available || [];
  const tz = d.timezone || "Asia/Tokyo";
  const tzOpts: string[] = d.tz_options && d.tz_options.length ? d.tz_options : [tz];
  const tzList = tzOpts.includes(tz) ? tzOpts : [tz, ...tzOpts];

  return (
    <>
      <p className="muted ds-note">
        変更は<strong>この後に起動するセッション/シェル</strong>に反映されます（起動中のものと既存プロセスは Stop → Start で反映）。
      </p>
      {!running && (
        <p className="muted ds-note">
          Workspace が停止中です。起動すると変更できます。
        </p>
      )}
      <Row label="タイムゾーン (TZ)">
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
              {v === "system" ? "既定 (image の node)" : "v" + v}
            </option>
          ))}
        </select>
      </Row>
      <Row label="Java (JAVA_HOME)">
        {javaOpts.length === 0 ? (
          <span className="muted">この workspace に JDK がありません</span>
        ) : (
          <select value={d.java || ""} disabled={!running} onChange={(e) => update({ java: e.target.value })}>
            <option value="">未選択</option>
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
  return (
    <section className="ds-group">
      <h4 className="ds-title">エージェント CLI の更新</h4>
      <label className="ds-check">
        <input type="checkbox" checked={!!au.agentUpdate} onChange={(e) => onChange(e.target.checked)} />
        <span>起動時に claude / opencode / codex を最新へ更新する</span>
      </label>
      <p className="muted ds-sub">
        OFF（既定）はシステムが焼いたイメージ版で固定。ON にすると次の起動時に最新へ更新します（Stop → Start で反映／OFF に戻して再起動すればイメージ版へ戻ります）。停止中でも変更できます。
      </p>
    </section>
  );
}

// WorkspaceDangerZone: the destructive "作り直す" is tucked away here — deep in
// 設定 > 環境, behind a warning dialog — rather than on the always-visible WS bar,
// since recreating discards sessions and cloned repos (logins/connections survive).
function WorkspaceDangerZone() {
  const toast = useToast();
  // Recreate = reset the layout up front (everything the views point at is about
  // to go away; the terminal reconciler then disposes the other panes' xterms),
  // then tear down + refresh. Old state.tsx recreateWs, orchestrated here.
  const recreateWs = async () => {
    useLayoutStore.getState().resetToTerminal();
    const err = await useWorkspaceStore.getState().recreate();
    if (err) toast("作り直しに失敗: " + err);
    void useSessionsStore.getState().refresh();
    void useReposStore.getState().refresh();
    useFilesStore.getState().bump();
  };
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);

  const doRecreate = async () => {
    setBusy(true);
    try {
      await recreateWs();
      setConfirm(false);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="danger-zone">
      <h4 className="danger-zone-title">
        <Icon name="warning" /> 危険な操作
      </h4>
      <div className="danger-zone-row">
        <div className="danger-zone-text">
          <strong>Workspace を作り直す</strong>
          <span className="muted">コンテナを破棄し、最新イメージで再生成します（セッション・clone は失われます）。</span>
        </div>
        <button className="danger-btn" onClick={() => setConfirm(true)}>
          作り直す
        </button>
      </div>
      {confirm && (
        <ConfirmDialog
          title="Workspace を作り直しますか？"
          confirmLabel="作り直す"
          busy={busy}
          onConfirm={doRecreate}
          onCancel={() => setConfirm(false)}
        >
          <p>コンテナを破棄し、最新イメージで新しく作り直します。</p>
          <ul className="confirm-list">
            <li className="keep"><Icon name="check" /> ログイン・接続（GitHub / Bitbucket / Claude）は保持されます</li>
            <li className="lose"><Icon name="close" /> 実行中のセッションは失われます</li>
            <li className="lose"><Icon name="close" /> clone 済みリポジトリ（未コミット変更を含む）は削除されます</li>
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
