// ResultCards — the trial slot and the results grid (ADR 0081 decision 11).
//
// The point of both is that the studio is usable without the gallery: the gallery is for
// looking ACROSS folders and sessions, not for finding what one just made.
//
// The cards hold PATHS, not bytes, and render lazily with the same `downloadURL(path, 512)`
// the gallery and the mirror use — the same cache key, so the workspace does not decode a
// second thumbnail per picture just because a different pane asked.
import { downloadURL } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import type { Job, StoredFile } from "../api.ts";

/** The same longest edge the gallery and the mirror ask for. Do not "tune" it here. */
const THUMB = 512;

export interface ResultItem {
  file: StoredFile;
  job: Job;
}

const baseName = (p: string): string => p.split("/").filter(Boolean).pop() || p;

/** Every finished picture of the jobs on screen, newest first. */
export function resultsOf(jobs: Job[]): ResultItem[] {
  const out: ResultItem[] = [];
  for (const j of jobs) {
    if (j.state !== "done") continue;
    for (const f of j.files || []) if (f?.path) out.push({ file: f, job: j });
  }
  return out;
}

export function TrialSlot({
  item,
  onZoom,
  onUseSeed,
}: {
  item: ResultItem | null;
  onZoom: (path: string) => void;
  onUseSeed: (seed: number) => void;
}) {
  const tr = useT();
  const seed = item?.file.seed ?? item?.job.seed ?? null;
  return (
    <section className="igen-trial">
      <h3>{tr("imggen.trial_latest")}</h3>
      {!item ? (
        <p className="igen-hint">{tr("imggen.trial_none")}</p>
      ) : (
        <div className="igen-trial-body">
          <button
            type="button"
            className="igen-thumb"
            title={tr("imggen.zoom", { name: baseName(item.file.path) })}
            onClick={() => onZoom(item.file.path)}
          >
            <img src={downloadURL(item.file.path, THUMB)} alt={baseName(item.file.path)} loading="lazy" decoding="async" />
          </button>
          <div className="igen-trial-meta">
            {seed != null && <div className="igen-seed">seed {seed}</div>}
            {item.job.elapsed_ms != null && (
              <div className="igen-hint">{tr("imggen.elapsed", { sec: Math.round(item.job.elapsed_ms / 1000) })}</div>
            )}
            {(item.job.warnings || []).map((w) => (
              <div className="igen-warn" key={w}>
                <Icon name="warning" /> {w}
              </div>
            ))}
            {seed != null && (
              // The whole reason a trial is worth doing before a sweep: pin the composition,
              // then vary one axis (decision 11).
              <button type="button" className="ui-btn" onClick={() => onUseSeed(seed)}>
                {tr("imggen.use_seed")}
              </button>
            )}
          </div>
        </div>
      )}
    </section>
  );
}

export function ResultCards({
  items,
  onZoom,
  onAgain,
}: {
  items: ResultItem[];
  onZoom: (path: string) => void;
  onAgain: (item: ResultItem, sameSeed: boolean) => void;
}) {
  const tr = useT();
  return (
    <section className="igen-results">
      <h3>{tr("imggen.results")}</h3>
      {items.length === 0 ? (
        <p className="igen-hint">{tr("imggen.results_none")}</p>
      ) : (
        <div className="igen-grid-cards" role="list">
          {items.map(({ file, job }) => {
            const seed = file.seed ?? job.seed ?? null;
            return (
              <div className="igen-card" role="listitem" key={file.path}>
                <button
                  type="button"
                  className="igen-thumb"
                  title={tr("imggen.zoom", { name: baseName(file.path) })}
                  onClick={() => onZoom(file.path)}
                >
                  <img src={downloadURL(file.path, THUMB)} alt={baseName(file.path)} loading="lazy" decoding="async" />
                </button>
                <div className="igen-card-meta">
                  {seed != null && <span className="igen-seed">seed {seed}</span>}
                  <button
                    type="button"
                    className="ui-btn ui-btn-ghost"
                    disabled={seed == null}
                    onClick={() => onAgain({ file, job }, true)}
                  >
                    {tr("imggen.again_same_seed")}
                  </button>
                  <button type="button" className="ui-btn ui-btn-ghost" onClick={() => onAgain({ file, job }, false)}>
                    {tr("imggen.again_new_seed")}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
