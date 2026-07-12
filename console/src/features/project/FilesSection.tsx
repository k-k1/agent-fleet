// FilesSection — the rail-bottom global file browser. The primary tree stays
// rooted at "repos" (the working copies are the daily objects, one level deep),
// and a separate collapsed "home" disclosure below it lazily mounts a second
// tree rooted at home ("") for everything else the agent may browse (the
// backend denylists secrets). Section default collapsed (the trees are
// on-demand); a reveal request (a clone just landed, a repo row's
// フォルダを開く) opens the section so the target is visible — hence the
// controlled Section + own persistence (the old console's af-section-files key,
// so an existing collapse choice carries over).
import { useEffect, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useFilesStore } from "../files/store.ts";
import { useFilesFilter } from "./filesFilter.ts";
import { ProjectFiles } from "./ProjectFiles.tsx";
import { FilesChanges } from "./FilesChanges.tsx";

const KEY = "af-section-files";
const HOME_KEY = "af-files-home";
const VIEW_KEY = "af-files-view"; // the old console's tree/changes choice carries over

export function FilesSection() {
  const reveal = useFilesStore((s) => s.reveal);
  const bump = useFilesStore((s) => s.bump);
  const q = useFilesFilter((s) => s.q);
  const setQ = useFilesFilter((s) => s.setQ);
  const focusTree = useFilesFilter((s) => s.focusTree);
  const focusInputN = useFilesFilter((s) => s.focusInputN);
  const filterRef = useRef<HTMLInputElement>(null);
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

  useEffect(() => {
    if (reveal.path) {
      set(true);
      setView("tree"); // a reveal targets the tree — switch back so it's visible
    }
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

  return (
    <Section
      title="ファイル"
      icon="files"
      open={open}
      onToggle={() => set(!open)}
      actions={
        <>
          {/* Tree vs every working copy's git changes (cross-repo). */}
          <span className="ui-seg sm files-view">
            <button
              type="button"
              className={"seg-btn" + (view === "tree" ? " active" : "")}
              title="ツリー"
              onClick={() => setViewPersist("tree")}
            >
              <Icon name="list-tree" /> ツリー
            </button>
            <button
              type="button"
              className={"seg-btn" + (view === "changes" ? " active" : "")}
              title="変更ファイルのみ（全作業コピー）"
              onClick={() => setViewPersist("changes")}
            >
              <Icon name="git-compare" /> 変更
            </button>
          </span>
          <IconButton icon="refresh" label="更新" onClick={bump} />
        </>
      }
    >
      {view === "changes" ? (
        <FilesChanges />
      ) : (
        <>
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
                placeholder="絞り込み（ファイル）"
                aria-label="ファイルを絞り込み"
              />
              {q && (
                <button type="button" className="proj-filter-clear" title="クリア" onClick={() => setQ("")}>
                  <Icon name="close" />
                </button>
              )}
            </div>
          </div>
          <ProjectFiles root="repos" markRepos />
          {/* home: the rest of ~ (repos/ shows again inside — harmless). Lazy:
              mounted only while open. */}
          <button
            type="button"
            className="files-home-btn"
            onClick={() => setHome(!homeOpen)}
            aria-expanded={homeOpen}
            title={homeOpen ? "home を折りたたむ" : "home を展開（~ 全体をブラウズ）"}
          >
            <Icon name={homeOpen ? "chevron-down" : "chevron-right"} />
            <Icon name="vm" /> home
          </button>
          {homeOpen && <ProjectFiles root="" />}
        </>
      )}
    </Section>
  );
}
