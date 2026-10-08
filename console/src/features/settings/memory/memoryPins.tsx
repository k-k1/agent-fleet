// MemoryPinsPanel — the member's pins on AF-owned agent memory (#1703). A pinned memory always
// sits first in the described part of the index agents read at the start of work; agents have no
// tool to pin, so this is the only place the choice is made. The use count beside each row is
// what the index ranks the unpinned ones by.

import { useCallback, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import type { MemoryListed } from "./memoryTypes.ts";

export function MemoryPinsPanel({ reload, onChanged }: { reload: number; onChanged: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<MemoryListed[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [busy, setBusy] = useState("");

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api("api/agents/memory/entries/list");
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    setLoadErr(res?.error ? errText(res.error) : "");
    setRows(res?.error ? [] : (res?.entries ?? []));
    return true;
  }, []);
  useRetryLoad(load, [reload]);

  const where = (m: MemoryListed) => (m.scope === "user" ? tr("mem.af_scope_user") : (m.project?.display ?? ""));
  const key = (m: MemoryListed) => `${m.scope}/${m.project?.id ?? ""}/${m.name}`;

  const toggle = async (m: MemoryListed) => {
    const pinned = !m.pinned;
    setBusy(key(m));
    try {
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const res = await apiJSON(
        "api/agents/memory/entries/pin" + (m.project ? "?project=" + encodeURIComponent(m.project.id) : ""),
        "POST",
        { scope: m.scope, project: m.project?.id ?? "", name: m.name, pinned },
      );
      if (res?.error) {
        toast(errDetail(res.error));
      } else {
        toast(tr(pinned ? "mem.pin_done" : "mem.pin_undone", { name: m.name }), { kind: "success" });
      }
    } catch {
      toast(tr("mem.af_failed"));
    } finally {
      setBusy("");
      onChanged();
    }
  };

  return (
    <div className="mem-pins">
      <h4>{tr("mem.pin_title")}</h4>
      <p className="muted ds-hint">{tr("mem.pin_intro")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      <ul className="mem-pin-list">
        {rows === null ? (
          <li className="muted pad">{tr("common.loading")}</li>
        ) : rows.length === 0 ? (
          !loadErr && <li className="muted pad">{tr("mem.pin_empty")}</li>
        ) : (
          rows.map((m) => (
            <li key={key(m)} className="mem-pin-row">
              <span className="mem-af-name">{m.name}</span>
              <span className="mem-snap-what muted" title={m.description}>
                {where(m)}
                {" · "}
                {tr("mem.pin_uses", { n: m.uses })}
              </span>
              <button
                type="button"
                aria-pressed={m.pinned}
                className={m.pinned ? "active" : undefined}
                disabled={busy !== ""}
                onClick={() => void toggle(m)}
              >
                {tr(m.pinned ? "mem.pin_unpin" : "mem.pin_pin")}
              </button>
            </li>
          ))
        )}
      </ul>
    </div>
  );
}
