import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { checkQuery, egressLevel, hostOf } from "./egressCheck.ts";
import type { EgressCheck } from "./egressCheck.ts";

// The egress warning + request flow shared by the member registry (McpTab) and the tenant
// distribution admin (AdminTab) — docs/log/48 §9. Both screens register a remote MCP server,
// and both need the same answer: can a workspace actually reach this host, and if not,
// what can this person do about it.
//
// The decision rules are in egressCheck.ts (unit tested); this file is the fetch and the
// rendering. Deliberately silent unless there is something true to say — see EgressLevel.

/**
 * useEgressCheck asks the CP about a set of hosts. Returns null until the first answer,
 * which reads as "nothing to say" everywhere — a warning must never be produced by a
 * failed or in-flight check.
 */
export function useEgressCheck(hosts: string[]): { check: EgressCheck | null; recheck: () => void } {
  const [check, setCheck] = useState<EgressCheck | null>(null);
  // Depend on the joined host list, not the array identity: callers rebuild it on every
  // render (it comes from a map over rows), and an array dep would refetch endlessly.
  const key = hosts.join(",");

  const recheck = useCallback(() => {
    if (!key) {
      setCheck(null);
      return;
    }
    api("api/egress/check?" + checkQuery(key.split(",")))
      .then((d) => setCheck(d && !d.error ? (d as EgressCheck) : null))
      .catch(() => setCheck(null));
  }, [key]);

  // Debounced: the host set changes on every keystroke while a URL is being typed, and
  // each partial host would otherwise be one request.
  useEffect(() => {
    const id = setTimeout(recheck, 300);
    return () => clearTimeout(id);
  }, [recheck]);
  return { check, recheck };
}

/**
 * EgressNote renders the warning for one MCP server URL, with the request form when the
 * member can act. `defaultReason` is prefilled into the reason box so the approving admin
 * gets context ("which MCP server is this for") without the member having to type it.
 */
export function EgressNote({
  url,
  check,
  defaultReason,
  onProposed,
}: {
  url?: string;
  check: EgressCheck | null;
  defaultReason?: string;
  onProposed: () => void;
}) {
  const tr = useT();
  const host = hostOf(url);
  const level = egressLevel(check, host);
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  if (level === "none") return null;
  if (level === "pending") {
    return <p className="ps-note egress-mcp-note">{tr("mcp.egress_pending", { host })}</p>;
  }

  const send = async () => {
    setBusy(true);
    setErr("");
    try {
      const d = await apiJSON("api/egress/propose", "POST", {
        entry: host,
        reason: reason.trim() || defaultReason || "",
      });
      if (d && d.error) {
        setErr(tr("mcp.egress_propose_failed", { msg: errText(d.error) }));
        return;
      }
      setOpen(false);
      // Re-check rather than assuming: the CP may answer "already active", in which case
      // the right outcome is the warning disappearing, not a "requested" message.
      onProposed();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="ps-note ps-note-warn egress-mcp-note">
      <div>{level === "blocked" ? tr("mcp.egress_blocked", { host }) : tr("mcp.egress_would_block", { host })}</div>
      {open ? (
        <div className="egress-mcp-ask">
          <input
            className="cinput"
            placeholder={tr("mcp.egress_reason_ph")}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            autoFocus
          />
          <button type="button" className="ghost xs mcp-btn" disabled={busy} onClick={() => void send()}>
            {tr("mcp.egress_send")}
          </button>
          <button type="button" className="ghost xs mcp-btn" disabled={busy} onClick={() => setOpen(false)}>
            {tr("common.cancel")}
          </button>
        </div>
      ) : (
        <button
          type="button"
          className="ghost xs mcp-btn"
          onClick={() => {
            // Prefill rather than send silently: the approving admin needs to know which
            // MCP server this is for, and the member should see (and be able to edit)
            // exactly what is being said on their behalf.
            setReason(defaultReason || "");
            setOpen(true);
          }}
        >
          {tr("mcp.egress_propose")}
        </button>
      )}
      {err && <div className="form-err">{err}</div>}
    </div>
  );
}
