import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { t as tr } from "../../lib/i18n/index.ts";
import { cloudRunParts, parseCloudRuns, type CloudRun } from "./cloudRunsModel.ts";
import "./cloudRuns.css";

export { parseCloudRuns, type CloudRun } from "./cloudRunsModel.ts";

/** CloudRunLine is one run as a single line: the mirror draws it between the turns, Settings
 *  lists it with its session. */
export function CloudRunLine({ run, session }: { run: CloudRun; session?: boolean }) {
  const failed = !!run.refused;
  return (
    <div className={"cloud-run" + (failed ? " failed" : "")} role="note" title={tr("cloudruns.title")}>
      <Icon name="cloud" />
      {session && <span className="cloud-run-session">{run.session || tr("cloudruns.no_session")}</span>}
      <span className="cloud-run-text">{cloudRunParts(run).join(" · ")}</span>
    </div>
  );
}

/** cloudRunCards places each run at its start time among the mirror's turns. A conversation
 *  whose turns carry no timestamp at all (some kinds record none) cannot place a run in time;
 *  its runs go after the last turn, in the order they started. A finite time would land them at
 *  the top instead, because the placement reads a turn without a time as a just-sent echo. */
export function cloudRunCards(runs: CloudRun[], turnTimes: Array<string | undefined>): Array<{ at: number; node: ReactNode }> {
  const timed = turnTimes.some((t) => !!t && !Number.isNaN(Date.parse(t)));
  return runs.map((r) => ({
    at: timed ? Date.parse(r.startedAt) : Number.POSITIVE_INFINITY,
    node: <CloudRunLine key={"cloud-run-" + r.id} run={r} />,
  }));
}

/** RecentCloudRuns is Settings' "Recent runs": the newest runs of every session, so a run
 *  whose session name was cleared or changed is still listed (ADR 0126 decision 2). */
export function RecentCloudRuns() {
  const [runs, setRuns] = useState<CloudRun[] | null>(null);
  const [err, setErr] = useState("");
  const load = useCallback(() => {
    api("api/aws-runs")
      .then((d) => {
        if (!d || !Array.isArray(d.runs)) throw new Error(d?.error?.message || "unexpected response");
        setRuns(parseCloudRuns(d.runs));
        setErr("");
      })
      .catch((e: any) => setErr(String(e?.message || e)));
  }, []);
  useEffect(load, [load]);
  return (
    <section className="cloud-runs-recent">
      <h3>
        {tr("cloudruns.recent")}
        <button type="button" className="ghost" title={tr("cloudruns.reload")} onClick={load}>
          <Icon name="refresh" />
        </button>
      </h3>
      <p className="field-help">{tr("cloudruns.recent_help")}</p>
      {err && <p className="field-error">{tr("cloudruns.load_failed", { msg: err })}</p>}
      {runs && runs.length === 0 && <p className="muted">{tr("cloudruns.recent_empty")}</p>}
      {runs && runs.length > 0 && (
        <ul className="cloud-runs-list">
          {runs.map((r) => (
            <li key={r.id}>
              <time dateTime={r.startedAt}>{new Date(r.startedAt).toLocaleString()}</time>
              <CloudRunLine run={r} session />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
