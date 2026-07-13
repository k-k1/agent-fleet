// CloneForm — the clone-source picker shared by「リポジトリを clone」(NewRepoModal)
// and the はじめる hub's clone-and-continue stage (StartModal, 起動導線 Ph2):
// 取得元 (接続から選ぶ / URL 手入力) + RepoPicker or URL+branch inputs. It only
// resolves WHAT to clone — branch forking / folder naming stay with the caller.
// Was duplicated across NewRepoModal and NewSessionModal (same SOURCE_HELP text).
import { useEffect, useState } from "react";
import { RepoPicker } from "./RepoPicker.tsx";
import type { RepoSelection } from "./RepoPicker.tsx";

const SOURCE_HELP: Record<string, string> = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
};

export interface CloneSource {
  cloneUrl: string;
  branch: string;
}

interface CloneFormProps {
  onChange: (v: CloneSource) => void;
}

export function CloneForm({ onChange }: CloneFormProps) {
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
      <span className="ui-field-label">取得元</span>
      <div className="ui-seg">
        {(
          [
            ["picker", "接続から選ぶ"],
            ["url", "URL 手入力"],
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
      <span className="ui-field-hint">{SOURCE_HELP[source]}</span>

      {source === "picker" ? (
        <RepoPicker onChange={setSel} />
      ) : (
        <>
          <label className="ui-field">
            <span className="ui-field-label">clone URL</span>
            <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://… / git@…" />
          </label>
          <label className="ui-field">
            <span className="ui-field-label">ブランチ（任意）</span>
            <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="既定ブランチ" />
          </label>
        </>
      )}
    </div>
  );
}
