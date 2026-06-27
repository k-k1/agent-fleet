import { useCallback, useEffect, useState } from "react";
import { api, apiJSON } from "../api.js";

// EnvTab selects the workspace toolchains: node (via nvm) and java (a pre-baked
// Temurin JDK). Reads/writes via the Agent, so the workspace must be running.
// Changes take effect on Stop → Start (the entrypoint applies them).
export default function EnvTab() {
  const [d, setD] = useState(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    setErr("");
    api("api/env/toolchains")
      .then((res) => {
        if (res && res.error) {
          setErr(res.error.message || "取得に失敗しました");
          setD(null);
        } else setD(res);
      })
      .catch(() => setErr("Workspace が起動しているか確認してください"));
  }, []);
  useEffect(load, [load]);

  const update = async (patch) => {
    const next = { node: d.node || "", java: d.java || "", ...patch };
    const res = await apiJSON("api/env/toolchains", "PUT", next);
    if (res && res.error) {
      alert("保存に失敗: " + (res.error.message || ""));
      return;
    }
    setD(res);
  };

  if (err) return <p className="muted pad">{err}</p>;
  if (!d) return <p className="muted pad">読み込み中…</p>;

  const nodeOpts = d.node_options || ["system"];
  const javaOpts = d.java_available || [];

  return (
    <div className="display-settings">
      <p className="muted ds-note">変更は Stop → Start（コンテナ再生成）で反映されます。</p>
      <Row label="Node.js">
        <select value={d.node || "system"} onChange={(e) => update({ node: e.target.value })}>
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
          <select value={d.java || ""} onChange={(e) => update({ java: e.target.value })}>
            <option value="">未選択</option>
            {javaOpts.map((v) => (
              <option key={v} value={v}>
                Temurin {v}
              </option>
            ))}
          </select>
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
