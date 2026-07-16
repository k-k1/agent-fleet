// DirPicker — browse the container's home tree and pick a directory to launch
// in. `value` is home-relative ("" = home); the CURRENT browsed path IS the
// selection. Base clones get a quick list at home (worktrees are omitted —
// they launch from their project-tree row, and folding them in buries the
// repos as tasks pile up) + a branch chip on any working copy's folder row.
// Port of the old components/DirPicker.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";

interface Entry {
  name: string;
  type: string;
}
export interface RepoLite {
  name: string;
  path?: string;
  branch?: string;
  worktree?: boolean; // linked git worktree (not a standalone clone)
}

export function DirPicker({
  value,
  onChange,
  repos = [],
}: {
  value: string;
  onChange: (p: string) => void;
  repos?: RepoLite[];
}) {
  const tr = useT();
  const [entries, setEntries] = useState<Entry[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState(false);
  const path = value; // home-relative; "" = home

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setErr(false);
    api(`api/fs/tree?path=${encodeURIComponent(path)}`)
      .then((d) => {
        if (!alive) return;
        setEntries((d.entries || []).filter((e: Entry) => e.type === "dir"));
      })
      .catch(() => alive && setErr(true))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [path]);

  const segs = path ? path.split("/") : [];
  const isHome = path === "";
  // Branch chips cover ALL working copies (worktrees included) so browsing into
  // repos/ still identifies them; only the home quick list hides worktrees.
  const repoBranch = new Map(repos.map((r) => [`repos/${r.name}`, r.branch || ""]));
  const baseRepos = repos.filter((r) => !r.worktree);
  const full = (name: string) => (path ? `${path}/${name}` : name);

  return (
    <div className="dirpick">
      <div className="dirpick-crumbs">
        <button type="button" className="dirpick-crumb" onClick={() => onChange("")} title={tr("rp.home")}>
          <Icon name="home" /> ~
        </button>
        {segs.map((s, i) => (
          <span key={i} className="dirpick-seg">
            <span className="dirpick-slash">/</span>
            <button type="button" className="dirpick-crumb" onClick={() => onChange(segs.slice(0, i + 1).join("/"))}>
              {s}
            </button>
          </span>
        ))}
      </div>
      <div className="dirpick-list">
        {isHome && baseRepos.length > 0 && (
          <>
            <div className="dirpick-head">{tr("rp.repositories")}</div>
            {baseRepos.map((r) => (
              <button
                key={"repo-" + r.name}
                type="button"
                className="dirpick-row"
                onClick={() => onChange(`repos/${r.name}`)}
              >
                <Icon name="repo" className="dirpick-ic" />
                <span className="dirpick-name">{r.name}</span>
                {r.branch && <span className="dirpick-branch">{r.branch}</span>}
                <Icon name="chevron-right" className="dirpick-go" />
              </button>
            ))}
            <div className="dirpick-head">{tr("rp.folders")}</div>
          </>
        )}
        {loading ? (
          <div className="dirpick-empty">
            <Icon name="loading" spin /> {tr("rp.loading")}
          </div>
        ) : err ? (
          <div className="dirpick-empty">{tr("rp.load_failed")}</div>
        ) : entries.length === 0 ? (
          <div className="dirpick-empty">{tr("rp.no_subfolders")}</div>
        ) : (
          entries.map((e) => {
            const branch = repoBranch.get(full(e.name)); // defined ⇒ a working copy
            return (
              <button key={e.name} type="button" className="dirpick-row" onClick={() => onChange(full(e.name))}>
                <Icon name={branch !== undefined ? "repo" : "folder"} className="dirpick-ic" />
                <span className="dirpick-name">{e.name}</span>
                {branch ? <span className="dirpick-branch">{branch}</span> : null}
                <Icon name="chevron-right" className="dirpick-go" />
              </button>
            );
          })
        )}
      </div>
      <div className="dirpick-cur">
        {tr("rp.launch_here")} <code>~{path ? "/" + path : ""}</code>
      </div>
    </div>
  );
}
