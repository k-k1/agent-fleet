// NewRepoModal — bring a repository into ~/repos. Two kinds (docs/41): Git clone
// (source picking in the shared CloneForm + fork-a-branch / folder name) and SVN
// checkout (URL + optional subpath + optional basic auth). The op itself runs in
// the parent (spinner row in the rail) so the user isn't trapped in a busy dialog.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { CloneForm } from "./CloneForm.tsx";
import type { CloneSource } from "./CloneForm.tsx";
import type { CloneRequest, SvnCheckoutRequest } from "./clone.ts";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../../lib/reponame.ts";
import { useT } from "../../lib/i18n/index.ts";

export type { CloneRequest };

interface NewRepoModalProps {
  onClose?: () => void;
  onClone: (req: CloneRequest) => void;
  onSvnCheckout?: (req: SvnCheckoutRequest) => void;
  repos?: { name: string }[];
}

// deriveSvnName picks a folder name from an SVN URL + subpath: the last path
// segment, but a bare "trunk" tip falls back to its parent (the repo name). Mirrors
// the backend's deriveSvnName; the user can still override.
function deriveSvnName(url: string, subpath: string): string {
  const base = url.replace(/\/+$/, "");
  const sp = subpath.trim().replace(/^\/+|\/+$/g, "");
  const full = (sp ? base + "/" + sp : base).replace(/\/+$/, "");
  const segs = full.split("/").filter((s) => s && !s.endsWith(":"));
  if (!segs.length) return "";
  const last = segs[segs.length - 1];
  if (last === "trunk" && segs.length >= 2) return segs[segs.length - 2];
  return last;
}

export function NewRepoModal({ onClose, onClone, onSvnCheckout, repos = [] }: NewRepoModalProps) {
  const tr = useT();
  const [vcs, setVcs] = useState<"git" | "svn">("git");
  const repoNames = new Set(repos.map((r) => r.name));

  // --- Git clone state ---
  const [src, setSrc] = useState<CloneSource>({ cloneUrl: "", branch: "" });
  const [newBranch, setNewBranch] = useState("");
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);

  const cloneUrl = src.cloneUrl;
  const cloneBranch = src.branch;
  const cloneNewBranch = cloneUrl ? newBranch.trim() : "";
  const targetBranch = cloneNewBranch || cloneBranch;
  const derivedRepo = cloneUrl ? deriveRepoName(cloneUrl) : "";
  const collision = !!derivedRepo && repoNames.has(derivedRepo);
  const wantName = !!cloneNewBranch || collision;
  const suggestedName = derivedRepo
    ? uniqueRepoName(`${derivedRepo}-${sanitizeSeg(targetBranch)}`, repoNames)
    : "";
  useEffect(() => {
    if (!nameEdited) setName(suggestedName);
  }, [suggestedName, nameEdited]);
  const nameOk = !wantName || (repoNameRe.test(name.trim()) && !repoNames.has(name.trim()));
  const canSubmitGit = !!cloneUrl && nameOk;

  // --- SVN checkout state ---
  const [svnUrl, setSvnUrl] = useState("");
  const [subpath, setSubpath] = useState("");
  const [svnUser, setSvnUser] = useState("");
  const [svnPass, setSvnPass] = useState("");
  const [svnSave, setSvnSave] = useState(false);
  const [svnName, setSvnName] = useState("");
  const [svnNameEdited, setSvnNameEdited] = useState(false);
  const svnDerived = svnUrl.trim() ? deriveSvnName(svnUrl.trim(), subpath) : "";
  const svnSuggested = svnDerived ? uniqueRepoName(sanitizeSeg(svnDerived), repoNames) : "";
  useEffect(() => {
    if (!svnNameEdited) setSvnName(svnSuggested);
  }, [svnSuggested, svnNameEdited]);
  const svnNameOk = repoNameRe.test(svnName.trim()) && !repoNames.has(svnName.trim());
  const svnUrlOk = /^https?:\/\/.+/i.test(svnUrl.trim());
  const canSubmitSvn = svnUrlOk && svnNameOk;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (vcs === "svn") {
      if (!canSubmitSvn || !onSvnCheckout) return;
      onSvnCheckout({
        url: svnUrl.trim(),
        subpath: subpath.trim(),
        name: svnName.trim(),
        username: svnUser.trim(),
        password: svnPass,
        save: svnSave && !!svnUser.trim(),
      });
      onClose?.();
      return;
    }
    if (!canSubmitGit) return;
    onClone({
      remote_url: cloneUrl,
      branch: cloneBranch,
      new_branch: cloneNewBranch,
      name: wantName ? name.trim() : "",
    });
    onClose?.();
  };

  return (
    <Modal title={tr("rp.clone_repo_title")} onClose={onClose} as="form" onSubmit={submit}>
      <div className="ui-modal-body">
        {/* Kind: Git clone vs SVN checkout. */}
        <div className="ui-field">
          <span className="ui-field-label">{tr("rp.vcs")}</span>
          <div className="ui-seg">
            {(
              [
                ["git", tr("rp.vcs_git")],
                ["svn", tr("rp.vcs_svn")],
              ] as const
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                className={"seg-btn" + (vcs === v ? " active" : "")}
                onClick={() => setVcs(v)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>

        {vcs === "git" ? (
          <>
            <CloneForm onChange={setSrc} />

            {!!cloneUrl && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("rp.new_branch_optional")}</span>
                <input
                  value={newBranch}
                  onChange={(e) => setNewBranch(e.target.value)}
                  placeholder={tr("rp.create_from", { branch: cloneBranch || tr("rp.default_branch") })}
                />
                <span className="ui-field-hint">
                  {tr("rp.new_branch_hint_pre")} <code>{cloneBranch || tr("rp.default_branch")}</code> {tr("rp.new_branch_hint_post")}
                </span>
              </div>
            )}

            {wantName && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("rp.folder_name")}</span>
                <span className="ui-field-hint">
                  {cloneNewBranch ? (
                    <>
                      {tr("rp.workcopy_newbranch_pre")} <code>{cloneNewBranch}</code> {tr("rp.workcopy_newbranch_post")}
                    </>
                  ) : (
                    tr("rp.workcopy_exists", { name: derivedRepo })
                  )}
                </span>
                <input
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value);
                    setNameEdited(true);
                  }}
                  placeholder={suggestedName}
                />
                {!nameOk && <span className="ui-field-hint">{tr("rp.name_rule_hint")}</span>}
              </div>
            )}
          </>
        ) : (
          <>
            <label className="ui-field">
              <span className="ui-field-label">{tr("rp.svn_url")}</span>
              <input value={svnUrl} onChange={(e) => setSvnUrl(e.target.value)} placeholder="https://svn.example.com/repo" />
            </label>
            <label className="ui-field">
              <span className="ui-field-label">{tr("rp.svn_subpath")}</span>
              <input value={subpath} onChange={(e) => setSubpath(e.target.value)} placeholder="trunk" />
              <span className="ui-field-hint">{tr("rp.svn_subpath_hint")}</span>
            </label>
            <label className="ui-field">
              <span className="ui-field-label">{tr("rp.svn_username")}</span>
              <input value={svnUser} onChange={(e) => setSvnUser(e.target.value)} autoComplete="off" />
            </label>
            <label className="ui-field">
              <span className="ui-field-label">{tr("rp.svn_password")}</span>
              <input type="password" value={svnPass} onChange={(e) => setSvnPass(e.target.value)} autoComplete="off" />
            </label>
            {!!svnUser.trim() && (
              <label className="ui-field" style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
                <input type="checkbox" checked={svnSave} onChange={(e) => setSvnSave(e.target.checked)} />
                <span>
                  {tr("rp.svn_save")}
                  <span className="ui-field-hint"> — {tr("rp.svn_save_hint")}</span>
                </span>
              </label>
            )}
            <div className="ui-field">
              <span className="ui-field-label">{tr("rp.folder_name")}</span>
              <input
                value={svnName}
                onChange={(e) => {
                  setSvnName(e.target.value);
                  setSvnNameEdited(true);
                }}
                placeholder={svnSuggested}
              />
              {!svnNameOk && !!svnName.trim() && <span className="ui-field-hint">{tr("rp.name_rule_hint")}</span>}
            </div>
          </>
        )}
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("rp.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={vcs === "svn" ? !canSubmitSvn : !canSubmitGit}>
          {vcs === "svn" ? tr("rp.svn_checkout") : tr("rp.clone")}
        </Button>
      </footer>
    </Modal>
  );
}
