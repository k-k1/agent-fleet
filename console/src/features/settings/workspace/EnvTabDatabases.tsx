import { useCallback, useEffect, useRef, useState } from "react";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { api, apiJSON } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";

// Engine state vocabulary from ADR 0086 P1 M2 Agent HTTP API.
type DBState = "absent" | "installing" | "starting" | "running" | "stopped" | "error";

// One database per working copy (decision 3'); the URLs belong to the database,
// not to the engine, because the Agent cannot know which working copy is asking.
interface DBDatabase {
  name: string;
  dir: string;
  urlSocket: string;
  urlTcp: string;
}

interface DBEngine {
  engine: string; // "postgres" | "mysql"
  major: string; // "17" | "8.4"
  installed: boolean;
  state: DBState;
  version: string;
  rssBytes: number;
  port: number;
  datadir: string;
  databases: DBDatabase[]; // empty unless running
  lastUsedAt: string;
  lastError: string;
}

interface DBPayload {
  engines: DBEngine[];
}

// Mask :password@ in URLs so credentials are not shown in the clear; the raw value is
// still passed to clipboard.writeText so copy/paste works as expected.
function maskUrl(url: string): string {
  return url.replace(/:([^:@/]+)@/, ":••••@");
}

function stateLabel(tr: ReturnType<typeof useT>, state: DBState): string {
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
  const [copied, setCopied] = useState(""); // name of the database just copied
  const [busy, setBusy] = useState(false);
  const [stopMenu, setStopMenu] = useState(false);
  const alive = useRef(true);

  useEffect(() => {
    // Reset on remount so StrictMode's double-invoke does not leave alive permanently false.
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // Poll while installing or starting (5 s interval). setInterval keeps firing even when the
  // refreshed state is still installing/starting; the effect cleanup stops it on unmount or
  // when the state transitions away from those two values.
  useEffect(() => {
    if (eng.state !== "installing" && eng.state !== "starting") return;
    const id = setInterval(() => {
      if (!alive.current) return;
      onRefresh();
    }, 5000);
    return () => clearInterval(id);
  }, [eng.state, onRefresh]);

  const doAction = async (action: string, query = "") => {
    setBusy(true);
    setStopMenu(false);
    const path = `api/env/databases/${eng.engine}/${action}${query ? "?" + query : ""}`;
    const res = await apiJSON(path, "POST", {});
    setBusy(false);
    if (!alive.current) return;
    if (res && res.error) {
      toast(tr("env.db_action_failed", { msg: res.error.message || "" }));
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

  const handleStopPurge = async () => {
    const ok = await askConfirm({
      title: tr("env.db_purge_confirm_title"),
      body: tr("env.db_purge_confirm_body"),
      confirmLabel: tr("env.db_stop_purge"),
      danger: true,
    });
    if (!ok) return;
    doAction("stop", "purge=1");
  };

  const copyUrl = (db: DBDatabase) => {
    const raw = urlMode === "socket" ? db.urlSocket : db.urlTcp;
    if (!raw) return;
    navigator.clipboard.writeText(raw).then(() => {
      setCopied(db.name);
      setTimeout(() => {
        if (alive.current) setCopied("");
      }, 1500);
    });
  };

  const engineName = eng.engine === "postgres" ? "PostgreSQL" : "MySQL";
  const versionSuffix = eng.version && eng.version !== eng.major ? ` · ${eng.version}` : "";
  const label = `${engineName} ${eng.major}${versionSuffix}`;
  const stateText = stateLabel(tr, eng.state);
  const rssText = eng.rssBytes > 0 ? tr("env.db_rss", { n: Math.round(eng.rssBytes / 1_000_000) }) : "";
  const portText = eng.port > 0 ? tr("env.db_port", { n: eng.port }) : "";
  const dbs = eng.databases || [];
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

      {eng.state === "running" &&
        (dbs.length === 0 ? (
          <p className="muted db-engine-empty">{tr("env.db_none_yet")}</p>
        ) : (
          dbs.map((db) => (
            <div className="db-database" key={db.name}>
              <div className="db-database-meta">
                <code className="db-database-name">{db.name}</code>
                {db.dir && <span className="db-database-dir muted">{db.dir}</span>}
              </div>
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
                <code className="db-url-text">
                  {maskUrl(urlMode === "socket" ? db.urlSocket : db.urlTcp)}
                </code>
                <button
                  className="db-url-copy"
                  onClick={() => copyUrl(db)}
                  title={tr("env.db_copy")}
                >
                  {copied === db.name ? tr("env.db_copied") : tr("env.db_copy")}
                </button>
              </div>
            </div>
          ))
        ))}

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
                <button className="db-stop-item db-stop-purge" onClick={handleStopPurge}>
                  {tr("env.db_stop_purge")}
                </button>
              </div>
            )}
          </div>
        )}

        {eng.state === "running" && (
          <button className="db-btn db-btn-reset" disabled={busy2} onClick={handleReset}>
            {tr("env.db_reset")}
          </button>
        )}
      </div>
    </div>
  );
}

// EnvTabDatabases is the "Databases" card in the workspace settings Toolchains tab.
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
