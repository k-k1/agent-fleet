import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { api, apiJSON, getTenant } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { OnOff, Row } from "./controls.tsx";
import { useHostUpdate } from "./hostUpdate.ts";
import { usePolling } from "./usePolling.ts";
import { useT } from "../../lib/i18n/index.ts";
import { pinDrift } from "../../lib/pinDrift.ts";

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

  // プレビュー用サブドメインの設定（docs/81）。同じ ws-settings に載るので、
  // 保存経路も応答の形も agentUpdate と同じ。
  const savePreview = async (patch: Record<string, unknown>) => {
    const res = await apiJSON("api/env/ws-settings", "PUT", patch);
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
        <Toolchains d={d} update={update} running={running} reload={load} />
      ) : err ? (
        <p className="muted pad">{err}</p>
      ) : (
        <p className="muted pad">{tr("common.loading")}</p>
      )}
      {au && au.allowAgentUpdate && <AgentUpdateRow au={au} onChange={setAgentUpdate} />}
      {au && au.previewDomain && <PreviewSection au={au} save={savePreview} />}
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
  const st = useHostUpdate(); // native-only; null on other deployments (or while loading)
  const [busy, setBusy] = useState(false);

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
// 起こり得る（override バッジ）。ビルド時ピンとのずれは方向つきで色分けする
// （ピンより古い＝warn / 新しい＝accent、pinDrift）。Agent 経由なので workspace が
// 起動中のときだけ取れる。
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

  // ピンずれバッジ（方向つき色分け）: ピンより古い＝warn（更新が届いていない・kiro の
  // 固着型）、新しい＝accent（自己更新などの前進・想定内）。一致/判定不能は出さない。
  const pinBadge = (bin: any, pin: string) => {
    const d = pinDrift(bin?.version, pin);
    if (d !== "behind" && d !== "ahead") return null;
    return (
      <span
        className={"tool-ver-pin is-" + d}
        title={tr(d === "behind" ? "env.pin_behind_title" : "env.pin_ahead_title")}
      >
        {tr("env.pin_label", { pin })}
      </span>
    );
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
                  {/* 実効とピンのずれ（lean/焼き込みどちらの variant でも出す。cursor の
                      sha 接尾辞付きピンなどの版形状差は pinDrift が吸収する） */}
                  {pinBadge(t.effective, t.pin)}
                </td>
                {/* ピンは versions.json 由来なので焼き込み実体が無くても出せる。lean
                    variant（BAKE_AGENT_CLIS=0）は /usr/local に CLI を焼かないので
                    baked=null になり、以前はこの列が「—」だけでピンが見えなかった。
                    焼き込み有り: 実体の版（ピンとズレていればバッジ併記）。
                    焼き込み無し + ピン有り: 実体が無いことを括弧付きの版で表す。 */}
                <td>
                  {!t.baked && t.pin ? (
                    <span className="tool-ver-pin-only" title={tr("env.pin_only_title")}>
                      {tr("env.pin_paren", { pin: t.pin })}
                    </span>
                  ) : (
                    <>
                      {cell(t.baked)}
                      {pinBadge(t.baked, t.pin)}
                    </>
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

function Toolchains({
  d,
  update,
  running,
  reload,
}: {
  d: any;
  update: (patch: Record<string, string>) => void;
  running: boolean;
  reload: () => void;
}) {
  const tr = useT();
  const nodeOpts: string[] = d.node_options || ["system"];
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
      <JavaRow d={d} update={update} running={running} reload={reload} />
    </>
  );
}

// Java is the one toolchain whose picker offers versions that are not on disk yet:
// java_available is "installed ∪ installable" (agent jdk.go), because on ECS
// /usr/lib/jvm is empty and EVERY JDK has to be downloaded into the home volume. Until
// now, selecting an absent major only wrote the choice — the download happened at the
// next container start, so nothing changed in the running workspace and the member had
// to Stop → Start (or run `workspace-agent install-jdk` in a terminal) to get a JDK.
// This row makes that one button: it downloads into the home volume now, and since the
// agent resolves JAVA_HOME by globbing the JDK dirs at each launch, the next session
// picks it up with no restart.
function JavaRow({
  d,
  update,
  running,
  reload,
}: {
  d: any;
  update: (patch: Record<string, string>) => void;
  running: boolean;
  reload: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [installing, setInstalling] = useState("");
  const javaOpts: string[] = d.java_available || [];
  const installed: string[] = d.java_installed || [];
  const selected: string = d.java || "";
  const needsInstall = !!selected && !installed.includes(selected);

  const install = async () => {
    if (!selected) return;
    setInstalling(selected);
    const finish = (msg?: string) => {
      setInstalling("");
      if (msg) toast(tr("env.java_install_failed", { msg }));
      reload(); // refresh java_installed either way
    };
    const res = await apiJSON("api/env/jdk-install", "POST", { major: selected });
    if (!res || res.error) {
      finish(res?.error?.message || "");
      return;
    }
    if (res.state === "done" || res.state === "error") {
      finish(res.state === "error" ? res.error || "" : undefined);
      return;
    }
    // A JDK is ~200MB, so the install runs in the background and we poll it.
    poll({
      deadlineMs: 20 * 60 * 1000,
      firstDelayMs: 4000,
      onExpire: () => finish(tr("env.java_install_timeout")),
      step: async () => {
        let p;
        try {
          p = await api("api/env/jdk-install");
        } catch {
          p = null;
        }
        if (p && p.state === "done") {
          finish();
          return { stop: true };
        }
        if (p && p.state === "error") {
          finish(p.error || "");
          return { stop: true };
        }
        return { stop: false, nextMs: 4000 };
      },
    });
  };

  return (
    <>
      <Row label="Java (JAVA_HOME)">
        {javaOpts.length === 0 ? (
          <span className="muted">{tr("env.no_jdk")}</span>
        ) : (
          <span className="env-java-pick">
            <select
              value={selected}
              disabled={!running || !!installing}
              onChange={(e) => update({ java: e.target.value })}
            >
              <option value="">{tr("env.unselected")}</option>
              {javaOpts.map((v) => (
                <option key={v} value={v}>
                  {installed.includes(v) ? `Temurin ${v}` : tr("env.java_opt_absent", { v })}
                </option>
              ))}
            </select>
            {needsInstall && (
              <button disabled={!running || !!installing} onClick={install}>
                {installing ? tr("env.java_installing") : tr("env.java_install")}
              </button>
            )}
          </span>
        )}
      </Row>
      {needsInstall && <p className="muted ds-sub">{tr("env.java_install_note", { v: selected })}</p>}
    </>
  );
}

// AgentUpdateRow is the CLI self-update opt-in. It's CP-owned (per-workspace, stored
// in the CP DB), so — unlike the toolchains above — it can be toggled even while the
// workspace is STOPPED; the value is applied at the next container start. Shown only
// when the operator allows it (tenant policy).
// PreviewSection: プレビュー用サブドメイン（docs/81）の Workspace 単位の設定。
// ホスト方式が無いデプロイ（AF_PREVIEW_DOMAIN 未設定）では previewDomain が空なので
// 呼び出し側ごと描画されない —— 「押しても何も起きない設定」を置かないため。
function PreviewSection({ au, save }: { au: any; save: (patch: Record<string, unknown>) => void }) {
  const tr = useT();
  const [ports, setPorts] = useState((au.previewPorts || []).join(", "));
  // 保存は入力欄を離れたときだけ。打鍵ごとに PUT すると、"3000, 80" のような
  // 打ちかけの状態がそのまま保存されて 80 番が許可される。
  const commitPorts = () => {
    const parsed = ports
      .split(/[\s,]+/)
      .map((v: string) => Number(v))
      .filter((n: number) => Number.isInteger(n) && n >= 1 && n <= 65535);
    save({ previewPorts: parsed });
  };
  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("env.preview_title")}</h4>
      <Row label={tr("env.preview_ports_label")}>
        <input
          className="ds-select"
          value={ports}
          onChange={(e) => setPorts(e.target.value)}
          onBlur={commitPorts}
          onKeyDown={(e) => e.key === "Enter" && commitPorts()}
          placeholder="3000, 8080"
          aria-label={tr("env.preview_ports_label")}
          spellCheck={false}
        />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_ports_note", { n: au.previewMaxPorts || 8 })}</p>
      <Row label={tr("env.preview_fixed_label")}>
        <OnOff value={!!au.previewFixedSlug} onChange={(on) => save({ previewFixedSlug: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_fixed_note")}</p>
      <Row label={tr("env.preview_public_label")}>
        <OnOff value={!!au.previewPublic} onChange={(on) => save({ previewPublic: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_public_note")}</p>
    </section>
  );
}

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
