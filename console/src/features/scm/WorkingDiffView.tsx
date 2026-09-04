// WorkingDiffView — ONE working-tree file's diff in its own pane, opened from
// the changes view. Port of views/WorkingDiffView.
import { useState } from "react";
import type { ReactNode } from "react";
import { api, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { Diff } from "./GitDiff.tsx";

export function WorkingDiffView({
  repo,
  path,
  staged,
  wrap,
  headerActions,
}: {
  repo: string;
  path: string;
  staged?: boolean | null;
  wrap?: boolean;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const [diff, setDiff] = useState("");
  // Right after a workspace start the agent is unreachable and api() returns http_5xx, so
  // retry transient failures (isTransientErr).
  useRetryLoad(async (signal) => {
    if (!repo || !path) {
      setDiff("");
      return true;
    }
    const q = `path=${encodeURIComponent(path)}${staged ? "&staged=1" : ""}`;
    let d;
    try {
      d = await api(`api/repos/${encodeURIComponent(repo)}/diff?${q}`);
    } catch {
      return false; // network drop — retry
    }
    if (signal.aborted) return true;
    if (isTransientErr(d)) return false;
    setDiff(d.diff && d.diff.length ? d.diff : tr("scm.no_diff"));
    return true;
  }, [repo, path, staged]);

  return (
    <div className="scmview">
      {/* Trailing items here are status tags, not actions — they stay inline on
          the head's own gap rather than in the (tighter-packed) actions slot. */}
      <ViewHead actions={headerActions}>
        <span className="view-title" title={path || ""}>
          <Icon name="git-compare" /> {path || tr("scm.no_file_selected")}
        </span>
        <span className="view-spacer" />
        {staged ? <span className="scm-staged-tag">staged</span> : null}
        {repo && <span className="scm-repo-tag">{repo}</span>}
      </ViewHead>
      <div className="scm-scroll">
        <Diff text={diff} wrap={wrap} />
      </div>
    </div>
  );
}
