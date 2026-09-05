// CloneForm — the clone-source picker shared by the "clone a repository" dialog
// (NewRepoModal) and the Start hub's clone-and-continue stage (StartModal, launch
// flow Ph2): source (pick from a connection / type a URL) + RepoPicker or
// URL+branch inputs. It only resolves WHAT to clone — branch forking / folder
// naming stay with the caller.
import { useEffect, useState } from "react";
import { RepoPicker } from "./RepoPicker.tsx";
import type { RepoSelection } from "./RepoPicker.tsx";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

const SOURCE_HELP: Record<string, MsgKey> = {
  picker: "rp.source_help_picker",
  url: "rp.source_help_url",
};

export interface CloneSource {
  cloneUrl: string;
  branch: string;
}

interface CloneFormProps {
  onChange: (v: CloneSource) => void;
}

export function CloneForm({ onChange }: CloneFormProps) {
  const tr = useT();
  const [source, setSource] = useState<"picker" | "url">("picker");
  const [sel, setSel] = useState<RepoSelection | null>(null);
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");

  const cloneUrl = (source === "picker" ? sel?.cloneUrl : url.trim()) || "";
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();

  useEffect(() => {
    onChange({ cloneUrl, branch: cloneBranch });
    // onChange is a setter from the caller; re-emitting on identity churn is harmless
    // but pointless — only the derived values matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cloneUrl, cloneBranch]);

  return (
    <div className="ui-field">
      <span className="ui-field-label">{tr("rp.source")}</span>
      <div className="ui-seg">
        {(
          [
            ["picker", tr("rp.source_connected")],
            ["url", tr("rp.source_url")],
          ] as const
        ).map(([v, label]) => (
          <button
            key={v}
            type="button"
            className={"seg-btn" + (source === v ? " active" : "")}
            onClick={() => setSource(v)}
          >
            {label}
          </button>
        ))}
      </div>
      <span className="ui-field-hint">{tr(SOURCE_HELP[source])}</span>

      {source === "picker" ? (
        <RepoPicker onChange={setSel} />
      ) : (
        <>
          <label className="ui-field">
            <span className="ui-field-label">{tr("rp.clone_url")}</span>
            <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://… / git@…" />
          </label>
          <label className="ui-field">
            <span className="ui-field-label">{tr("rp.branch_optional")}</span>
            <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder={tr("rp.default_branch")} />
          </label>
        </>
      )}
    </div>
  );
}
