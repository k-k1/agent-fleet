// FilesSection — the rail-bottom global file browser. The primary tree stays
// rooted at "repos" (the working copies are the daily objects, one level deep),
// and a separate collapsed "home" disclosure below it lazily mounts a second
// tree rooted at home ("") for everything else the agent may browse (the
// backend denylists secrets). Section default collapsed (the trees are
// on-demand); a reveal request (a clone just landed, a repo row's
// フォルダを開く) opens the section so the target is visible — hence the
// controlled Section + own persistence (the old console's af-section-files key,
// so an existing collapse choice carries over).
import { memo, useEffect, useLayoutEffect, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useFilesStore } from "../files/store.ts";
import { useFilesFilter } from "./filesFilter.ts";
import { ProjectFiles } from "./ProjectFiles.tsx";
import { FilesChanges } from "./FilesChanges.tsx";
import { useT } from "../../lib/i18n/index.ts";

const KEY = "af-section-files";
const HOME_KEY = "af-files-home";
const VIEW_KEY = "af-files-view"; // the old console's tree/changes choice carries over

export const FilesSection = memo(function FilesSection() {
  const tr = useT();
  const reveal = useFilesStore((s) => s.reveal);
  const bump = useFilesStore((s) => s.bump);
  const q = useFilesFilter((s) => s.q);
  const setQ = useFilesFilter((s) => s.setQ);
  const scope = useFilesFilter((s) => s.scope);
  const setScope = useFilesFilter((s) => s.setScope);
  const focusTree = useFilesFilter((s) => s.focusTree);
  const focusInputN = useFilesFilter((s) => s.focusInputN);
  const filterRef = useRef<HTMLInputElement>(null);
  const sectionBodyRef = useRef<HTMLDivElement>(null);
  const wasSearchingRef = useRef(false);
  const [open, setOpen] = useState(() => localStorage.getItem(KEY) === "1");
  const set = (v: boolean) => {
    setOpen(v);
    try {
      localStorage.setItem(KEY, v ? "1" : "0");
    } catch {}
  };
  const [homeOpen, setHomeOpen] = useState(() => localStorage.getItem(HOME_KEY) === "1");
  const setHome = (v: boolean) => {
    setHomeOpen(v);
    try {
      localStorage.setItem(HOME_KEY, v ? "1" : "0");
    } catch {}
  };
  const [view, setView] = useState(() => localStorage.getItem(VIEW_KEY) || "tree");
  const setViewPersist = (v: string) => {
    setView(v);
    try {
      localStorage.setItem(VIEW_KEY, v);
    } catch {}
  };
  const searchRoot = q.trim() && scope === "home" ? "" : "repos";

  useEffect(() => {
    const p = reveal.path;
    if (!p) return;
    set(true);
    setView("tree"); // a reveal targets the tree — switch back so it's visible
    // A leftover 絞り込み hides most of the tree (and, while it is a recursive search,
    // replaces it with a flat hit list and unmounts the home tree) — so the row we are
    // about to expand to could land somewhere nobody can see. Clearing it is what
    // "見せて" means.
    setQ("");
    // Only the repos tree is mounted unconditionally; everything else in home lives in
    // the collapsed home disclosure below it. Revealing e.g. ~/.local/share/x without
    // opening that would look like the click did nothing at all.
    if (p !== "repos" && !p.startsWith("repos/")) setHome(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal.n]);

  // Ctrl+F in the tree asks for the box: open the section + tree view (so the
  // box is mounted), then focus and select it.
  useEffect(() => {
    if (!focusInputN) return;
    set(true);
    setView("tree");
    requestAnimationFrame(() => filterRef.current?.select());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusInputN]);

  // Replacing a tall expanded tree with a short result list leaves the rail's
  // old scrollTop behind. Bring this section back to the viewport when a search
  // starts so its results do not end up below the visible area.
  useLayoutEffect(() => {
    const searching = !!q.trim();
    if (searching && !wasSearchingRef.current) {
      sectionBodyRef.current?.closest(".ui-section")?.scrollIntoView({ block: "start" });
    }
    wasSearchingRef.current = searching;
  }, [q]);

  return (
    <Section
      title={tr("pj.files")}
      icon="files"
      open={open}
      onToggle={() => set(!open)}
      actions={
        <>
          {/* Tree vs every working copy's git changes (cross-repo). */}
          <span className="ui-seg sm files-view sel-scope">
            <button
              type="button"
              className={"seg-btn" + (view === "tree" ? " active" : "")}
              title={tr("pj.tree")}
              onClick={() => setViewPersist("tree")}
            >
              <Icon name="list-tree" /> {tr("pj.tree")}
            </button>
            <button
              type="button"
              className={"seg-btn" + (view === "changes" ? " active" : "")}
              title={tr("pj.changes_only")}
              onClick={() => setViewPersist("changes")}
            >
              <Icon name="git-compare" /> {tr("pj.changes")}
            </button>
          </span>
          <IconButton icon="refresh" label={tr("pj.refresh")} onClick={bump} />
        </>
      }
    >
      {view === "changes" ? (
        <FilesChanges />
      ) : (
        <div ref={sectionBodyRef} className={q.trim() ? "files-search-active" : undefined}>
          <div className="proj-filter-bar">
            <div className="proj-filter">
              <Icon name="search" />
              <input
                ref={filterRef}
                value={q}
                onChange={(e) => setQ(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") setQ("");
                  // Enter hands focus to the tree (its keydown then drives selection).
                  else if (e.key === "Enter") {
                    e.preventDefault();
                    focusTree();
                  }
                }}
                placeholder={tr("pj.filter_files_ph")}
                aria-label={tr("pj.filter_files_aria")}
              />
              <span className="files-search-scope" role="group" aria-label={tr("pj.search_scope")}>
                <button
                  type="button"
                  className={scope === "repos" ? "active" : ""}
                  aria-pressed={scope === "repos"}
                  title={tr("pj.search_from_repos")}
                  aria-label={tr("pj.search_from_repos")}
                  onClick={() => setScope("repos")}
                >
                  <Icon name="root-folder" />
                </button>
                <button
                  type="button"
                  className={scope === "home" ? "active" : ""}
                  aria-pressed={scope === "home"}
                  title={tr("pj.search_from_home")}
                  aria-label={tr("pj.search_from_home")}
                  onClick={() => setScope("home")}
                >
                  <Icon name="vm" />
                </button>
              </span>
              {q && (
                <button type="button" className="proj-filter-clear" title={tr("pj.clear")} onClick={() => setQ("")}>
                  <Icon name="close" />
                </button>
              )}
            </div>
          </div>
          <ProjectFiles root={searchRoot} markRepos={searchRoot === "repos"} searchable groupByRepo={scope === "repos"} />
          {/* Browsing shortcut for home while the repos scope is selected. Lazy:
              mounted only while open and hidden during a recursive search. */}
          {!q && (
            <>
              <button
                type="button"
                className="files-home-btn"
                onClick={() => setHome(!homeOpen)}
                aria-expanded={homeOpen}
                title={homeOpen ? tr("pj.home_collapse") : tr("pj.home_expand")}
              >
                <Icon name={homeOpen ? "chevron-down" : "chevron-right"} />
                <Icon name="vm" /> home
              </button>
              {homeOpen && <ProjectFiles root="" secondary />}
            </>
          )}
        </div>
      )}
    </Section>
  );
});
