// ProjectModal — docs/log/56 / docs/log/57 §3: a working copy's project-scope settings,
// entered from the repo row's right-click menu (a SEPARATE modal from the settings
// dialog on purpose — that one is workspace-wide/user-scope, this one is "this one
// repo", and mixing the two would make it unclear which scope a value belongs to).
// Ships exactly one section (MCP servers): the servers × files matrix that answers
// "is the same server duplicated, and which copy is actually alive" (docs/log/56 §1's
// novel-lab motivation), plus the P1 write actions (③, docs/log/56 §9.2) — copying a
// server into another file (with dialect translation and conflict resolution) and
// adding a file to git's ignore list. This modal never plans-and-applies silently:
// every write goes through a preview the user must explicitly confirm.
import { useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { errText, isTransientErr } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { ProjectCopyPanel, ProjectIgnorePanel } from "./ProjectActionPanels.tsx";
import {
  dialectsText,
  divergedNames,
  fetchProjectMcpSnapshot,
  gateText,
  matrixServerNames,
  serverIn,
  warningText,
} from "./projectMcpWire.ts";
import type { ProjectSnapshot } from "./projectMcpWire.ts";

interface ProjectModalProps {
  repo: string;
  onClose?: () => void;
}

export function ProjectModal({ repo, onClose }: ProjectModalProps) {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);

  const [snap, setSnap] = useState<ProjectSnapshot | null>(null);
  const [err, setErr] = useState("");
  const [reloadTick, setReloadTick] = useState(0);

  useRetryLoad(
    async (signal) => {
      if (!running) return true; // gated below; nothing to fetch while stopped
      let d: ProjectSnapshot | { error: { code: string; message?: string } };
      try {
        d = await fetchProjectMcpSnapshot(repo);
      } catch {
        d = { error: { code: "network" } };
      }
      if (signal.aborted) return true;
      if ("error" in d) {
        if (isTransientErr(d)) return false;
        setErr(errText(d.error));
        return true;
      }
      setErr("");
      setSnap(d);
      return true;
    },
    [repo, running, reloadTick],
  );

  return (
    <Modal title={tr("pmcp.title", { repo })} onClose={onClose} className="pmcp-modal">
      <div className="ui-modal-body pmcp-body">
        {!running ? (
          <EmptyState icon="debug-disconnect" title={tr("pmcp.ws_required_title")} hint={tr("pmcp.ws_required_hint")}>
            <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
              {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
            </Button>
          </EmptyState>
        ) : err ? (
          <p className="ps-note ps-note-warn">{err}</p>
        ) : !snap ? (
          <p className="ps-note">{tr("common.loading")}</p>
        ) : (
          <ProjectMcpSection repo={repo} snap={snap} onApplied={() => setReloadTick((n) => n + 1)} />
        )}
      </div>
    </Modal>
  );
}

interface ProjectMcpSectionProps {
  repo: string;
  snap: ProjectSnapshot;
  onApplied: () => void;
}

function ProjectMcpSection({ repo, snap, onApplied }: ProjectMcpSectionProps) {
  const tr = useT();
  const names = matrixServerNames(snap.files);
  const diverged = divergedNames(snap.warnings);
  const [warningsOpen, setWarningsOpen] = useState(true);
  const [copySource, setCopySource] = useState<{ file: string; name: string } | null>(null);
  const [ignoreFile, setIgnoreFile] = useState<string | null>(null);

  const closeActions = () => {
    setCopySource(null);
    setIgnoreFile(null);
  };
  const handleApplied = () => {
    onApplied();
  };

  return (
    <div className="pmcp-section">
      {snap.worktree && (
        <p className="ps-note">
          <Icon name="info" /> {tr("pmcp.worktree_note")}
        </p>
      )}
      {snap.vcs !== "git" && (
        <p className="ps-note ps-note-warn">
          <Icon name="warning" /> {tr(snap.vcs === "svn" ? "pmcp.vcs_svn_note" : "pmcp.vcs_none_note")}
        </p>
      )}

      <h4 className="pmcp-h">{tr("pmcp.files_title")}</h4>
      <ul className="pmcp-files">
        {snap.files.map((f) => (
          <li key={f.path} className="pmcp-file-row">
            <span className="pmcp-file-path">{f.path}</span>
            {f.kinds.map((k) => (
              <span key={k} className="pmcp-kind-chip" title={kindLabel(k)}>
                <Icon name={kindIcon(k)} /> {kindLabel(k)}
              </span>
            ))}
            {!f.exists && <span className="pmcp-file-state muted">{tr("pmcp.file_missing")}</span>}
            {f.exists && !f.parsable && (
              <span className="pmcp-file-state warn" title={f.note}>
                <Icon name="warning" /> {tr("pmcp.file_unparsable")}
              </span>
            )}
            {f.exists && f.parsable && (
              <span className="pmcp-file-state">
                {f.trackedUncertain
                  ? tr("pmcp.tracked_unknown")
                  : f.tracked
                    ? tr("pmcp.tracked")
                    : f.ignored
                      ? tr("pmcp.ignored")
                      : tr("pmcp.untracked")}
              </span>
            )}
            {f.exists && snap.vcs === "git" && !f.ignored && (
              <button
                type="button"
                className="pmcp-link-btn"
                onClick={() => {
                  closeActions();
                  setIgnoreFile(f.path);
                }}
              >
                {tr("pmcp.add_to_ignore")}
              </button>
            )}
          </li>
        ))}
      </ul>

      {ignoreFile && <ProjectIgnorePanel repo={repo} file={ignoreFile} onClose={closeActions} />}

      {names.length > 0 ? (
        <>
          <h4 className="pmcp-h">{tr("pmcp.matrix_title")}</h4>
          <div className="pmcp-matrix-wrap">
            <table className="pmcp-matrix">
              <thead>
                <tr>
                  <th>{tr("pmcp.server_col")}</th>
                  {snap.files.map((f) => (
                    <th key={f.path} title={f.path}>
                      {f.path}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {names.map((name) => (
                  <tr key={name}>
                    <td className="pmcp-server-name">
                      {name}
                      {diverged.has(name) && (
                        <Icon name="warning" className="pmcp-diverge" title={tr("pmcp.diverged_hint")} />
                      )}
                    </td>
                    {snap.files.map((f) => {
                      const s = serverIn(f, name);
                      return (
                        <td key={f.path} className={"pmcp-cell" + (s ? " present" : "")}>
                          {s ? (
                            <button
                              type="button"
                              className="pmcp-cell-btn"
                              title={tr("pmcp.copy_from_cell", { server: name, file: f.path })}
                              onClick={() => {
                                closeActions();
                                setCopySource({ file: f.path, name });
                              }}
                            >
                              <Icon name="circle-filled" /> {s.transport}
                            </button>
                          ) : (
                            <Icon name="circle-outline" className="muted" />
                          )}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : (
        <p className="ps-note">{tr("pmcp.empty")}</p>
      )}

      {copySource && (
        <ProjectCopyPanel repo={repo} snap={snap} source={copySource} onClose={closeActions} onApplied={handleApplied} />
      )}

      {snap.warnings && snap.warnings.length > 0 && (
        <>
          <h4 className="pmcp-h">
            <button type="button" className="pmcp-h-toggle" onClick={() => setWarningsOpen((v) => !v)}>
              <Icon name={warningsOpen ? "chevron-down" : "chevron-right"} />{" "}
              {tr("pmcp.warnings_title", { n: snap.warnings.length })}
            </button>
          </h4>
          {warningsOpen && (
            <ul className="pmcp-warnings">
              {snap.warnings.map((w, i) => (
                <li key={i} className={"pmcp-warning " + w.severity}>
                  <Icon name={w.severity === "red" ? "error" : "warning"} /> {warningText(w)}
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      <h4 className="pmcp-h">{tr("pmcp.kinds_title")}</h4>
      <ul className="pmcp-kinds">
        {snap.kinds.map((k) => (
          <li key={k.kind} className="pmcp-kind-row">
            <span className="pmcp-kind-chip">
              <Icon name={kindIcon(k.kind)} /> {kindLabel(k.kind)}
            </span>
            {!k.hasProjectScope && <span className="muted">{tr("pmcp.kind_no_scope")}</span>}
            {k.hasProjectScope && k.unverified && (
              <span className="pmcp-badge-unverified">{tr("pmcp.kind_unverified")}</span>
            )}
            {k.hasProjectScope && !k.unverified && (
              <span className="muted">
                {dialectsText(k.dialects)}
                {gateText(k.gateCode) ? " · " + gateText(k.gateCode) : ""}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
