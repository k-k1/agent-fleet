// AttachAgentModal — "attach an agent" to a studio (ADR 0100 decision 8). Built from the launch
// dialog's parts (ModelPicker, EffortPicker, SubdirPicker, BranchList) rather than by embedding
// the dialog, the way StartModal's home stage uses them: there is no prompt field (the first turn
// is the studio's persona), and the kind list and the worktree rule are the studio's own.
import { useEffect, useMemo, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { agentOf } from "../../../agents/registry.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { resolveEffort, resolveModel } from "../../../lib/repoLast.ts";
import { agentLaunchDefault, useSettings } from "../../../lib/settings.ts";
import { Button } from "../../../ui/Button.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { EffortPicker, ModelPicker } from "../../../ui/ModelPicker.tsx";
import { BranchList, type Branch } from "../../repos/BranchList.tsx";
import { SubdirPicker } from "../../repos/SubdirPicker.tsx";
import { useReposStore } from "../../repos/store.ts";
import { useRepoRailContext } from "../../repos/useRepoRail.ts";
import { studioKindChoices, worktreeOptional, type StudioDriver } from "../studioSync.ts";

export interface AttachOpts {
  kind: string;
  driver: StudioDriver;
  model: string;
  effort: string;
  /** The working copy's path, "" for home. */
  dir: string;
  subdir: string;
  worktree: boolean;
  /** The branch a worktree starts from; "" = the working copy's own. */
  base: string;
  skipPermissions?: boolean;
}

export function AttachAgentModal({
  replacing,
  onClose,
  onAttach,
}: {
  /** The session being replaced ("switch agents"), shown so the member knows it is stopped. */
  replacing?: string;
  onClose: () => void;
  onAttach: (o: AttachOpts) => Promise<boolean>;
}) {
  const tr = useT();
  const settings = useSettings();
  const ctx = useRepoRailContext();
  const repos = useReposStore((s) => s.repos);
  const [driver, setDriver] = useState<StudioDriver>("tui");
  const choices = useMemo(() => studioKindChoices(driver, ctx.launchKinds), [driver, ctx.launchKinds]);
  const firstOpen = choices.find((c) => !c.blocked)?.kind || "";
  const [kind, setKind] = useState<string>(firstOpen);
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [skipPerm, setSkipPerm] = useState<boolean | undefined>(undefined);
  const [repoName, setRepoName] = useState("");
  const [subdir, setSubdir] = useState("");
  const [worktree, setWorktree] = useState(true);
  const [base, setBase] = useState("");
  const [branches, setBranches] = useState<Branch[] | null>(null);
  const [baseOpen, setBaseOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  // Keep the kind among the offered ones when the execution method (or the connections) change.
  useEffect(() => {
    if (!choices.some((c) => c.kind === kind && !c.blocked)) setKind(firstOpen);
  }, [choices, kind, firstOpen]);
  useEffect(() => {
    if (!kind) return;
    const d = agentLaunchDefault(settings, kind);
    setModel(resolveModel(kind, "", d.model));
    setEffort(resolveEffort(kind, "", d.effort));
    setSkipPerm(undefined);
    // Only on a kind change: re-running on every settings sync would reset a picked model.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  const repo = repos.find((r) => r.name === repoName) || null;
  const canOptOut = worktreeOptional(kind, driver);
  const wt = repo ? (canOptOut ? worktree : true) : false;
  // Without a worktree the af server finds its session by the working directory; where that is
  // not certain (codex Managed and every other Managed kind but lcpp) a repository is required.
  const needsRepo = !canOptOut && !repo;

  useEffect(() => {
    if (!baseOpen || !repo || branches !== null) return;
    let alive = true;
    api(`api/repos/${encodeURIComponent(repo.name)}/branches`)
      .then((d) => alive && setBranches(d?.branches || []))
      .catch(() => alive && setBranches([]));
    return () => {
      alive = false;
    };
  }, [baseOpen, repo, branches]);

  const a = agentOf(kind);
  const showEffort = a.caps.effort && (driver === "managed" || a.caps.tuiEffort);
  const showPerm = a.caps.permissionChoice;
  const skipEffective = skipPerm ?? agentLaunchDefault(settings, kind).skipPermissions;

  const go = async () => {
    if (!kind || needsRepo) return;
    setBusy(true);
    const ok = await onAttach({
      kind,
      driver,
      model,
      effort,
      dir: repo?.path || "",
      subdir: repo ? subdir : "",
      worktree: wt,
      base: wt ? base || repo?.branch || "" : "",
      ...(showPerm ? { skipPermissions: skipEffective } : {}),
    });
    setBusy(false);
    if (ok) onClose();
  };

  return (
    <Modal title={tr(replacing ? "imggen.attach_replace_title" : "imggen.attach_title")} onClose={onClose} className="igen-attach" lockClose={busy}>
      <div className="ui-modal-body">
        {replacing && <p className="igen-hint">{tr("imggen.attach_replace_note")}</p>}
        <div className="ui-field">
          <span className="ui-field-label">{tr("imggen.attach_driver")}</span>
          <div className="ui-seg">
            {(["tui", "managed"] as const).map((d) => (
              <button
                key={d}
                type="button"
                className={"seg-btn" + (driver === d ? " active" : "")}
                onClick={() => setDriver(d)}
              >
                {tr(d === "tui" ? "imggen.attach_driver_tui" : "imggen.attach_driver_managed")}
              </button>
            ))}
          </div>
        </div>
        <div className="ui-field">
          <span className="ui-field-label">{tr("launch.field.agent")}</span>
          <div className="ui-seg big igen-attach-kinds">
            {choices.map((c) => {
              const ag = agentOf(c.kind);
              const reason = c.blocked ? tr(`imggen.attach_block_${c.blocked}` as "imggen.attach_block_af_shared") : "";
              return (
                <button
                  key={c.kind}
                  type="button"
                  disabled={!!c.blocked}
                  title={reason || tr(ag.launchHintKey)}
                  data-kind={c.kind}
                  className={"seg-btn kind-" + ag.cssClass + (kind === c.kind ? " active" : "")}
                  onClick={() => setKind(c.kind)}
                >
                  <Icon name={ag.icon} className="seg-ic" />
                  {kindDisplayName(c.kind)}
                </button>
              );
            })}
          </div>
          {choices.some((c) => c.blocked) && (
            <ul className="igen-attach-blocked">
              {choices
                .filter((c) => c.blocked)
                .map((c) => (
                  <li key={c.kind}>
                    {kindDisplayName(c.kind)}: {tr(`imggen.attach_block_${c.blocked}` as "imggen.attach_block_af_shared")}
                  </li>
                ))}
            </ul>
          )}
        </div>
        {kind && a.caps.model && (
          <div className="ui-field">
            <span className="ui-field-label">{tr("launch.field.model")}</span>
            <ModelPicker
              kind={kind}
              model={model}
              onChange={(next) => {
                setModel(next);
                setEffort("");
              }}
            />
          </div>
        )}
        {kind && (showEffort || showPerm) && (
          <div className="ui-field-row">
            {showEffort && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("launch.field.effort")}</span>
                <EffortPicker kind={kind} model={model} effort={effort} onChange={setEffort} />
              </div>
            )}
            {showPerm && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("launch.field.permissions")}</span>
                <select value={skipEffective ? "skip" : "ask"} onChange={(e) => setSkipPerm(e.target.value === "skip")}>
                  <option value="skip">{tr("launch.perm_skip")}</option>
                  <option value="ask">{tr("launch.perm_ask")}</option>
                </select>
              </div>
            )}
          </div>
        )}
        <div className="ui-field">
          <span className="ui-field-label">{tr("imggen.attach_repo")}</span>
          <select
            value={repoName}
            onChange={(e) => {
              setRepoName(e.target.value);
              setSubdir("");
              setBase("");
              setBranches(null);
            }}
          >
            <option value="">{tr("imggen.attach_repo_home")}</option>
            {repos
              .filter((r) => r.path)
              .map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name}
                </option>
              ))}
          </select>
          <span className="ui-field-hint">{tr("imggen.attach_repo_hint")}</span>
        </div>
        {repo && (
          <div className="ui-field">
            <span className="ui-field-label">{tr("launch.field.subdir")}</span>
            <SubdirPicker repo={repo.name} value={subdir} onChange={setSubdir} />
          </div>
        )}
        {repo && (
          <div className="ui-field">
            <label className="igen-attach-wt">
              <input
                type="checkbox"
                checked={wt}
                disabled={!canOptOut}
                onChange={(e) => setWorktree(e.target.checked)}
              />
              {tr("imggen.attach_worktree")}
            </label>
            <span className="ui-field-hint">
              {tr(canOptOut ? "imggen.attach_worktree_hint" : "imggen.attach_worktree_required")}
            </span>
            {wt && (
              <>
                <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => setBaseOpen((v) => !v)}>
                  {tr("imggen.attach_base", { branch: base || repo.branch || "" })}
                </button>
                {baseOpen && (
                  <BranchList
                    branches={branches}
                    selected={base || repo.branch}
                    onPick={(name) => {
                      setBase(name);
                      setBaseOpen(false);
                    }}
                  />
                )}
              </>
            )}
          </div>
        )}
        {needsRepo && kind && <p className="igen-warn">{tr("imggen.attach_needs_repo")}</p>}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" onClick={() => void go()} disabled={busy || !kind || needsRepo}>
          {busy ? tr("imggen.attach_busy") : tr("imggen.attach_go")}
        </Button>
      </footer>
    </Modal>
  );
}
