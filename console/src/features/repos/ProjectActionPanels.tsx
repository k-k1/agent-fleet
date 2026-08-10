// ProjectActionPanels — docs/56 §9.2 ③ / §7.5 操作A: the two write actions P1
// ships. Both follow the same shape: build one Op from form state, plan() to show
// a masked preview + warnings, apply() only once the user has seen that preview
// (docs/56 §5 "マージは利用者が決める" — this UI never plans-and-applies in one
// click). A field change after a plan clears it, forcing a fresh preview before
// apply is allowed again — the previewed op and the applied op must be the exact
// op the user is looking at.
import { useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { errText } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Field } from "../settings/mcpForm.tsx";
import { kindLabel } from "../../lib/sessionkind.ts";
import { openFileDiff } from "../scm/open.ts";
import {
  applyProjectMcp,
  gateText,
  opErrorText,
  planProjectMcp,
  targetHasEntry,
  warningText,
} from "./projectMcpWire.ts";
import type { DialectChoice, OnConflict, ProjectOp, ProjectPlanResult, ProjectServer, ProjectSnapshot } from "./projectMcpWire.ts";

type ApiResult<T> = T | { error: { code: string; message?: string } };

async function call<T>(fn: () => Promise<T>): Promise<ApiResult<T>> {
  try {
    return await fn();
  } catch {
    return { error: { code: "network" } };
  }
}

// --- copy ----------------------------------------------------------------

interface CopyPanelProps {
  repo: string;
  snap: ProjectSnapshot;
  source: { file: string; name: string };
  onClose: () => void;
  onApplied: () => void;
}

export function ProjectCopyPanel({ repo, snap, source, onClose, onApplied }: CopyPanelProps) {
  const tr = useT();
  const toast = useToast();
  const otherFiles = snap.files.filter((f) => f.path !== source.file);

  const [toFile, setToFile] = useState(otherFiles[0]?.path || "");
  const [asName, setAsName] = useState(source.name);
  const [dialect, setDialect] = useState<DialectChoice>("translate");
  const [withSecrets, setWithSecrets] = useState(false);
  const [onConflict, setOnConflict] = useState<OnConflict>("overwrite");
  const [plan, setPlan] = useState<ProjectPlanResult | null>(null);
  const [applied, setApplied] = useState<ProjectPlanResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const resolvedAs = asName.trim() || source.name;
  const toFileObj = snap.files.find((f) => f.path === toFile);
  const destKind = toFileObj?.kinds[0];
  const destKindInfo = snap.kinds.find((k) => k.kind === destKind);
  const canTranslate = !!destKindInfo?.dialects?.length;
  const effectiveDialect: DialectChoice = canTranslate ? dialect : "as-is";
  const hasConflict = !!toFile && targetHasEntry(snap.files, toFile, resolvedAs);

  const clearPlan = () => {
    setPlan(null);
    setErr("");
  };

  const buildOp = (): ProjectOp => ({
    op: "copy",
    from: { file: source.file, name: source.name },
    to: { file: toFile },
    as: resolvedAs,
    onConflict: hasConflict ? onConflict : "overwrite",
    withSecrets,
    dialect: effectiveDialect,
  });

  const doPlan = async () => {
    if (!toFile) return;
    setBusy(true);
    setErr("");
    const res = await call(() => planProjectMcp(repo, [buildOp()]));
    setBusy(false);
    if ("error" in res) {
      setErr(errText(res.error));
      return;
    }
    setPlan(res);
  };

  const doApply = async () => {
    if (!plan) return;
    setBusy(true);
    setErr("");
    const res = await call(() => applyProjectMcp(repo, [buildOp()], plan.planHash));
    setBusy(false);
    if ("error" in res) {
      if (res.error.code === "mcp_project_plan_stale") {
        setErr(tr("pmcp.plan_stale"));
        setPlan(null);
      } else {
        setErr(errText(res.error));
      }
      return;
    }
    const opRes = res.ops[0];
    if (opRes.status === "error") {
      toast(opErrorText(opRes.reason));
      return;
    }
    if (opRes.status === "skipped") {
      toast(tr("pmcp.op_skipped_toast"));
      onClose();
      return;
    }
    setApplied(res);
    onApplied();
  };

  if (applied) {
    const opRes = applied.ops[0];
    const gate = gateText(opRes.gateCode);
    return (
      <div className="pmcp-panel pmcp-panel-done">
        <p className="pmcp-panel-title">
          <Icon name="check" /> {tr("pmcp.applied", { file: opRes.file || toFile })}
        </p>
        {gate && (
          <p className="ps-note">
            <Icon name="clock" /> {gate}
          </p>
        )}
        <div className="pmcp-panel-actions">
          <Button
            variant="ghost"
            icon="source-control"
            onClick={() => {
              openFileDiff(repo, opRes.file || toFile, false);
              onClose();
            }}
          >
            {tr("pmcp.view_in_scm")}
          </Button>
          <Button onClick={onClose}>{tr("ui.close")}</Button>
        </div>
      </div>
    );
  }

  return (
    <div className="pmcp-panel">
      <p className="pmcp-panel-title">{tr("pmcp.copy_title", { server: source.name, file: source.file })}</p>
      <Field label={tr("pmcp.copy_to")}>
        <select
          className="cinput"
          value={toFile}
          onChange={(e) => {
            setToFile(e.target.value);
            clearPlan();
          }}
        >
          {otherFiles.map((f) => (
            <option key={f.path} value={f.path}>
              {f.path}
            </option>
          ))}
        </select>
      </Field>
      <Field label={tr("pmcp.copy_as")}>
        <input
          className="cinput"
          value={asName}
          onChange={(e) => {
            setAsName(e.target.value);
            clearPlan();
          }}
        />
      </Field>
      {hasConflict && (
        <Field label={tr("pmcp.on_conflict")}>
          <select
            className="cinput"
            value={onConflict}
            onChange={(e) => {
              setOnConflict(e.target.value as OnConflict);
              clearPlan();
            }}
          >
            <option value="overwrite">{tr("pmcp.on_conflict_overwrite")}</option>
            <option value="skip">{tr("pmcp.on_conflict_skip")}</option>
            <option value="rename">{tr("pmcp.on_conflict_rename")}</option>
          </select>
        </Field>
      )}
      <Field label={tr("pmcp.dialect_label")}>
        <div className="pmcp-radio-group">
          {canTranslate && (
            <label>
              <input
                type="radio"
                checked={dialect === "translate"}
                onChange={() => {
                  setDialect("translate");
                  clearPlan();
                }}
              />{" "}
              {tr("pmcp.dialect_translate")}
            </label>
          )}
          <label>
            <input
              type="radio"
              checked={effectiveDialect === "as-is"}
              onChange={() => {
                setDialect("as-is");
                clearPlan();
              }}
            />{" "}
            {tr("pmcp.dialect_as_is")}
          </label>
          <label>
            <input
              type="radio"
              checked={dialect === "expand"}
              onChange={() => {
                setDialect("expand");
                clearPlan();
              }}
            />{" "}
            {tr("pmcp.dialect_expand")}
          </label>
        </div>
        {!canTranslate && destKind && <div className="hint">{tr("pmcp.dialect_none_hint", { kind: kindLabel(destKind) })}</div>}
      </Field>
      <Field label={tr("pmcp.with_secrets")}>
        <label>
          <input
            type="checkbox"
            checked={withSecrets}
            onChange={(e) => {
              setWithSecrets(e.target.checked);
              clearPlan();
            }}
          />{" "}
          {tr("pmcp.with_secrets_label")}
        </label>
        {withSecrets && <div className="hint pmcp-warn-hint">{tr("pmcp.with_secrets_warn")}</div>}
      </Field>

      {err && <p className="ps-note ps-note-warn">{err}</p>}

      {plan && (
        <div className="pmcp-preview">
          {plan.ops[0].status === "skipped" ? (
            <p className="ps-note">{tr("pmcp.op_skipped_toast")}</p>
          ) : (
            <>
              <p className="pmcp-panel-title">{tr("pmcp.preview_title")}</p>
              <PreviewDiff before={plan.ops[0].before} after={plan.ops[0].after} />
              {gateText(plan.ops[0].gateCode) && (
                <p className="ps-note">
                  <Icon name="clock" /> {gateText(plan.ops[0].gateCode)}
                </p>
              )}
              {plan.warnings?.map((w, i) => (
                <p key={i} className={"pmcp-warning " + w.severity}>
                  <Icon name={w.severity === "red" ? "error" : "warning"} /> {warningText(w)}
                </p>
              ))}
            </>
          )}
        </div>
      )}

      <div className="pmcp-panel-actions">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("ui.cancel")}
        </Button>
        {!plan ? (
          <Button onClick={doPlan} disabled={busy || !toFile}>
            {tr("pmcp.preview_action")}
          </Button>
        ) : (
          <Button onClick={doApply} disabled={busy || plan.ops[0].status === "skipped"}>
            {tr("pmcp.apply_action")}
          </Button>
        )}
      </div>
    </div>
  );
}

function PreviewDiff({ before, after }: { before?: ProjectServer; after?: ProjectServer }) {
  const tr = useT();
  return (
    <div className="pmcp-diff">
      {before && (
        <div className="pmcp-diff-side del">
          <div className="pmcp-diff-h">{tr("pmcp.diff_before")}</div>
          <pre>{JSON.stringify(before, null, 2)}</pre>
        </div>
      )}
      {after && (
        <div className="pmcp-diff-side add">
          <div className="pmcp-diff-h">{tr("pmcp.diff_after")}</div>
          <pre>{JSON.stringify(after, null, 2)}</pre>
        </div>
      )}
    </div>
  );
}

// --- ignore ----------------------------------------------------------------

interface IgnorePanelProps {
  repo: string;
  file: string;
  onClose: () => void;
}

export function ProjectIgnorePanel({ repo, file, onClose }: IgnorePanelProps) {
  const tr = useT();
  const toast = useToast();
  const [where, setWhere] = useState<"exclude" | "gitignore">("exclude");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const doAdd = async () => {
    setBusy(true);
    setErr("");
    const op: ProjectOp = { op: "ignore", file, where };
    const planRes = await call(() => planProjectMcp(repo, [op]));
    if ("error" in planRes) {
      setBusy(false);
      setErr(errText(planRes.error));
      return;
    }
    const applyRes = await call(() => applyProjectMcp(repo, [op], planRes.planHash));
    setBusy(false);
    if ("error" in applyRes) {
      setErr(errText(applyRes.error));
      return;
    }
    const opRes = applyRes.ops[0];
    if (opRes.status === "error") {
      toast(opErrorText(opRes.reason));
      return;
    }
    toast(opRes.alreadyPresent ? tr("pmcp.ignore_already") : tr("pmcp.ignore_added"), { kind: "success" });
    onClose();
  };

  return (
    <div className="pmcp-panel">
      <p className="pmcp-panel-title">{tr("pmcp.ignore_title", { file })}</p>
      <Field label={tr("pmcp.ignore_where")}>
        <div className="pmcp-radio-group">
          <label>
            <input type="radio" checked={where === "exclude"} onChange={() => setWhere("exclude")} /> {tr("pmcp.ignore_exclude")}
          </label>
          <label>
            <input type="radio" checked={where === "gitignore"} onChange={() => setWhere("gitignore")} /> {tr("pmcp.ignore_gitignore")}
          </label>
        </div>
        <div className="hint">{where === "exclude" ? tr("pmcp.ignore_exclude_hint") : tr("pmcp.ignore_gitignore_hint")}</div>
      </Field>
      {err && <p className="ps-note ps-note-warn">{err}</p>}
      <div className="pmcp-panel-actions">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("ui.cancel")}
        </Button>
        <Button onClick={doAdd} disabled={busy}>
          {tr("pmcp.ignore_add_action")}
        </Button>
      </div>
    </div>
  );
}
