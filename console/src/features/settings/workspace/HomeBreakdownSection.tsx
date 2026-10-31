import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { Button } from "../../../ui/Button.tsx";
import { humanSize } from "../../../lib/filemeta.ts";
import { fmtDateTime, relTime } from "../../../lib/intl.ts";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";
import { useSessionUI } from "../../sessions/ui.ts";
import { Row } from "../parts/controls.tsx";
import { PlaceLine, type Place } from "./machinePlace.tsx";

// The /cleanup/home answer (workspace/agent/home_usage.go).
interface HomePart extends Place {
  name: string;
  bytes: number;
  reclaimable?: boolean;
}
interface HomeCategory {
  key: string;
  bytes: number;
  files: number;
  reclaimable: number;
  parts?: HomePart[];
}
interface HomeRepo extends Place {
  name: string;
  bytes: number;
  regenerable: number;
  unjudged?: boolean;
}
interface HomeAfWork extends Place {
  name: string;
  bytes: number;
  modified?: string;
  marked?: boolean;
  orphan?: boolean;
}
export interface HomeUsage {
  total: number;
  files: number;
  categories: HomeCategory[];
  repos: HomeRepo[];
  af_work: HomeAfWork[];
  af_work_judged: boolean;
  truncated?: boolean;
  approximate?: boolean;
  unreadable?: number;
  measured_at: string;
  elapsed_ms: number;
}
interface HomeStatus {
  running: boolean;
  started_at?: string;
  scanned: number;
  error?: string;
  result: HomeUsage | null;
}

const POLL_MS = 2000;
/** Rows a list shows before "show all": the point is where most of it went. */
const TOP = 8;
/** Parts under a category, largest first; the rest are small enough not to matter here. */
const TOP_PARTS = 4;

// HomeBreakdownSection — where the whole home volume went, by category (#3083).
//
// The sections below it each answer for one cleanup; none says where a full home went, and
// on the home that prompted this the two biggest buckets (build output inside worktrees,
// ~/.gradle) were in none of them. Read-only: each category points at the cleanup that
// already exists for it, and says so when there is none yet.
//
// The walk runs in the Agent's background and can take minutes on a large home, so the
// button starts it and this polls; the Agent keeps the last result, which is what the
// section shows on opening — never a walk on its own.
export function HomeBreakdownSection({ own }: { own: boolean }) {
  const tr = useT();
  const [st, setSt] = useState<HomeStatus | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [failed, setFailed] = useState(false);
  const [allRepos, setAllRepos] = useState(false);
  const [allAfWork, setAllAfWork] = useState(false);
  const closeSettings = useSettingsUI((s) => s.closeSettings);
  const openCleanup = useSessionUI((s) => s.openCleanup);
  const alive = useRef(true);

  const read = useCallback(async (init?: RequestInit) => {
    try {
      const res = await api(init ? "api/cleanup/home/measure" : "api/cleanup/home", init);
      if (!alive.current) return;
      // An Agent older than the endpoint answers with an error.
      if (!res || res.error || typeof res.running !== "boolean") {
        setUnsupported(true);
        return;
      }
      setFailed(false);
      setSt(res);
    } catch {
      if (alive.current) setFailed(true);
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    read();
    return () => {
      alive.current = false;
    };
  }, [read]);

  const running = !!st?.running;
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => read(), POLL_MS);
    return () => clearInterval(id);
  }, [running, read]);

  if (unsupported) return null;

  const u = st?.result;
  const label = (key: string) => tMaybe("machine.home_cat_" + key) ?? key;
  const openTrash = () => {
    closeSettings();
    openCleanup();
  };
  const repos = u ? (allRepos ? u.repos : u.repos.slice(0, TOP)) : [];
  const afWork = u ? (allAfWork ? u.af_work : u.af_work.slice(0, TOP)) : [];
  const reclaimable = u ? u.categories.reduce((n, c) => n + c.reclaimable, 0) : 0;

  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("machine.home_title")}</h4>
      {failed && <p className="muted ds-note">{tr("machine.home_failed")}</p>}
      {st?.error && <p className="muted ds-note">{tr("machine.home_walk_failed", { error: st.error })}</p>}
      {u ? (
        <>
          <Row label={tr("machine.home_total")}>
            <span className="mv-val">
              {humanSize(u.total)}
              {reclaimable > 0 && (
                <span className="muted hb-reclaim">
                  {tr("machine.home_reclaimable", {
                    size: humanSize(reclaimable),
                  })}
                </span>
              )}
            </span>
          </Row>
          {u.categories
            .filter((c) => c.bytes > 0)
            .map((c) => (
              <div key={c.key} className="hb-cat" data-cat={c.key}>
                <Row label={label(c.key)}>
                  <span className="mv-val mv-size">{humanSize(c.bytes)}</span>
                  <span className="hb-bar" aria-hidden="true">
                    <span
                      style={{
                        width: (u.total > 0 ? (100 * c.bytes) / u.total : 0).toFixed(1) + "%",
                      }}
                    />
                  </span>
                  {c.key === "trash" && (
                    <Button small icon="trash" onClick={openTrash}>
                      {tr("machine.disk_open_cleanup")}
                    </Button>
                  )}
                </Row>
                {c.reclaimable > 0 && (
                  <p className="muted ds-note hb-hint">
                    {tr("machine.home_cat_reclaimable", {
                      size: humanSize(c.reclaimable),
                    })}{" "}
                    {tMaybe("machine.home_hint_" + c.key) ?? ""}
                  </p>
                )}
                {c.reclaimable === 0 && tMaybe("machine.home_none_" + c.key) && (
                  <p className="muted ds-note hb-hint">{tMaybe("machine.home_none_" + c.key)}</p>
                )}
                {(c.parts || [])
                  .filter((p) => p.bytes > 0)
                  .slice(0, TOP_PARTS)
                  .map((p) => (
                    <Row key={p.name} label={<span className="mv-sub">{p.name}</span>}>
                      <span className="mv-val mv-size">{humanSize(p.bytes)}</span>
                      <PlaceLine place={p} />
                    </Row>
                  ))}
              </div>
            ))}

          {u.repos.length > 0 && (
            <>
              <h5 className="hb-subtitle">{tr("machine.home_repos_title")}</h5>
              {repos.map((r) => (
                <Row key={r.name} label={<span className="mv-sub">{r.name}</span>}>
                  <span className="mv-val mv-size">{humanSize(r.bytes)}</span>
                  <span className="muted hb-regen">
                    {r.unjudged
                      ? tr("machine.home_repo_unjudged")
                      : tr("machine.home_repo_regen", {
                          size: humanSize(r.regenerable),
                        })}
                  </span>
                  <PlaceLine place={r} />
                </Row>
              ))}
              {u.repos.length > TOP && (
                <Button small onClick={() => setAllRepos(!allRepos)}>
                  {allRepos ? tr("machine.home_show_top") : tr("machine.home_show_all", { count: u.repos.length })}
                </Button>
              )}
            </>
          )}

          {u.af_work.length > 0 && (
            <>
              <h5 className="hb-subtitle">{tr("machine.home_afwork_title")}</h5>
              {afWork.map((f) => (
                <Row key={f.name} label={<span className="mv-sub">{f.name}</span>}>
                  <span className="mv-val mv-size">{humanSize(f.bytes)}</span>
                  <span className="muted hb-regen" title={f.modified ? fmtDateTime(f.modified) : undefined}>
                    {f.modified ? relTime(f.modified) : ""}
                    {f.orphan && <span className="hb-badge">{tr("machine.home_afwork_orphan")}</span>}
                    {!f.marked && <span className="hb-badge">{tr("machine.home_afwork_unmarked")}</span>}
                  </span>
                  <PlaceLine place={f} />
                </Row>
              ))}
              {u.af_work.length > TOP && (
                <Button small onClick={() => setAllAfWork(!allAfWork)}>
                  {allAfWork ? tr("machine.home_show_top") : tr("machine.home_show_all", { count: u.af_work.length })}
                </Button>
              )}
              {!u.af_work_judged && <p className="muted ds-note">{tr("machine.home_afwork_unjudged")}</p>}
            </>
          )}
          <p className="muted ds-note">
            {tr("machine.home_measured", {
              when: fmtDateTime(u.measured_at),
              secs: Math.round(u.elapsed_ms / 1000),
            })}
          </p>
          {u.truncated && <p className="muted ds-note">{tr("machine.disk_truncated")}</p>}
          {u.approximate && <p className="muted ds-note">{tr("machine.home_approximate")}</p>}
          {!!u.unreadable && <p className="muted ds-note">{tr("machine.home_unreadable", { count: u.unreadable })}</p>}
        </>
      ) : (
        st && !running && <p className="muted ds-note">{tr("machine.home_never")}</p>
      )}
      <div className="mv-actions">
        <Button small icon="refresh" onClick={() => read({ method: "POST" })} disabled={!st || running}>
          {running
            ? tr("machine.home_measuring", { count: st?.scanned ?? 0 })
            : u
              ? tr("machine.home_remeasure")
              : tr("machine.tool_measure")}
        </Button>
      </div>
      <p className="muted ds-note">{tr(own ? "machine.home_note_own" : "machine.home_note_shared")}</p>
    </section>
  );
}
