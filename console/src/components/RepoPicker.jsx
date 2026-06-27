import { useEffect, useState } from "react";
import { api } from "../api.js";
import BranchList from "./BranchList.jsx";

// Provider tabs, Bitbucket first (the default selection).
const PROVIDERS = [
  ["bitbucket.org", "Bitbucket"],
  ["github.com", "GitHub"],
];

// RepoPicker: choose a repository (dropdown) and branch (filterable, newest-commit-
// first list — same UI as the branch-switch modal) from a connected provider; the
// branch list lazy-loads when a repo is selected. Calls onChange({ host, cloneUrl,
// fullName, branch }) on every change, or onChange(null) when nothing valid is
// selected. Both GitHub and Bitbucket are supported (tabs enable only connected hosts).
export default function RepoPicker({ onChange }) {
  const [conns, setConns] = useState(null);
  const [host, setHost] = useState("bitbucket.org");
  const [repos, setRepos] = useState(null); // null = not loaded
  const [reposErr, setReposErr] = useState("");
  const [loadingRepos, setLoadingRepos] = useState(false);

  const [fullName, setFullName] = useState("");
  const [branches, setBranches] = useState([]);
  const [branch, setBranch] = useState("");
  const [loadingBranches, setLoadingBranches] = useState(false);
  const [branchErr, setBranchErr] = useState("");

  useEffect(() => {
    api("api/connections").then(setConns).catch(() => setConns({}));
  }, []);

  const connected = (h) =>
    h === "github.com" ? !!conns?.github?.connected : !!conns?.bitbucket?.connected;

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
    setFullName("");
    setBranches([]);
    setBranch("");
    onChange?.(null);
    api(`api/connections/git/${host}/repos`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) {
          setReposErr(d.error.message || d.error.code || "取得に失敗しました");
          setRepos([]);
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
  const pickRepo = async (fn) => {
    setFullName(fn);
    setBranches([]);
    setBranch("");
    setBranchErr("");
    onChange?.(null);
    if (!fn) return;
    setLoadingBranches(true);
    try {
      const d = await api(`api/connections/git/${host}/branches?repo=${encodeURIComponent(fn)}`);
      if (d && d.error) {
        setBranchErr(d.error.message || d.error.code || "ブランチ取得に失敗");
        return;
      }
      const list = d.branches || [];
      setBranches(list);
      const def = d.default || (list[0] && list[0].name) || "";
      setBranch(def);
      emit(fn, def);
    } catch {
      setBranchErr("ブランチ取得に失敗");
    } finally {
      setLoadingBranches(false);
    }
  };

  const pickBranch = (b) => {
    setBranch(b);
    emit(fullName, b);
  };

  const emit = (fn, b) => {
    const repo = (repos || []).find((r) => r.full_name === fn);
    if (repo) onChange?.({ host, cloneUrl: repo.clone_url, fullName: fn, branch: b });
    else onChange?.(null);
  };

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
          {host === "github.com" ? "GitHub" : "Bitbucket"} が未接続です。⚙ 設定 → 接続 から繋いでください。
        </p>
      ) : (
        <>
          <label className="pick-field">
            <span>リポジトリ</span>
            <select
              value={fullName}
              disabled={loadingRepos || !!reposErr}
              onChange={(e) => pickRepo(e.target.value)}
            >
              <option value="">
                {loadingRepos ? "読み込み中…" : reposErr ? reposErr : "選択してください"}
              </option>
              {(repos || []).map((r) => (
                <option key={r.full_name} value={r.full_name}>
                  {r.full_name}
                  {r.private ? " 🔒" : ""}
                </option>
              ))}
            </select>
          </label>

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
