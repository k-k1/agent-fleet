import { useEffect, useState } from "react";
import { api } from "../api.js";

// RepoPicker: choose a repository and branch from a connected provider via two
// dropdowns (the CodeLeaf / git-reader pattern — branches lazy-load when a repo is
// selected). Calls onChange({ host, cloneUrl, fullName, branch }) on every change,
// or onChange(null) when nothing valid is selected. GitHub is supported now;
// Bitbucket reports not-implemented (501) from the backend and we surface that.
export default function RepoPicker({ onChange }) {
  const [conns, setConns] = useState(null);
  const [host, setHost] = useState("github.com");
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
      const def = d.default || list[0] || "";
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
        {[
          ["github.com", "GitHub"],
          ["bitbucket.org", "Bitbucket"],
        ].map(([h, label]) => (
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

          <label className="pick-field">
            <span>ブランチ</span>
            <select
              value={branch}
              disabled={!fullName || loadingBranches || branches.length === 0}
              onChange={(e) => pickBranch(e.target.value)}
            >
              <option value="">
                {!fullName ? "先にリポジトリを選択" : loadingBranches ? "読み込み中…" : branchErr || "—"}
              </option>
              {branches.map((b) => (
                <option key={b} value={b}>
                  {b}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
    </div>
  );
}
