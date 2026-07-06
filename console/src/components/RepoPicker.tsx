import { useEffect, useMemo, useState } from "react";
import { api } from "../api.js";
import BranchList, { relTime } from "./BranchList.jsx";
import type { Branch } from "./BranchList.jsx";
import type { ConnectionsStatus } from "../types/session.ts";

// Provider tabs, Bitbucket first (the default selection). "internal" is the
// tenant's self-hosted git (docs/reference/internal-git-provider): its repo/branch
// lists come from the CP directly (api/internal-git/*), not the per-user Agent.
const PROVIDERS: [string, string][] = [
  ["bitbucket.org", "Bitbucket"],
  ["github.com", "GitHub"],
  ["internal", "内部"],
];

// conns key per provider tab (the connections bag names providers, not hosts).
const CONN_KEY: Record<string, string> = {
  "github.com": "github",
  "bitbucket.org": "bitbucket",
  internal: "internal",
};

// A remote repo from GET /api/connections/git/{host}/repos.
interface RepoItem {
  full_name: string;
  clone_url: string;
  private?: boolean;
  updated_at?: string;
}

// The selection RepoPicker emits (or null when nothing valid is chosen).
export interface RepoSelection {
  host: string;
  cloneUrl: string;
  fullName: string;
  branch: string;
}

interface RepoPickerProps {
  onChange?: (sel: RepoSelection | null) => void;
}

// RepoPicker: choose a repository (dropdown) and branch (filterable, newest-commit-
// first list — same UI as the branch-switch modal) from a connected provider; the
// branch list lazy-loads when a repo is selected. Calls onChange({ host, cloneUrl,
// fullName, branch }) on every change, or onChange(null) when nothing valid is
// selected. Both GitHub and Bitbucket are supported (tabs enable only connected hosts).
export default function RepoPicker({ onChange }: RepoPickerProps) {
  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  const [host, setHost] = useState("bitbucket.org");
  const [repos, setRepos] = useState<RepoItem[] | null>(null); // null = not loaded
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

  // If the default (Bitbucket) isn't connected, fall back to the first connected
  // provider so the picker isn't stuck on an unusable tab.
  useEffect(() => {
    if (!conns || connected(host)) return;
    const fallback = PROVIDERS.map(([h]) => h).find(connected);
    if (fallback) setHost(fallback);
  }, [conns]); // eslint-disable-line react-hooks/exhaustive-deps

  // load repos when the active (connected) provider changes
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
          setReposErr(d.error.message || d.error.code || "取得に失敗しました");
          setRepos([]);
        } else if (isInternal) {
          // Internal repos are named by `name`; map to the common RepoItem shape.
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
        setReposErr("Workspace が起動しているか確認してください");
        setRepos([]);
      })
      .finally(() => alive && setLoadingRepos(false));
    return () => {
      alive = false;
    };
  }, [conns, host]); // eslint-disable-line react-hooks/exhaustive-deps

  // load branches when a repo is picked
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
        setBranchErr(d.error.message || d.error.code || "ブランチ取得に失敗");
        return;
      }
      // Internal branches are plain strings; external ones are Branch objects.
      const list: Branch[] = isInternal
        ? (d.branches || []).map((n: string) => ({ name: n }))
        : d.branches || [];
      setBranches(list);
      const def = (isInternal ? d.default_branch : d.default) || (list[0] && list[0].name) || "";
      setBranch(def);
      emit(fn, def);
    } catch {
      setBranchErr("ブランチ取得に失敗");
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

  // Sort newest-updated-first, then substring-filter by full name.
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
            title={connected(h) ? "" : "未接続（⚙設定 → 接続）"}
            onClick={() => setHost(h)}
          >
            {label}
            {!connected(h) && <span className="prov-off"> ○</span>}
          </button>
        ))}
      </div>

      {!connected(host) ? (
        <p className="muted pad">
          {host === "internal"
            ? "内部リポジトリは利用できません（この環境では無効）。"
            : `${host === "github.com" ? "GitHub" : "Bitbucket"} が未接続です。⚙ 設定 → 接続 から繋いでください。`}
        </p>
      ) : (
        <>
          <div className="pick-field">
            <span>リポジトリ</span>
            {loadingRepos ? (
              <p className="muted pad">読み込み中…</p>
            ) : reposErr ? (
              <p className="muted pad">{reposErr}</p>
            ) : (
              <div className="branchlist-wrap">
                <input
                  className="branch-filter"
                  placeholder="フィルタ（リポジトリ名）"
                  value={repoFilter}
                  onChange={(e) => setRepoFilter(e.target.value)}
                />
                {shownRepos.length === 0 ? (
                  <p className="muted pad">
                    {repoFilter ? "該当するリポジトリがありません" : "リポジトリがありません"}
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
            <span>ブランチ</span>
            {!fullName ? (
              <p className="muted pad">先にリポジトリを選択</p>
            ) : branchErr ? (
              <p className="muted pad">{branchErr}</p>
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
