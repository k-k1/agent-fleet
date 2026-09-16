import { useCallback, useEffect, useRef, useState } from "react";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { api, apiJSON } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";

// Engine state vocabulary from ADR 0086 P1 M2 Agent HTTP API.
type DBState = "absent" | "installing" | "starting" | "running" | "stopped" | "error";

interface DBEngine {
  engine: string; // "postgres" | "mysql"
  major: string; // "17" | "8.4"
  installed: boolean;
  state: DBState;
  version: string;
  rssBytes: number;
  port: number;
  datadir: string;
  urlSocket: string;
  urlTcp: string;
  databases: Record<string, string>;
  lastUsedAt: string;
  lastError: string;
}

interface DBPayload {
  engines: DBEngine[];
}

function stateLabel(tr: (k: string) => string, state: DBState): string {
  switch (state) {
    case "absent":
      return tr("env.db_state_absent");
    case "installing":
      return tr("env.db_state_installing");
    case "starting":
      return tr("env.db_state_starting");
    case "running":
      return tr("env.db_state_running");
    case "stopped":
      return tr("env.db_state_stopped");
    case "error":
      return tr("env.db_state_error");
  }
}

function EngineRow({ eng, onRefresh }: { eng: DBEngine; onRefresh: () => void }) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [urlMode, setUrlMode] = useState<"socket" | "tcp">("socket");
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  const [stopMenu, setStopMenu] = useState(false);
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );

  // Poll while installing or starting (5 s interval).
  useEffect(() => {
    if (eng.state !== "installing" && eng.state !== "starting") return;
    const id = setTimeout(function tick() {
      if (!alive.current) return;
      onRefresh();
    }, 5000);
    return () => clearTimeout(id);
  }, [eng.state, onRefresh]);

  const doAction = async (action: string, query = "") => {
    setBusy(true);
    setStopMenu(false);
    const path = `api/env/databases/${eng.engine}/${action}${query ? "?" + query : ""}`;
    const res = await apiJSON(path, "POST", {});
    setBusy(false);
    if (!alive.current) return;
    if (res && res.error) {
      toast(tr("env.db_action_failed").replace("{msg}", res.error.message || ""));
      return;
    }
    onRefresh();
  };

  const handleReset = async () => {
    const ok = await askConfirm({
      title: tr("env.db_reset_confirm_title"),
      body: tr("env.db_reset_confirm_body"),
      confirmLabel: tr("env.db_reset_go"),
      danger: true,
    });
    if (!ok) return;
    doAction("reset");
  };

  const copyUrl = () => {
    const url = urlMode === "socket" ? eng.urlSocket : eng.urlTcp;
    navigator.clipboard.writeText(url).then(() => {
      setCopied(true);
      setTimeout(() => {
        if (alive.current) setCopied(false);
      }, 1500);
    });
  };

  const label = `${eng.engine === "postgres" ? "PostgreSQL" : "MySQL"} ${eng.major}`;
  const stateText = stateLabel(tr, eng.state);
  const rssText = eng.rssBytes > 0 ? tr("env.db_rss").replace("{n}", String(Math.round(eng.rssBytes / 1_000_000))) : "";
  const portText = eng.port > 0 ? tr("env.db_port").replace("{n}", String(eng.port)) : "";
  const url = urlMode === "socket" ? eng.urlSocket : eng.urlTcp;
  const busy2 = busy || eng.state === "installing" || eng.state === "starting";

  return (
    <div className="db-engine-row">
      <div className="db-engine-meta">
        <span className="db-engine-label">{label}</span>
        <span className={"db-engine-state db-state-" + eng.state}>{stateText}</span>
        {rssText && <span className="db-engine-rss muted">{rssText}</span>}
        {portText && <span className="db-engine-port muted">{portText}</span>}
      </div>

      {eng.lastError && <p className="db-engine-error">{eng.lastError}</p>}

      {(eng.state === "running" || eng.state === "stopped") && (
        <div className="db-engine-url">
          <span className="db-url-toggle">
            <button
              className={"db-url-mode" + (urlMode === "socket" ? " is-active" : "")}
              onClick={() => setUrlMode("socket")}
            >
              {tr("env.db_url_socket")}
            </button>
            <button
              className={"db-url-mode" + (urlMode === "tcp" ? " is-active" : "")}
              onClick={() => setUrlMode("tcp")}
            >
              {tr("env.db_url_tcp")}
            </button>
          </span>
          <code className="db-url-text">{url}</code>
          <button className="db-url-copy" onClick={copyUrl} title={tr("env.db_copy")}>
            {copied ? tr("env.db_copied") : tr("env.db_copy")}
          </button>
        </div>
      )}

      <div className="db-engine-actions">
        {(eng.state === "absent" || eng.state === "stopped" || eng.state === "error") && (
          <button className="db-btn" disabled={busy2} onClick={() => doAction("start")}>
            {eng.state === "absent" ? tr("env.db_start_absent") : tr("env.db_start")}
          </button>
        )}

        {eng.state === "running" && (
          <div className="db-stop-wrap">
            <button className="db-btn" disabled={busy2} onClick={() => setStopMenu((v) => !v)}>
              {tr("env.db_stop")} ▾
            </button>
            {stopMenu && (
              <div className="db-stop-menu">
                <button className="db-stop-item" onClick={() => doAction("stop")}>
                  {tr("env.db_stop")}
                </button>
                <button className="db-stop-item db-stop-purge" onClick={() => doAction("stop", "purge=1")}>
                  {tr("env.db_stop_purge")}
                </button>
              </div>
            )}
          </div>
        )}

        {(eng.state === "running" || eng.state === "stopped") && (
          <button className="db-btn db-btn-reset" disabled={busy2} onClick={handleReset}>
            {tr("env.db_reset")}
          </button>
        )}
      </div>
    </div>
  );
}

// EnvTabDatabases is the "Databases" card in the workspace settings Env tab.
// It shows the per-engine state — version, resident size, port, URL, and
// Start / Stop / Reset actions — proxied through the CP to the Agent.
// Requires the workspace to be running (the Agent owns the database processes).
export function EnvTabDatabases({ running }: { running: boolean }) {
  const tr = useT();
  const [data, setData] = useState<DBPayload | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    if (!running) return;
    setErr("");
    api("api/env/databases")
      .then((res) => {
        if (res && res.error) throw new Error(res.error.message || "");
        setData(res as DBPayload);
      })
      .catch((e: unknown) => {
        setErr(e instanceof Error ? e.message : "");
        setData(null);
      });
  }, [running]);

  useEffect(load, [load]);

  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("env.db_title")}</h4>
      {!running ? (
        <p className="muted ds-sub">{tr("env.db_ws_stopped")}</p>
      ) : err ? (
        <p className="muted ds-sub">{err}</p>
      ) : !data ? (
        <p className="muted ds-sub">{tr("common.loading")}</p>
      ) : (
        <div className="db-engine-list">
          {(data.engines || []).map((eng) => (
            <EngineRow key={eng.engine + "-" + eng.major} eng={eng} onRefresh={load} />
          ))}
        </div>
      )}
    </section>
  );
}
