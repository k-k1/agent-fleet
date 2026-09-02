import { useEffect, useState } from "react";
import { api, apiJSON, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { InternalRepoBrowser } from "./InternalRepoBrowser.tsx";
import { ProviderCard, StatusPill } from "../parts/providerCard.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// InternalReposTab manages the tenant's self-hosted git repositories (docs/reference/
// internal-git-provider). Split out of GitTab into the ワークスペース group: unlike the
// OAuth git-hosting cards (GitHub/Bitbucket, which are external-account CONNECTIONS and
// need a running Agent), internal repos are CP-native workspace infra — list / create /
// rename / delete talk to the CP directly (api/internal-git/*), need no external account,
// and work while the workspace is stopped. Clone URLs authenticate via the CP-injected
// token, so there is no connect step.

// An internal repo from GET /api/internal-git/repos.
interface InternalRepo {
  name: string;
  clone_url: string;
  default_branch?: string;
  created_at?: string;
}

export function InternalReposTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [repos, setRepos] = useState<InternalRepo[] | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [browsing, setBrowsing] = useState<string | null>(null);

  const load = () =>
    api("api/internal-git/repos")
      .then((d) => setRepos(d && !d.error ? d.repos || [] : []))
      .catch(() => setRepos([]));
  useEffect(() => {
    load();
  }, []);

  const create = async () => {
    const n = name.trim();
    if (!n) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/internal-git/repos", "POST", { name: n });
      if (res && res.error) {
        toast(tr("git.create_failed", { msg: res.error.message || res.error.code || "" }));
        return;
      }
      toast(tr("git.created", { name: res.name }));
      setName("");
      load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (rn: string) => {
    const ok = await askConfirm({
      title: tr("git.internal_delete_title"),
      body: tr("git.internal_delete_confirm", { name: rn }),
      confirmLabel: tr("common.delete"),
      danger: true,
    });
    if (!ok) return;
    const res = await raw(`api/internal-git/repos/${encodeURIComponent(rn)}`, { method: "DELETE" });
    if (!res.ok) {
      toast(tr("git.delete_failed"));
      return;
    }
    toast(tr("git.deleted", { name: rn }), { kind: "success", persist: true });
    load();
  };

  const rename = async (oldName: string, newName: string) => {
    const res = await apiJSON(`api/internal-git/repos/${encodeURIComponent(oldName)}/rename`, "POST", {
      new_name: newName,
    });
    if (res && res.error) {
      toast(tr("git.rename_failed", { msg: res.error.message || res.error.code || "" }));
      return false;
    }
    toast(tr("git.renamed", { old: oldName, new: res.name }));
    load();
    return true;
  };

  const copyUrl = (url: string) => {
    navigator.clipboard?.writeText(url).then(
      () => toast(tr("git.clone_url_copied")),
      () => {},
    );
  };

  const count = repos?.length ?? 0;
  return (
    <div className="conns">
      <ProviderCard
        id="internal"
        name={tr("git.internal_name")}
        status={<StatusPill on>{count ? tr("git.count", { count }) : tr("git.available")}</StatusPill>}
      >
        <div className="p-desc">{tr("git.internal_desc")}</div>
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              placeholder={tr("git.repo_name_placeholder")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && create()}
            />
            <button disabled={busy || !name.trim()} onClick={create}>
              {tr("git.create")}
            </button>
          </div>
          {repos === null ? (
            <p className="muted pad">{tr("common.loading")}</p>
          ) : repos.length === 0 ? (
            <p className="muted pad">{tr("git.internal_empty")}</p>
          ) : (
            <ul className="internal-repo-list">
              {repos.map((r) => (
                <InternalRepoRow
                  key={r.name}
                  repo={r}
                  onCopy={() => copyUrl(r.clone_url)}
                  onBrowse={() => setBrowsing(r.name)}
                  onRename={rename}
                  onRemove={() => remove(r.name)}
                />
              ))}
            </ul>
          )}
        </div>
      </ProviderCard>
      {browsing && <InternalRepoBrowser name={browsing} onClose={() => setBrowsing(null)} />}
    </div>
  );
}

// InternalRepoRow is one repo in the internal list: name (editable via リネーム),
// its clone URL (click to copy), and 削除. Rename edit-state is per-row.
function InternalRepoRow({
  repo,
  onCopy,
  onBrowse,
  onRename,
  onRemove,
}: {
  repo: InternalRepo;
  onCopy: () => void;
  onBrowse: () => void;
  onRename: (oldName: string, newName: string) => Promise<boolean>;
  onRemove: () => void;
}) {
  const tr = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(repo.name);
  const [busy, setBusy] = useState(false);

  const commit = async () => {
    const n = draft.trim();
    if (!n || n === repo.name) {
      setEditing(false);
      setDraft(repo.name);
      return;
    }
    setBusy(true);
    const ok = await onRename(repo.name, n);
    setBusy(false);
    if (ok) setEditing(false);
  };

  if (editing) {
    return (
      <li className="internal-repo">
        <input
          className="cinput ir-rename"
          value={draft}
          autoFocus
          disabled={busy}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") commit();
            if (e.key === "Escape") {
              // Don't let the Esc bubble to the settings modal's document-level
              // close handler — it should only cancel the rename.
              e.stopPropagation();
              setEditing(false);
              setDraft(repo.name);
            }
          }}
        />
        <button type="button" disabled={busy} onClick={commit}>
          {tr("common.save")}
        </button>
        <button
          type="button"
          className="ghost"
          disabled={busy}
          onClick={() => {
            setEditing(false);
            setDraft(repo.name);
          }}
        >
          {tr("git.rename_cancel")}
        </button>
      </li>
    );
  }
  return (
    <li className="internal-repo">
      <span className="ir-name" title={repo.name}>
        {repo.name}
      </span>
      <button type="button" className="ir-url" title={tr("git.copy_clone_url")} onClick={onCopy}>
        <code>{repo.clone_url}</code>
      </button>
      <button type="button" className="ghost" title={tr("git.browse_title")} onClick={onBrowse}>
        {tr("git.browse")}
      </button>
      <button type="button" className="ghost" title={tr("git.rename")} onClick={() => setEditing(true)}>
        {tr("git.rename")}
      </button>
      <button type="button" className="ghost danger conn-disconnect" title={tr("common.delete")} onClick={onRemove}>
        {tr("common.delete")}
      </button>
    </li>
  );
}
