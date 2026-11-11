import { useState } from "react";
import { api, apiJSON } from "../../../core/api/client.ts";
import { Button } from "../../../ui/Button.tsx";
import { ConfirmDialog } from "../../../ui/ConfirmDialog.tsx";
import { humanSize } from "../../../lib/filemeta.ts";
import { fmtDateTime, relTime } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Row } from "../parts/controls.tsx";
import { PlaceLine, type Place } from "./machinePlace.tsx";

// The /cleanup/af-work answer (workspace/agent/leftovers_afwork_age.go).
interface AgeFolder extends Place {
  name: string;
  bytes: number;
  modified: string;
}

const DEFAULT_DAYS = 30;

// AfWorkAgeSection — unmarked ~/.af-work folders nothing owns and nothing touched for N days,
// one row each, removed only after a confirm that lists them. The list is a survey: the Agent
// judges every named folder again when the delete arrives (in use, changed, claimed by a
// session since), and a folder that no longer qualifies is skipped and counted, not removed.
export function AfWorkAgeSection() {
  const tr = useT();
  const [days, setDays] = useState(String(DEFAULT_DAYS));
  // The days the shown list was made for: a delete uses these, not whatever is typed now.
  const [listed, setListed] = useState<{ days: number; folders: AgeFolder[] } | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState(false);
  const [truncated, setTruncated] = useState(false);

  const n = Number(days);
  const valid = Number.isInteger(n) && n >= 1 && n <= 3650;

  // queryDays: a delete refreshes with the days it ran with, not what is typed now.
  const find = async (queryDays?: number) => {
    const q = queryDays ?? n;
    if (queryDays === undefined && !valid) return;
    if (queryDays === undefined) setMsg("");
    setBusy(true);
    setErr(false);
    try {
      const res = await api("api/cleanup/af-work?older_than_days=" + q);
      if (!res || res.error || !Array.isArray(res.folders)) throw new Error("");
      if (res.unsupported) {
        setListed(null);
        return;
      }
      setListed({ days: q, folders: res.folders });
      setPicked(new Set(res.folders.map((f: AgeFolder) => f.name)));
      setTruncated(!!res.truncated);
    } catch {
      setErr(true);
    } finally {
      setBusy(false);
    }
  };

  const chosen = listed?.folders.filter((f) => picked.has(f.name)) ?? [];
  const chosenBytes = chosen.reduce((s, f) => s + f.bytes, 0);

  const remove = async () => {
    if (!listed || chosen.length === 0) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/cleanup/af-work", "POST", {
        older_than_days: listed.days,
        names: chosen.map((f) => f.name),
      });
      if (res?.error) setMsg(tr("machine.afage_failed_delete") + (res.error.message ?? res.error.code));
      else {
        const removed: AgeFolder[] = res?.removed ?? [];
        let m = tr("machine.afage_deleted", {
          count: removed.length,
          size: humanSize(removed.reduce((s, f) => s + f.bytes, 0)),
        });
        if (res?.skipped?.length) m += " " + tr("machine.afage_skipped", { count: res.skipped.length });
        if (res?.failed) m += " " + tr("machine.left_partial") + res.failed;
        setMsg(m);
      }
    } catch {
      setMsg(tr("machine.afage_failed_delete"));
    } finally {
      setBusy(false);
      setConfirm(false);
    }
    await find(listed.days);
  };

  const toggle = (name: string) =>
    setPicked((p) => {
      const q = new Set(p);
      if (q.has(name)) q.delete(name);
      else q.add(name);
      return q;
    });

  return (
    <>
      <h5 className="hb-subtitle">{tr("machine.afage_title")}</h5>
      <Row label={tr("machine.afage_older_than")}>
        <input
          type="number"
          min={1}
          max={3650}
          value={days}
          aria-label={tr("machine.afage_older_than")}
          onChange={(e) => setDays(e.target.value)}
          style={{ width: 80 }}
        />
        <span className="muted">{tr("machine.afage_days")}</span>
        <Button small icon="search" onClick={() => find()} disabled={busy || !valid}>
          {tr("machine.afage_find")}
        </Button>
      </Row>
      {err && <p className="muted ds-note">{tr("machine.afage_failed")}</p>}
      {listed && listed.folders.length === 0 && <p className="muted ds-note">{tr("machine.afage_none", { days: listed.days })}</p>}
      {listed?.folders.map((f) => (
        <Row
          key={f.name}
          label={
            <label className="mv-sub">
              <input type="checkbox" checked={picked.has(f.name)} onChange={() => toggle(f.name)} disabled={busy} /> {f.name}
            </label>
          }
        >
          <span className="mv-val mv-size">{humanSize(f.bytes)}</span>
          <span className="muted hb-regen" title={fmtDateTime(f.modified)}>
            {relTime(f.modified)}
          </span>
          <PlaceLine place={f} />
        </Row>
      ))}
      {listed && listed.folders.length > 0 && (
        <div className="mv-actions">
          <Button small icon="trash" onClick={() => setConfirm(true)} disabled={busy || chosen.length === 0}>
            {tr("machine.afage_delete", { count: chosen.length })}
          </Button>
        </div>
      )}
      {msg && <p className="ds-note">{msg}</p>}
      {truncated && <p className="muted ds-note">{tr("machine.disk_truncated")}</p>}
      <p className="muted ds-note">{tr("machine.afage_note")}</p>
      {confirm && listed && (
        <ConfirmDialog
          title={tr("machine.afage_confirm_title", { count: chosen.length })}
          confirmLabel={tr("machine.left_delete")}
          busy={busy}
          onConfirm={remove}
          onCancel={() => setConfirm(false)}
        >
          <p>{tr("machine.afage_confirm_body", { days: listed.days, size: humanSize(chosenBytes) })}</p>
          <ul>
            {chosen.map((f) => (
              <li key={f.name}>
                <code>{f.path || "~/.af-work/" + f.name}</code> ({humanSize(f.bytes)})
              </li>
            ))}
          </ul>
        </ConfirmDialog>
      )}
    </>
  );
}
