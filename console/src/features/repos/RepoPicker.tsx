// RepoPicker — choose a repository (filterable list) and branch from a connected
// provider (Bitbucket / GitHub / tenant-internal git). Branch list lazy-loads on
// repo pick. Emits onChange({host, cloneUrl, fullName, branch}) or null. Port of
// the old components/RepoPicker.
import { useEffect, useMemo, useState } from "react";
import { api } from "../../core/api/client.ts";
import { BranchList, relTime } from "./BranchList.tsx";
import type { Branch } from "./BranchList.tsx";
import type { ConnectionsStatus } from "../../types/session.ts";
import { useT } from "../../lib/i18n/index.ts";

// Provider tabs, Bitbucket first (the default). "internal" is the tenant's
// self-hosted git — its lists come from the CP (api/internal-git/*).
const PROVIDERS: [string, string][] = [
  ["bitbucket.org", "Bitbucket"],
  ["github.com", "GitHub"],
  ["internal", ""],
];

const CONN_KEY: Record<string, string> = {
  "github.com": "github",
  "bitbucket.org": "bitbucket",
  internal: "internal",
};

interface RepoItem {
  full_name: string;
  clone_url: string;
  private?: boolean;
  updated_at?: string;
}

export interface RepoSelection {
  host: string;
  cloneUrl: string;
  fullName: string;
  branch: string;
}

interface RepoPickerProps {
  onChange?: (sel: RepoSelection | null) => void;
}

export function RepoPicker({ onChange }: RepoPickerProps) {
  const tr = useT();
  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  const [host, setHost] = useState("bitbucket.org");
  const [repos, setRepos] = useState<RepoItem[] | null>(null);
  const [reposErr, setReposErr] = useState("");
  const [loadingRepos, setLoadingRepos] = useState(false);
  const [repoFilter, setRepoFilter] = useState("");

  const [fullName, setFullName] = useState("");
  const [branches, setBranches] = useState<Branch[]>([]);
  const [branch, setBranch] = useState("");
  const [loadingBranches, setLoadingBranches] = useState(false);
  const [branchErr, setBranchErr] = useState("");

  useEffect(() => {
    api("api/connections").then(setConns).catch(() => setConns({}));
  }, []);

  const connected = (h: string): boolean => !!conns?.[CONN_KEY[h] ?? h]?.connected;

  // If Bitbucket isn't connected, fall back to the first connected provider.
  useEffect(() => {
    if (!conns || connected(host)) return;
    const fallback = PROVIDERS.map(([h]) => h).find(connected);
    if (fallback) setHost(fallback);
  }, [conns]); // eslint-disable-line react-hooks/exhaustive-deps

  // Load repos when the active (connected) provider changes.
  useEffect(() => {
    if (!conns || !connected(host)) return;
    let alive = true;
    setLoadingRepos(true);
    setReposErr("");
    setRepos(null);
    setRepoFilter("");
    setFullName("");
    setBranches([]);
    setBranch("");
    onChange?.(null);
    const isInternal = host === "internal";
    api(isInternal ? "api/internal-git/repos" : `api/connections/git/${host}/repos`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) {
          setReposErr(d.error.message || d.error.code || tr("rp.fetch_failed"));
          setRepos([]);
        } else if (isInternal) {
          setRepos(
            (d.repos || []).map((r: { name: string; clone_url: string; created_at?: string }) => ({
              full_name: r.name,
              clone_url: r.clone_url,
              updated_at: r.created_at,
              private: true,
            })),
          );
        } else {
          setRepos(d.repos || []);
        }
      })
      .catch(() => {
        if (!alive) return;
        setReposErr(tr("rp.check_workspace"));
        setRepos([]);
      })
      .finally(() => alive && setLoadingRepos(false));
    return () => {
      alive = false;
    };
  }, [conns, host]); // eslint-disable-line react-hooks/exhaustive-deps

  const pickRepo = async (fn: string) => {
    setFullName(fn);
    setBranches([]);
    setBranch("");
    setBranchErr("");
    onChange?.(null);
    if (!fn) return;
    setLoadingBranches(true);
    try {
      const isInternal = host === "internal";
      const d = await api(
        isInternal
          ? `api/internal-git/repos/${encodeURIComponent(fn)}/branches`
          : `api/connections/git/${host}/branches?repo=${encodeURIComponent(fn)}`,
      );
      if (d && d.error) {
        setBranchErr(d.error.message || d.error.code || tr("rp.branch_fetch_failed"));
        return;
      }
      // Internal branches are plain strings; external ones are Branch objects.
      let list: Branch[] = isInternal
        ? (d.branches || []).map((n: string) => ({ name: n }))
        : d.branches || [];
      // A commit-less internal repo has no branches — offer its default branch as
      // a selectable placeholder so it can still be cloned.
      if (isInternal && list.length === 0 && d.default_branch) {
        list = [{ name: d.default_branch, default: true }];
      }
      setBranches(list);
      const def = (isInternal ? d.default_branch : d.default) || (list[0] && list[0].name) || "";
      setBranch(def);
      emit(fn, def);
    } catch {
      setBranchErr(tr("rp.branch_fetch_failed"));
    } finally {
      setLoadingBranches(false);
    }
  };

  const pickBranch = (b: string) => {
    setBranch(b);
    emit(fullName, b);
  };

  const emit = (fn: string, b: string) => {
    const repo = (repos || []).find((r) => r.full_name === fn);
    if (repo) onChange?.({ host, cloneUrl: repo.clone_url, fullName: fn, branch: b });
    else onChange?.(null);
  };

  const repoUnix = (r: RepoItem): number =>
    r.updated_at ? Math.floor(Date.parse(r.updated_at) / 1000) || 0 : 0;

  const shownRepos = useMemo(() => {
    const list = [...(repos || [])].sort((a, b) => repoUnix(b) - repoUnix(a));
    const q = repoFilter.trim().toLowerCase();
    if (!q) return list;
    return list.filter((r) => r.full_name.toLowerCase().includes(q));
  }, [repos, repoFilter]);

  return (
    <div className="repopicker">
      <div className="provider-tabs">
        {PROVIDERS.map(([h, label]) => (
          <button
            key={h}
            type="button"
            className={"prov" + (host === h ? " active" : "")}
            disabled={!connected(h)}
            title={connected(h) ? "" : tr("rp.not_connected_git")}
            onClick={() => setHost(h)}
          >
            {h === "internal" ? tr("rp.internal") : label}
            {!connected(h) && <span className="prov-off"> ○</span>}
          </button>
        ))}
      </div>

      {!connected(host) ? (
        <p className="pick-muted">
          {host === "internal"
            ? tr("rp.internal_unavailable")
            : tr("rp.provider_not_connected", { provider: host === "github.com" ? "GitHub" : "Bitbucket" })}
        </p>
      ) : (
        <>
          <div className="pick-field">
            <span>{tr("rp.repositories")}</span>
            {loadingRepos ? (
              <p className="pick-muted">{tr("rp.loading")}</p>
            ) : reposErr ? (
              <p className="pick-muted">{reposErr}</p>
            ) : (
              <div className="branchlist-wrap">
                <input
                  className="branch-filter"
                  placeholder={tr("rp.filter_repos")}
                  value={repoFilter}
                  onChange={(e) => setRepoFilter(e.target.value)}
                />
                {shownRepos.length === 0 ? (
                  <p className="pick-muted">
                    {repoFilter ? tr("rp.no_repos_match") : tr("rp.no_repos")}
                  </p>
                ) : (
                  <ul className="branch-list repo-list">
                    {shownRepos.map((r) => {
                      const active = r.full_name === fullName;
                      return (
                        <li key={r.full_name}>
                          <button
                            type="button"
                            className={"branch-item" + (active ? " current" : "")}
                            onClick={() => pickRepo(r.full_name)}
                            title={r.full_name}
                          >
                            <span className="bi-main">
                              <span className="bi-name">
                                {active ? "● " : ""}
                                {r.full_name}
                                {r.private ? " 🔒" : ""}
                              </span>
                              <span className="bi-time">{relTime(repoUnix(r))}</span>
                            </span>
                          </button>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            )}
          </div>

          <div className="pick-field">
            <span>{tr("rp.branch")}</span>
            {!fullName ? (
              <p className="pick-muted">{tr("rp.select_repo_first")}</p>
            ) : branchErr ? (
              <p className="pick-muted">{branchErr}</p>
            ) : (
              <BranchList
                branches={loadingBranches ? null : branches}
                selected={branch}
                onPick={pickBranch}
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}
