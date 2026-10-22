import { useCallback, useState } from "react";
import { api, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";

// Mirrors mcpsrv.CallRecord (control-plane/internal/mcpsrv/mcp_calls.go).
interface CallItem {
  method: string;
  name?: string;
  era: string;
  meta_version?: string;
  client_name?: string;
  client_version?: string;
  status: number;
  notification?: boolean;
  error_code?: number;
  error_message?: string;
  tools?: number;
  tool_error?: boolean;
}
interface Call {
  at: string;
  http_method: string;
  status: number;
  latency_ms: number;
  header_version?: string;
  mcp_method_header: boolean;
  mcp_name_header: boolean;
  accept?: string;
  content_type?: string;
  user_agent?: string;
  items: CallItem[];
  items_omitted?: number;
}

function summary(it: CallItem, tr: ReturnType<typeof useT>): string {
  let s = it.method || tr("tokens.calls_unparsed");
  if (it.name) s += ` (${it.name})`;
  if (it.tools !== undefined) s += ` → ${tr("tokens.calls_tools", { n: it.tools })}`;
  if (it.tool_error) s += ` ✖ ${tr("tokens.calls_tool_error")}`;
  if (it.error_code) s += ` ✖ ${it.error_code} ${it.error_message ?? ""}`;
  return s;
}

/** The recent /mcp requests of one token or connection, loaded when opened. The server keeps
 * them in memory per Control Plane replica, which the hint says. */
export function RecentCalls({ url }: { url: string }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [calls, setCalls] = useState<Call[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const res = await api(url);
      if (res?.error) {
        setError(errText(res.error));
        return;
      }
      setCalls(res.calls ?? []);
      setError("");
    } catch {
      setError(tr("tokens.calls_load_failed"));
    }
  }, [url, tr]);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next) void load();
  };
  return (
    <div className="calls">
      <button type="button" className="btn-ghost" aria-expanded={open} onClick={toggle}>
        {open ? "▾ " : "▸ "}{tr("tokens.calls_toggle")}
      </button>
      {open && (
        <div className="calls-body">
          <p className="muted ds-note">
            {tr("tokens.calls_hint")}{" "}
            <button type="button" className="btn-ghost" onClick={() => void load()}>{tr("tokens.calls_refresh")}</button>
          </p>
          {error && <p role="alert">{error}</p>}
          {calls === null && !error && <p className="muted">{tr("tokens.calls_loading")}</p>}
          {calls && calls.length === 0 && !error && <p className="muted">{tr("tokens.calls_empty")}</p>}
          {calls && calls.length > 0 && (
            <div style={{ overflowX: "auto" }}>
              <table className="pat-table calls-table">
                <thead><tr>
                  <th>{tr("tokens.calls_time")}</th><th>{tr("tokens.calls_status")}</th>
                  <th>{tr("tokens.calls_rpc")}</th><th>{tr("tokens.calls_era")}</th>
                  <th>{tr("tokens.calls_client")}</th><th>{tr("tokens.calls_headers")}</th>
                  <th>{tr("tokens.calls_latency")}</th>
                </tr></thead>
                <tbody>{calls.map((c, i) => {
                  const first = c.items[0];
                  const era = first?.era ? `${first.era}${first.meta_version ? ` ${first.meta_version}` : ""}` : "";
                  const client = first?.client_name ? `${first.client_name}/${first.client_version ?? ""}` : c.user_agent ?? "";
                  return (
                    <tr key={`${c.at}-${i}`} className={c.status >= 400 || c.items.some((it) => it.tool_error) ? "calls-bad" : ""}>
                      <td>{new Date(c.at).toLocaleString()}</td>
                      <td>{c.http_method} {c.status}</td>
                      <td>{c.items.map((it, j) => <div key={j}>{summary(it, tr)}</div>)}
                        {c.items_omitted ? <div className="muted">+{c.items_omitted}</div> : null}</td>
                      <td>{era}</td>
                      <td>{client}{c.user_agent && c.user_agent !== client && <small className="muted" style={{ display: "block" }}>{c.user_agent}</small>}</td>
                      <td><small>
                        {tr("tokens.calls_hdr_version")}: {c.header_version || "—"}<br />
                        Mcp-Method: {c.mcp_method_header ? "✓" : "—"} · Mcp-Name: {c.mcp_name_header ? "✓" : "—"}<br />
                        Accept: {c.accept || "—"}
                      </small></td>
                      <td>{c.latency_ms} ms</td>
                    </tr>
                  );
                })}</tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
