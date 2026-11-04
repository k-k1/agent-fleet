import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { t as tr, tCount } from "../../lib/i18n/index.ts";
import { DATETIME_LOCALE_DEFAULT, fmtDateTime, TIME_LOCALE_DEFAULT } from "../../lib/intl.ts";
import { chronoInsertIndex } from "../mirror/handoffPlacement.ts";
import {
  cloudRunParts,
  cloudRunSummary,
  failedRun,
  parseCloudRuns,
  providerIcon,
  providerLabel,
  type CloudRun,
} from "./cloudRunsModel.ts";
import "./cloudRuns.css";

export { parseCloudRuns, placeRuns, toolKey, type CloudRun } from "./cloudRunsModel.ts";

/** The provider's brand icon, the same one the WS bar's badge uses, named for screen readers. */
function ProviderIcon({ provider }: { provider: string }) {
  const label = providerLabel(provider);
  return (
    <span className="cloud-run-provider" role="img" aria-label={label} title={label}>
      <Icon name={providerIcon(provider)} />
    </span>
  );
}

/** CloudRunLine is one run as a single line: Settings lists it with its session, and an expanded
 *  group lists it with its time. */
export function CloudRunLine({ run, session, time }: { run: CloudRun; session?: boolean; time?: boolean }) {
  return (
    <div className={"cloud-run" + (failedRun(run) ? " failed" : "")} role="note" title={tr("cloudruns.title")}>
      <ProviderIcon provider={run.provider} />
      {time && (
        <time className="cloud-run-time" dateTime={run.startedAt}>
          {fmtDateTime(run.startedAt, TIME_LOCALE_DEFAULT)}
        </time>
      )}
      {session && <span className="cloud-run-session">{run.session || tr("cloudruns.no_session")}</span>}
      <span className="cloud-run-text">{cloudRunParts(run).join(" · ")}</span>
    </div>
  );
}

/**
 * CloudRunGroup draws several runs as one line that opens to the runs one by one. The line names
 * the identity once and counts the runs; failures are counted apart in the error colour, so a
 * refused run shows without opening the group. One run is its own line: there is nothing to fold.
 * `attached` is the smaller form drawn under the tool call that started the runs.
 */
export function CloudRunGroup({ runs, attached = false }: { runs: CloudRun[]; attached?: boolean }) {
  const [open, setOpen] = useState(false);
  const cls = "cloud-run-group" + (attached ? " attached" : "");
  if (runs.length === 1) {
    return (
      <div className={cls}>
        <CloudRunLine run={runs[0]} time={attached} />
      </div>
    );
  }
  const { text, failed, providers } = cloudRunSummary(runs);
  return (
    <div className={cls + (open ? " open" : "")}>
      <button
        type="button"
        className={"cloud-run cloud-run-head" + (failed ? " failed" : "")}
        aria-expanded={open}
        title={open ? tr("cloudruns.collapse") : tr("cloudruns.expand")}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        {providers.map((p) => (
          <ProviderIcon key={p} provider={p} />
        ))}
        <span className="cloud-run-text">{text}</span>
        <span className="cloud-run-count">{tCount("cloudruns.runs_count", runs.length)}</span>
        {failed > 0 && <span className="cloud-run-failed">{tCount("cloudruns.failed_count", failed)}</span>}
      </button>
      {open && (
        <div className="cloud-run-list">
          {runs.map((r) => (
            <CloudRunLine key={r.id} run={r} time />
          ))}
        </div>
      )}
    </div>
  );
}

/** cloudRunCards places the runs no tool call took at their start time among the mirror's turns,
 *  one collapsed group per place: the runs of one turn that nothing attributed land together after
 *  its reply, and one line for them is enough. A conversation whose turns carry no timestamp at all
 *  (some kinds record none) cannot place a run in time; its runs go after the last turn. A finite
 *  time would land them at the top instead, because the placement reads a turn without a time as a
 *  just-sent echo. */
export function cloudRunCards(runs: CloudRun[], turnTimes: Array<string | undefined>): Array<{ at: number; node: ReactNode }> {
  const timed = turnTimes.some((t) => !!t && !Number.isNaN(Date.parse(t)));
  const slots = new Map<number, { at: number; runs: CloudRun[] }>();
  for (const r of runs) {
    const at = timed ? Date.parse(r.startedAt) : Number.POSITIVE_INFINITY;
    const slot = chronoInsertIndex(turnTimes, at);
    const s = slots.get(slot);
    if (s) s.runs.push(r);
    else slots.set(slot, { at, runs: [r] });
  }
  return [...slots.values()].map((s) => ({
    at: s.at,
    node: <CloudRunGroup key={"cloud-run-" + s.runs[0].id} runs={s.runs} />,
  }));
}

/** RecentCloudRuns is Settings' "Recent runs": the newest runs of every session, so a run
 *  whose session name was cleared or changed is still listed (ADR 0126 decision 2). `provider`
 *  keeps the rows of one cloud, for that cloud's tab. */
export function RecentCloudRuns({ provider }: { provider?: string } = {}) {
  const [runs, setRuns] = useState<CloudRun[] | null>(null);
  const [err, setErr] = useState("");
  const load = useCallback(() => {
    api("api/aws-runs")
      .then((d) => {
        if (!d || !Array.isArray(d.runs)) throw new Error(d?.error?.message || "unexpected response");
        const all = parseCloudRuns(d.runs);
        setRuns(provider ? all.filter((r) => r.provider === provider) : all);
        setErr("");
      })
      .catch((e: any) => setErr(String(e?.message || e)));
  }, [provider]);
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
              <time dateTime={r.startedAt}>{fmtDateTime(r.startedAt, DATETIME_LOCALE_DEFAULT)}</time>
              <CloudRunLine run={r} session />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
