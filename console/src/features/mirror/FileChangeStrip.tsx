// FileChangeStrip —「変更ファイル」: the files this session's agent edited, folded into
// one collapsible strip directly under the mirror's head (docs/log/68 §68.5).
//
// It sits beside the ToDo checklist and deliberately copies its manners — the same
// disclosure, the same per-session remembered open/closed choice, the same re-key by
// session from the parent — because two panels in the same band that fold differently
// read as two unrelated features.
//
// What it is NOT: the rail's 変更 list and the SCM pane answer "what is dirty in this
// working copy". This answers "what did THIS session do", which is a different question
// whenever more than one session has passed through the same working copy.
import { useState } from "react";
import { api, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { Icon } from "../../ui/Icon.tsx";
import FileIcon from "../../ui/FileIcon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useFilesStore } from "../files/store.ts";
import { openRepoScm } from "../scm/open.ts";
import { DisclosureContent, readLS, writeLS } from "./transcript/blocks.tsx";
import {
  joinChanges,
  openRow,
  sortRows,
  stateBadge,
  type FileSort,
  type FsChange,
  type SessionFile,
} from "./sessionFiles.ts";

export function FileChangeStrip({ session, files }: { session: string; files: SessionFile[] }) {
  const tr = useT();
  const filesTick = useFilesStore((s) => s.tick);
  const [changes, setChanges] = useState<FsChange[]>([]);
  const [committed, setCommitted] = useState<string[]>([]);
  const [sort, setSort] = useState<FileSort>(() => (readLS("af.mirror-files-sort." + session) === "path" ? "path" : "recent"));
  const openKey = "af.mirror-files-open." + session;
  const sortKey = "af.mirror-files-sort." + session;
  const [open, setOpen] = useState(() => readLS(openKey) === "1");

  // The git side is fetched here rather than polled: it only changes when the agent
  // writes (which moves `files`), when the reader stages/discards (useFilesStore.bump),
  // or when they switch sessions. `newest` re-runs the load as soon as a new edit lands,
  // without adding a second timer that could show a different moment than the strip's own
  // list — the list itself rides the transcript poll.
  const newest = files.length ? files[0].lastIdx + ":" + files.length : "";
  useRetryLoad(
    async (signal) => {
      try {
        // Two questions, one load: what does the working tree say, and which of these
        // paths already went into a commit (docs/log/68 P2). The second only refines rows the
        // first has nothing to say about, so a failure there is not a failure of the strip.
        const [d, c] = await Promise.all([
          api("api/fs/changes"),
          api(`api/sessions/${encodeURIComponent(session)}/committed`).catch(() => null),
        ]);
        if (signal.aborted) return true;
        if (isTransientErr(d)) return false;
        setChanges(d.changes || []);
        setCommitted(Array.isArray(c?.committed) ? c.committed : []);
        return true;
      } catch {
        return false;
      }
    },
    [newest, filesTick, session],
  );

  // ⚠️ No empty state. A kind that records no edit coordinates at all (kiro / agy /
  // shell — docs/log/68 §68.2.1) would otherwise show a permanent「0 件」that is
  // indistinguishable from "this session really changed nothing".
  if (!files.length) return null;

  const rows = sortRows(joinChanges(files, changes, committed), sort);
  const added = rows.reduce((n, r) => n + (r.added || 0), 0);
  const removed = rows.reduce((n, r) => n + (r.removed || 0), 0);
  const lead = sortRows(rows, "recent")[0];
  const repo = rows.find((r) => r.repo)?.repo;

  return (
    <section className={"mirror-files mirror-disclosure" + (open ? " open" : "")}>
      <div className="mirror-files-head">
        <button
          type="button"
          className="mirror-files-toggle"
          aria-expanded={open}
          onClick={() => {
            const next = !open;
            setOpen(next);
            writeLS(openKey, next ? "1" : "0");
          }}
        >
          <Icon name="edit" />
          <span className="mfl-title">{tr("mirror.files.title")}</span>
          <span className="mfl-count muted">{rows.length}</span>
          {/* The summary names the file touched LAST even in path order — collapsed, the
              one thing worth a glance is "what was just changed". */}
          {lead && <span className="mfl-lead muted">{lead.name}</span>}
          {(added > 0 || removed > 0) && (
            <span className="mfl-stat">
              {added > 0 && <span className="dv-add">+{added}</span>}
              {removed > 0 && <span className="dv-del">−{removed}</span>}
            </span>
          )}
        </button>
        {open && (
          <>
            <button
              type="button"
              className="mfl-sort"
              onClick={() => {
                const next: FileSort = sort === "recent" ? "path" : "recent";
                setSort(next);
                writeLS(sortKey, next);
              }}
              title={tr(sort === "recent" ? "mirror.files.sort_to_path" : "mirror.files.sort_to_recent")}
            >
              <Icon name="sort-precedence" />
              {tr(sort === "recent" ? "mirror.files.sort_recent" : "mirror.files.sort_path")}
            </button>
            {repo && (
              <button type="button" className="mfl-scm" title={tr("mirror.files.open_scm")} onClick={() => openRepoScm(repo)}>
                <Icon name="source-control" />
              </button>
            )}
          </>
        )}
      </div>
      <DisclosureContent open={open} className="mirror-files-list-wrap">
        <ul className="mirror-files-list">
          {rows.map((r) => {
            const badge = stateBadge(r.state);
            const openable = !r.deleted || r.state === "unstaged" || r.state === "staged";
            return (
              <li key={r.path} className={"mfl-item mfl-" + r.state + (r.deleted ? " mfl-deleted" : "")}>
                <button
                  type="button"
                  className="mfl-row"
                  disabled={!openable}
                  title={r.path}
                  onClick={(e) => openRow(r, e.ctrlKey || e.metaKey)}
                >
                  <span className="mfl-ic">
                    <FileIcon name={r.name} />
                  </span>
                  <span className="mfl-name">{r.name}</span>
                  {r.dir && <span className="mfl-dir muted">{r.dir}</span>}
                  {r.sidechain && (
                    <span className="mfl-sub muted" title={tr("mirror.files.sidechain")}>
                      <Icon name="tools" />
                    </span>
                  )}
                  {/* Always rendered, even with nothing in it: this span carries the
                      margin-left:auto that pushes the badge to the right edge. Making it
                      conditional moved the badge of every count-less row (a delete, or a
                      kind that records no before/after) back against the file name. */}
                  <span className="mfl-stat">
                    {!!r.added && <span className="dv-add">+{r.added}</span>}
                    {!!r.removed && <span className="dv-del">−{r.removed}</span>}
                  </span>
                  <span className={"chg-badge " + badge.cls}>{tr(badge.label)}</span>
                </button>
              </li>
            );
          })}
        </ul>
      </DisclosureContent>
    </section>
  );
}
