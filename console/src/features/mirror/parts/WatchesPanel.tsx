import { useEffect, useRef, useState, type MouseEvent } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr, tCount } from "../../../lib/i18n/index.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import type { SessionWatch } from "../../../core/api/client.ts";
import { openWorkItemUrl } from "../../workitems/openUrl.ts";
import { elapsedShort } from "./BackgroundShellsPanel.tsx";

/** parseWatches keeps the well-formed entries of a messages payload's `watches`. */
export function parseWatches(v: unknown): SessionWatch[] {
  if (!Array.isArray(v)) return [];
  const str = (x: unknown) => (typeof x === "string" && x !== "" ? x : undefined);
  const out: SessionWatch[] = [];
  for (const w of v) {
    if (!w || typeof w !== "object") continue;
    const o = w as Record<string, unknown>;
    if (typeof o.id !== "string" || o.id === "" || typeof o.source !== "string") continue;
    out.push({
      id: o.id,
      source: o.source,
      ref: typeof o.ref === "string" ? o.ref : "",
      types: Array.isArray(o.types) ? o.types.filter((x): x is string => typeof x === "string") : [],
      ...(str(o.url) ? { url: str(o.url) } : {}),
      ...(str(o.due) ? { due: str(o.due) } : {}),
      ...(str(o.expires) ? { expires: str(o.expires) } : {}),
      ...(str(o.by) ? { by: str(o.by) } : {}),
      ...(str(o.error) ? { error: str(o.error) } : {}),
    });
  }
  return out;
}

/** sameWatches returns prev when next lists the same watches, so an unchanged poll does not
 *  re-render the panel. */
export function sameWatches(prev: SessionWatch[], next: SessionWatch[]): SessionWatch[] {
  return JSON.stringify(prev) === JSON.stringify(next) ? prev : next;
}

/** Only a pull request watch has a work item behind its link; a CI run has none. */
const isPullRequest = (source: string) => source === "github" || source === "bitbucket";

/** A plain click opens the work item detail modal, as every other ticket link does. Ctrl/Cmd, Shift
 *  and a non-primary button keep the anchor's own behaviour (the tracker page), and so does a URL
 *  that does not name a ticket: the click is never swallowed without something opening. */
function openPullRequest(e: MouseEvent<HTMLAnchorElement>, href: string) {
  if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
  if (openWorkItemUrl(href)) e.preventDefault();
}

/** safeWatchUrl passes only an https URL on github.com or bitbucket.org. A watch's `url` comes
 *  from a provider answer or an ext source, so anything else (another host, http:, javascript:,
 *  userinfo that makes a lookalike host) is dropped and the row stays plain text. */
export function safeWatchUrl(raw: string | undefined): string | undefined {
  if (!raw) return undefined;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return undefined;
  }
  if (u.protocol !== "https:" || u.username || u.password || u.port) return undefined;
  const host = u.hostname.toLowerCase();
  return host === "github.com" || host === "bitbucket.org" ? u.href : undefined;
}

/** sourceIcon names the codicon for a watch's source. */
export function sourceIcon(source: string): string {
  switch (source) {
    case "github":
    case "bitbucket":
      return "git-pull-request";
    case "time":
      return "clock";
    case "github_actions":
    case "bitbucket_pipelines":
      return "play-circle";
    default:
      return "plug";
  }
}

// The built-in event types in words. An unlisted type (every ext.<name> one) is shown as the
// text it is.
const TYPE_KEYS = {
  "pr.checks.completed": "mirror.watch_type_checks_completed",
  "pr.checks.failing": "mirror.watch_type_checks_failing",
  "pr.checks.failed": "mirror.watch_type_checks_failed",
  "pr.closed": "mirror.watch_type_closed",
  "pr.conflict": "mirror.watch_type_conflict",
  "pr.mergeable": "mirror.watch_type_mergeable",
  "pr.merged": "mirror.watch_type_merged",
  "pr.ready": "mirror.watch_type_ready",
  "pr.review.changes_requested": "mirror.watch_type_changes_requested",
  "pr.review.threads_unresolved": "mirror.watch_type_threads_unresolved",
  "ci.run.completed": "mirror.watch_type_run_completed",
  "ci.run.failed": "mirror.watch_type_run_failed",
  "time.fired": "mirror.watch_type_time",
} as const;

/** typeLabel words one event type. */
export function typeLabel(type: string): string {
  const key = (TYPE_KEYS as Record<string, (typeof TYPE_KEYS)[keyof typeof TYPE_KEYS]>)[type];
  return key ? tr(key) : type;
}

/** sourceLabel is the short name of what the watch looks at. An ext source keeps its own name. */
export function sourceLabel(source: string): string {
  switch (source) {
    case "github":
    case "bitbucket":
      return tr("mirror.watch_src_pr");
    case "time":
      return tr("mirror.watch_src_time");
    case "github_actions":
    case "bitbucket_pipelines":
      return tr("mirror.watch_src_run");
    default:
      return source;
  }
}

/** timeLeftMs is how long until a time watch fires, or null for one without a due time. */
export function timeLeftMs(w: SessionWatch, now: number): number | null {
  const at = Date.parse(w.due ?? "");
  return Number.isFinite(at) ? Math.max(0, at - now) : null;
}

// The panel's own clock, for the same reason as the shells panel's: an unchanged poll payload is
// not re-applied, so the time left would stop where it was first drawn.
const CLOCK_MS = 15 * 1000;

/** WatchesPanel lists the watches a session is waiting on, just above the composer (#3947), so
 *  a session idle on a PR's CI or a timer does not look like one that has stopped. `onEnd` is
 *  absent for a read-only viewer: the rows show, the button does not. */
export function WatchesPanel({
  watches,
  ending,
  onEnd,
  now: fixedNow,
}: {
  watches: SessionWatch[];
  ending: ReadonlySet<string>;
  onEnd?: (w: SessionWatch) => void;
  /** Pins the clock (tests). */
  now?: number;
}) {
  const askConfirm = useConfirm();
  // The dialog outlives the row: the watch may fire or end (or the panel unmount) while it is
  // open, and ending by id afterwards is a call for nothing.
  const live = useRef({ watches, mounted: true });
  live.current.watches = watches;
  useEffect(() => {
    live.current.mounted = true;
    return () => {
      live.current.mounted = false;
    };
  }, []);
  const [tick, setTick] = useState(() => Date.now());
  const has = watches.length > 0;
  const timed = watches.some((w) => w.due);
  useEffect(() => {
    if (!timed || fixedNow !== undefined) return;
    setTick(Date.now());
    const id = setInterval(() => setTick(Date.now()), CLOCK_MS);
    return () => clearInterval(id);
  }, [timed, fixedNow]);
  if (!has) return null;
  const now = fixedNow ?? tick;
  return (
    <div className="mirror-watches" role="status">
      <div className="mw-head">
        <Icon name="eye" />
        <span className="mw-msg">{tCount("mirror.watches", watches.length)}</span>
      </div>
      <ul className="mw-items">
        {watches.map((w) => {
          const left = timeLeftMs(w, now);
          const href = safeWatchUrl(w.url);
          const ref = w.ref || w.id;
          const busy = ending.has(w.id);
          return (
            <li className={"mw-item" + (w.error ? " failing" : "")} key={w.id}>
              <Icon name={sourceIcon(w.source)} title={sourceLabel(w.source)} />
              {href ? (
                <a
                  className="mw-ref"
                  href={href}
                  target="_blank"
                  rel="noopener noreferrer"
                  title={ref}
                  onClick={isPullRequest(w.source) ? (e) => openPullRequest(e, href) : undefined}
                >
                  {ref}
                </a>
              ) : (
                <span className="mw-ref" title={ref}>
                  {ref}
                </span>
              )}
              <span className="mw-meta">
                <span className="mw-types">{w.types.map(typeLabel).join(" · ")}</span>
                {left !== null && (
                  <span className="mw-left">{left > 0 ? tr("mirror.watch_in", { time: elapsedShort(left) }) : tr("mirror.watch_due")}</span>
                )}
                {w.by && <span className="mw-by">{tr("mirror.watch_by", { name: w.by })}</span>}
                {w.error && (
                  <span className="mw-warn" title={tr("mirror.watch_error_title", { error: w.error })}>
                    <Icon name="warning" />
                    <span className="mw-err">{w.error}</span>
                  </span>
                )}
              </span>
              {onEnd && (
                <button
                  type="button"
                  className="ghost mw-end"
                  disabled={busy}
                  title={tr("mirror.watch_end_title")}
                  onClick={() =>
                    void (async () => {
                      const ok = await askConfirm({
                        title: tr("mirror.watch_end_confirm_title"),
                        body: (
                          <>
                            <pre className="ui-confirm-code">{ref}</pre>
                            <p>{tr("mirror.watch_end_confirm_body")}</p>
                          </>
                        ),
                        confirmLabel: tr("mirror.watch_end"),
                        danger: true,
                      });
                      const cur = live.current;
                      // Gone before the member confirmed: it is ended, so there is nothing to call.
                      if (ok && cur.mounted && cur.watches.some((x) => x.id === w.id)) onEnd(w);
                    })()
                  }
                >
                  {busy ? tr("mirror.watch_ending") : tr("mirror.watch_end")}
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
