// AttachAgentModal — "start a studio" / "attach an agent" (ADR 0100 decision 8). Built from the
// launch dialog's parts (ModelPicker, EffortPicker, SubdirPicker, BranchList) rather than by
// embedding the dialog, the way StartModal's home stage uses them: there is no prompt field (the
// first turn is the studio's persona), and the kind list and the worktree rule are the studio's own.
//
// One screen: the image model first (prompts are written for a model, revision 9, so nothing
// starts without one), then the agent. The agent's settings are remembered per tenant, and the
// next open shows them as one summary line + "change" — the launch dialog's folded tiers: the
// summary lists every value the start will use, so one press never launches something hidden.
//
// No pane state: the working-copy rows open it too (StartStudioModal), with the place fixed.
import { useEffect, useMemo, useRef, useState } from "react";
import { api, getTenant, isTransientErr } from "../../../core/api/client.ts";
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
import { useReposStore, type Repo } from "../../repos/store.ts";
import { useRepoRailContext } from "../../repos/useRepoRail.ts";
import { imagegenStatus, type ImagegenStatus } from "../api.ts";
import { emptyDraft } from "../draft.ts";
import { fleetProviders, resolveFleetProvider } from "../wire.ts";
import { studioKindChoices, type StudioDriver } from "../studioSync.ts";
import { defaultWorktree, lastUsable, orderedPlaces, planPlace, readAttachLast, writeAttachLast } from "../attachPlan.ts";
import { ModelSelect } from "./GenerateForm.tsx";
// The working-copy rows open this without the pane, so its styles cannot ride on the view's import.
import "../imagegen.css";

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
  /** The image model the studio's draft is set to, and its fleet row. */
  imageProvider: string;
  imageModel: string;
  /** The working copy's name as the member picked it ("" = home): the default studio title. */
  place: string;
}

export function AttachAgentModal({
  replacing,
  initialImage,
  fixedRepo,
  onClose,
  onAttach,
}: {
  /** The session being replaced ("switch agents"), shown so the member knows it is stopped. */
  replacing?: string;
  /** The pane's current image model: the starting pick over the remembered one. */
  initialImage?: { providerId: string; model: string };
  /** Opened from a working-copy row: the place starts as that copy, shown as a summary. */
  fixedRepo?: Repo;
  onClose: () => void;
  onAttach: (o: AttachOpts) => Promise<boolean>;
}) {
  const tr = useT();
  const settings = useSettings();
  const ctx = useRepoRailContext();
  const repos = useReposStore((s) => s.repos);
  const tenant = getTenant();

  // Read once: the summary is what was remembered when the dialog opened.
  const [last] = useState(() => {
    const l = readAttachLast(tenant);
    // The connection check is still out on the first render, so only the kind's shape is
    // checked here; a kind the check then rules out unfolds the dialog below.
    const offered = (k: string, d: StudioDriver) => studioKindChoices(d).some((c) => c.kind === k && !c.blocked);
    return lastUsable(l, { offered, repos, fixedRepo: !!fixedRepo }) ? l : null;
  });
  const [folded, setFolded] = useState(!!last);

  const [driver, setDriver] = useState<StudioDriver>(last?.driver || "tui");
  const choices = useMemo(() => studioKindChoices(driver, ctx.launchKinds), [driver, ctx.launchKinds]);
  const firstOpen = choices.find((c) => !c.blocked)?.kind || "";
  const [kind, setKind] = useState<string>(last?.kind || firstOpen);
  const [model, setModel] = useState(last?.model || "");
  const [effort, setEffort] = useState(last?.effort || "");
  const [skipPerm, setSkipPerm] = useState<boolean | undefined>(last?.skipPermissions);
  // The kind the remembered model/effort belong to: its first "kind changed" run keeps them.
  const seeded = useRef<string>(last?.kind || "");

  const [placeFixed, setPlaceFixed] = useState(!!fixedRepo);
  const [repoName, setRepoName] = useState(fixedRepo?.name ?? last?.repo ?? "");
  const [subdir, setSubdir] = useState("");
  const [worktree, setWorktree] = useState(() =>
    fixedRepo ? defaultWorktree(fixedRepo) : last ? last.worktree : true,
  );
  const [base, setBase] = useState("");
  const [branches, setBranches] = useState<Branch[] | null>(null);
  const [baseOpen, setBaseOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  // The image model: the pane's, else the remembered one, checked against the fleet once the
  // status is in. A pick the fleet no longer offers is dropped rather than sent.
  const [status, setStatus] = useState<ImagegenStatus | null>(null);
  const [statusFailed, setStatusFailed] = useState(false);
  const [image, setImage] = useState(() =>
    initialImage?.model
      ? { providerId: initialImage.providerId, model: initialImage.model }
      : { providerId: last?.imageProvider || "", model: last?.imageModel || "" },
  );
  useEffect(() => {
    let alive = true;
    imagegenStatus()
      .then((s) => {
        if (!alive) return;
        if (!s || isTransientErr(s)) setStatusFailed(true);
        else setStatus(s);
      })
      .catch(() => alive && setStatusFailed(true));
    return () => {
      alive = false;
    };
  }, []);
  const providers = useMemo(() => fleetProviders(status), [status]);
  const provider = resolveFleetProvider(providers, image.providerId);
  const models = provider?.models || [];
  const imageOk = !!image.model && models.some((m) => m.id === image.model);
  useEffect(() => {
    if (!status) return;
    // A named engine that is gone is not replaced by the first one left: model ids overlap across
    // fleet rows, so the same id there is another engine. Ask again, like a vanished working copy.
    if (image.providerId && !providers.some((p) => p.id === image.providerId)) {
      setImage({ providerId: "", model: "" });
      setFolded(false);
      return;
    }
    if (image.model && !models.some((m) => m.id === image.model)) setImage((i) => ({ ...i, model: "" }));
  }, [status, providers, models, image.providerId, image.model]);

  // Keep the kind among the offered ones when the execution method (or the connections) change.
  // Not while the connection check is out: its empty list would wipe a remembered kind.
  useEffect(() => {
    if (ctx.connsSettling) return;
    if (!choices.some((c) => c.kind === kind && !c.blocked)) {
      setKind(firstOpen);
      setFolded(false);
    }
  }, [choices, kind, firstOpen, ctx.connsSettling]);
  useEffect(() => {
    if (!kind) return;
    if (seeded.current === kind) {
      seeded.current = "";
      return;
    }
    seeded.current = "";
    const d = agentLaunchDefault(settings, kind);
    setModel(resolveModel(kind, "", d.model));
    setEffort(resolveEffort(kind, "", d.effort));
    setSkipPerm(undefined);
    // Only on a kind change: re-running on every settings sync would reset a picked model.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  const repo = (placeFixed ? fixedRepo : repos.find((r) => r.name === repoName)) || null;
  const plan = planPlace({ repo, repos, kind, driver, want: worktree, base });
  const places = useMemo(() => orderedPlaces(repos), [repos]);

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

  const pickRepo = (name: string) => {
    setRepoName(name);
    setSubdir("");
    setBase("");
    setBranches(null);
    setWorktree(defaultWorktree(repos.find((r) => r.name === name) || null));
  };

  const placeLabel = (r: Repo | null): string => {
    if (!r) return tr("imggen.start_title_home");
    if (!plan.worktree) return tr("imggen.start_sum_in_place", { place: r.name });
    return tr("imggen.start_sum_new_wt", { place: r.name, base: plan.base || tr("launch.base_default") });
  };
  const summary = [
    kindDisplayName(kind),
    tr(driver === "tui" ? "imggen.attach_driver_tui" : "imggen.attach_driver_managed"),
    a.caps.model ? model || tr("imggen.start_sum_model_default") : "",
    showEffort && effort ? tr("launch.sum.effort", { v: effort }) : "",
    showPerm && !skipEffective ? tr("launch.sum.permissions_on") : "",
    placeLabel(repo) + (repo && subdir ? ` / ${subdir}` : ""),
  ]
    .filter(Boolean)
    .join(" · ");

  const blocked = !kind || !!plan.blocked;
  // A remembered choice this place cannot host is shown open, with the reason, not folded.
  const showFolded = folded && !plan.blocked && !plan.wantUnmet;
  const go = async () => {
    if (blocked || !imageOk) return;
    setBusy(true);
    const perm = showPerm ? { skipPermissions: skipEffective } : {};
    const ok = await onAttach({
      kind,
      driver,
      model,
      effort,
      dir: plan.dir,
      subdir: repo ? subdir : "",
      worktree: plan.worktree,
      base: plan.base,
      ...perm,
      imageProvider: provider?.id || "",
      imageModel: image.model,
      place: repo?.name || "",
    });
    setBusy(false);
    if (!ok) return;
    writeAttachLast(tenant, {
      driver,
      kind,
      model,
      effort,
      ...perm,
      // A place fixed by the row it was opened from is that row's choice, not a preference.
      repo: fixedRepo && placeFixed ? (last?.repo ?? "") : repo?.name || "",
      worktree: fixedRepo && placeFixed ? (last?.worktree ?? true) : worktree,
      imageProvider: provider?.id || "",
      imageModel: image.model,
    });
    onClose();
  };

  return (
    <Modal
      title={tr(replacing ? "imggen.attach_replace_title" : fixedRepo ? "imggen.start_title" : "imggen.attach_title")}
      onClose={onClose}
      className="igen-attach"
      lockClose={busy}
    >
      <div className="ui-modal-body">
        {replacing && <p className="igen-hint">{tr("imggen.attach_replace_note")}</p>}
        <div className="ui-field igen-attach-image">
          {status ? (
            providers.length ? (
              <ModelSelect
                draft={{ ...emptyDraft(), providerId: provider?.id || "", model: image.model }}
                patch={(p) => setImage((i) => ({ providerId: p.providerId ?? i.providerId, model: p.providerId !== undefined ? "" : (p.model ?? i.model) }))}
                fleetProviders={providers}
                provider={provider}
                models={models}
                modelLabel={tr("imggen.start_image_model")}
              />
            ) : (
              <p className="igen-warn">{tr("imggen.engine_unavailable")}</p>
            )
          ) : statusFailed ? (
            <p className="igen-warn">{tr("imggen.engine_unavailable")}</p>
          ) : (
            <span className="igen-hint">
              <Icon name="loading" spin /> {tr("imggen.start_models_loading")}
            </span>
          )}
          {status && providers.length > 0 && !imageOk && <span className="ui-field-hint">{tr("imggen.start_needs_model")}</span>}
        </div>
        {showFolded ? (
          <div className="igen-attach-last">
            <span className="igen-attach-last-label">{tr("imggen.start_same_as_last")}</span>
            <span className="igen-attach-last-sum" data-testid="attach-summary">
              {summary}
            </span>
            <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => setFolded(false)}>
              {tr("launch.sec_edit")}
            </button>
          </div>
        ) : (
          <>
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
                <span className="ui-field-label">{tr("imggen.start_agent_model")}</span>
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
              {placeFixed && fixedRepo ? (
                <div className="igen-attach-place">
                  <span className="igen-attach-place-name" data-testid="attach-place">
                    <Icon name={fixedRepo.worktree ? "git-branch" : "repo"} /> {fixedRepo.name}
                  </span>
                  <button
                    type="button"
                    className="ui-btn ui-btn-ghost ui-btn-sm"
                    onClick={() => {
                      setPlaceFixed(false);
                      setRepoName(fixedRepo.name);
                    }}
                  >
                    {tr("imggen.start_other_place")}
                  </button>
                </div>
              ) : (
                <select value={repoName} onChange={(e) => pickRepo(e.target.value)}>
                  <option value="">{tr("imggen.attach_repo_home")}</option>
                  {places.map(({ repo: r, nested }) => (
                    <option key={r.name} value={r.name}>
                      {nested ? `　└ ${r.name}` : r.name}
                    </option>
                  ))}
                </select>
              )}
              <span className="ui-field-hint">{tr("imggen.attach_repo_hint")}</span>
            </div>
            {repo && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("launch.field.subdir")}</span>
                <SubdirPicker repo={repo.name} value={subdir} onChange={setSubdir} />
              </div>
            )}
            {repo && !plan.blocked && (plan.worktreeChoice || plan.worktree) && (
              <div className="ui-field">
                <label className="igen-attach-wt">
                  <input
                    type="checkbox"
                    checked={plan.worktree}
                    disabled={!plan.worktreeChoice}
                    onChange={(e) => setWorktree(e.target.checked)}
                  />
                  {tr("imggen.attach_worktree")}
                </label>
                <span className="ui-field-hint">
                  {plan.forcedFromWorktree
                    ? tr("imggen.start_wt_forced", { branch: plan.base })
                    : repo.worktree && plan.worktree
                      ? tr("imggen.start_wt_from_branch", { branch: plan.base })
                      : tr(plan.worktreeChoice ? "imggen.attach_worktree_hint" : "imggen.attach_worktree_required")}
                </span>
                {plan.worktree && !repo.worktree && (
                  <>
                    <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => setBaseOpen((v) => !v)}>
                      {tr("imggen.attach_base", { branch: plan.base })}
                    </button>
                    {baseOpen && (
                      <BranchList
                        branches={branches}
                        selected={plan.base}
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
          </>
        )}
        {plan.blocked === "needs_repo" && kind && <p className="igen-warn">{tr("imggen.attach_needs_repo")}</p>}
        {plan.blocked === "no_worktree" && kind && <p className="igen-warn">{tr("imggen.start_no_worktree")}</p>}
        {plan.blocked === "no_parent" && kind && <p className="igen-warn">{tr("imggen.start_no_parent")}</p>}
        {plan.wantUnmet && kind && <p className="igen-warn">{tr("imggen.start_no_parent_in_place")}</p>}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" onClick={() => void go()} disabled={busy || blocked || !imageOk}>
          {busy ? tr("imggen.attach_busy") : tr(fixedRepo && !replacing ? "imggen.start_go" : "imggen.attach_go")}
        </Button>
      </footer>
    </Modal>
  );
}
