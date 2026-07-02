import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, getTenant } from "../api.js";
import { useApp } from "../state.jsx";
import ConfirmDialog from "../components/ConfirmDialog.jsx";
import Icon from "../components/Icon.jsx";

// EnvTab selects the workspace toolchains: node (via nvm) and java (a pre-baked
// Temurin JDK). Reads/writes via the Agent, so the workspace must be running.
// Changes apply to sessions/shells started AFTER the change (the Agent injects the
// selection at launch); already-running ones and the agent process itself pick it
// up on the next Stop → Start.
export default function EnvTab() {
  const { wsState } = useApp();
  const running = wsState === "running";
  const [d, setD] = useState(null);
  const [err, setErr] = useState("");
  // allowUpdate: the operator gate for member CLI self-update, read from the active
  // tenant's policy (GET /api/tenants). The toggle is only offered when allowed.
  const [allowUpdate, setAllowUpdate] = useState(false);

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
    api("api/tenants")
      .then((res) => {
        const ts = (res && res.tenants) || [];
        const sel = getTenant();
        // Single-membership users have no explicit selection (auto-selected).
        const t = ts.find((x) => x.slug === sel) || (ts.length === 1 ? ts[0] : null);
        setAllowUpdate(!!(t && t.allow_agent_self_update));
      })
      .catch(() => setAllowUpdate(false));
  }, []);

  const update = async (patch) => {
    const next = {
      node: d.node || "",
      java: d.java || "",
      timezone: d.timezone || "",
      agentUpdate: !!d.agentUpdate,
      ...patch,
    };
    const res = await apiJSON("api/env/toolchains", "PUT", next);
    if (res && res.error) {
      alert("保存に失敗: " + (res.error.message || ""));
      return;
    }
    setD(res);
  };

  return (
    <div className="display-settings">
      {d ? (
        <Toolchains d={d} update={update} allowUpdate={allowUpdate} running={running} />
      ) : err ? (
        <p className="muted pad">{err}</p>
      ) : (
        <p className="muted pad">読み込み中…</p>
      )}
      <WorkspaceDangerZone />
    </div>
  );
}

function Toolchains({ d, update, allowUpdate, running }) {
  const nodeOpts = d.node_options || ["system"];
  const javaOpts = d.java_available || [];
  const tz = d.timezone || "Asia/Tokyo";
  const tzOpts = d.tz_options && d.tz_options.length ? d.tz_options : [tz];
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
      {allowUpdate && (
        <Row label="エージェント CLI の更新">
          <label className="ds-check">
            <input
              type="checkbox"
              checked={!!d.agentUpdate}
              disabled={!running}
              onChange={(e) => update({ agentUpdate: e.target.checked })}
            />
            <span>起動時に claude / opencode / codex を最新へ更新する</span>
          </label>
          <p className="muted ds-sub">
            OFF（既定）はシステムが焼いたイメージ版で固定。ON にすると次の起動時に最新へ更新します（Stop → Start で反映／OFF に戻して再起動すればイメージ版へ戻ります）。
          </p>
        </Row>
      )}
    </>
  );
}

// WorkspaceDangerZone: the destructive "作り直す" is tucked away here — deep in
// 設定 > 環境, behind a warning dialog — rather than on the always-visible WS bar,
// since recreating discards sessions and cloned repos (logins/connections survive).
function WorkspaceDangerZone() {
  const { recreateWs } = useApp();
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

function Row({ label, children }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}
