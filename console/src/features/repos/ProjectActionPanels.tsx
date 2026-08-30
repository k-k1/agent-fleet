// ProjectActionPanels — docs/log/56 §9.2 ③ / §7.5 操作A: the two write actions P1
// ships. Copy is deliberately ONE decision (which agent) plus one optional toggle
// (copy secret values too) — not a form: dialect and overwrite-vs-skip are decided
// FOR the user from the destination's own known contract (translate when the
// target kind has a native dialect, overwrite when a same-named entry already
// exists — docs/log/56 §5 still requires a preview before every write, so the user
// sees exactly what will land and can cancel, but is never asked to pick options
// that have one obviously-right answer). plan() computes that preview; apply()
// only runs once the user has looked at it and confirmed.
import { useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { errText } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Field, Meta } from "../settings/mcpForm.tsx";
import { kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { openFileDiff } from "../scm/open.ts";
import { applyProjectMcp, gateText, opErrorText, planProjectMcp, warningText } from "./projectMcpWire.ts";
import type { ProjectFile, ProjectOp, ProjectPlanResult, ProjectServer, ProjectSnapshot } from "./projectMcpWire.ts";

type ApiResult<T> = T | { error: { code: string; message?: string } };

async function call<T>(fn: () => Promise<T>): Promise<ApiResult<T>> {
  try {
    return await fn();
  } catch {
    return { error: { code: "network" } };
  }
}

// --- copy ----------------------------------------------------------------

// The agent kinds a copy may target — kiro is read-only (docs/log/56 §4.3, its
// project-scope write contract is unmeasured) and agy has no project scope at
// all, so neither is offered as a destination.
const COPY_TARGET_KINDS = ["claude", "codex", "opencode", "cursor", "copilot"];

// v1's file↔kind map is fixed (docs/log/56 §4.3): each kind reads exactly one project
// file (copilot has a second, .github/mcp.json, but .mcp.json — listed first — is
// its documented default when both could apply). Picking by KIND here, not by raw
// path, is the whole point of this redesign — the file is an implementation detail
// the destination-agent choice already determines.
function fileForKind(files: ProjectFile[], kind: string): string | undefined {
  return files.find((f) => f.kinds.includes(kind))?.path;
}

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
  const sourceFile = snap.files.find((f) => f.path === source.file);
  const sourceKinds = new Set(sourceFile?.kinds || []);

  const targets = COPY_TARGET_KINDS.map((kind) => ({ kind, file: fileForKind(snap.files, kind) })).filter(
    (t): t is { kind: string; file: string } => !!t.file && !sourceKinds.has(t.kind),
  );

  const [targetKind, setTargetKind] = useState<string | null>(null);
  const [withSecrets, setWithSecrets] = useState(false);
  const [plan, setPlan] = useState<ProjectPlanResult | null>(null);
  const [applied, setApplied] = useState<ProjectPlanResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const opFor = (kind: string, secrets: boolean): ProjectOp | null => {
    const file = targets.find((t) => t.kind === kind)?.file;
    if (!file) return null;
    const canTranslate = !!snap.kinds.find((k) => k.kind === kind)?.dialects?.length;
    return {
      op: "copy",
      from: { file: source.file, name: source.name },
      to: { file },
      as: source.name,
      onConflict: "overwrite",
      withSecrets: secrets,
      dialect: canTranslate ? "translate" : "as-is",
    };
  };

  const runPlan = async (kind: string, secrets: boolean) => {
    const op = opFor(kind, secrets);
    if (!op) return;
    setBusy(true);
    setErr("");
    const res = await call(() => planProjectMcp(repo, [op]));
    setBusy(false);
    if ("error" in res) {
      setErr(errText(res.error));
      return;
    }
    setPlan(res);
  };

  const pickTarget = (kind: string) => {
    setTargetKind(kind);
    setPlan(null);
    setErr("");
    void runPlan(kind, withSecrets);
  };

  const toggleSecrets = () => {
    const next = !withSecrets;
    setWithSecrets(next);
    if (targetKind) void runPlan(targetKind, next);
  };

  const doApply = async () => {
    if (!plan || !targetKind) return;
    const op = opFor(targetKind, withSecrets);
    if (!op) return;
    setBusy(true);
    setErr("");
    const res = await call(() => applyProjectMcp(repo, [op], plan.planHash));
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
    setApplied(res);
    onApplied();
  };

  if (applied) {
    const opRes = applied.ops[0];
    const gate = gateText(opRes.gateCode);
    return (
      <div className="pmcp-panel pmcp-panel-done">
        <p className="pmcp-panel-title">
          <Icon name="check" /> {tr("pmcp.applied", { file: opRes.file || "" })}
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
              if (opRes.file) openFileDiff(repo, opRes.file, false);
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

      {!targetKind ? (
        <>
          <div className="pmcp-kind-chips">
            {targets.map((t) => (
              <button key={t.kind} type="button" className="pmcp-kind-chip-btn" onClick={() => pickTarget(t.kind)}>
                <Icon name={kindIcon(t.kind)} /> {kindLabel(t.kind)}
              </button>
            ))}
          </div>
          {targets.length === 0 && <p className="ps-note">{tr("pmcp.copy_no_targets")}</p>}
          <div className="pmcp-panel-actions">
            <Button variant="ghost" onClick={onClose}>
              {tr("ui.cancel")}
            </Button>
          </div>
        </>
      ) : (
        <>
          <p className="pmcp-copy-target">
            <button type="button" className="pmcp-link-btn" onClick={() => setTargetKind(null)}>
              <Icon name="arrow-left" /> {tr("pmcp.copy_change_target")}
            </button>
            <span>
              <Icon name={kindIcon(targetKind)} /> {kindLabel(targetKind)}
            </span>
          </p>

          {err && <p className="ps-note ps-note-warn">{err}</p>}
          {busy && !plan && <p className="ps-note">{tr("common.loading")}</p>}

          {plan && (
            <div className="pmcp-preview">
              {plan.ops[0].before && (
                <p className="ps-note ps-note-warn">
                  <Icon name="warning" /> {tr("pmcp.copy_will_overwrite", { file: plan.ops[0].file || "" })}
                </p>
              )}
              {plan.ops[0].after && <ServerSummary s={plan.ops[0].after} />}
              <label className="pmcp-secrets-toggle">
                <input type="checkbox" checked={withSecrets} onChange={toggleSecrets} /> {tr("pmcp.with_secrets_label")}
              </label>
              {withSecrets && <div className="hint pmcp-warn-hint">{tr("pmcp.with_secrets_warn")}</div>}
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
            </div>
          )}

          <div className="pmcp-panel-actions">
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              {tr("ui.cancel")}
            </Button>
            <Button onClick={doApply} disabled={busy || !plan}>
              {tr("pmcp.apply_action")}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

// ServerSummary — plain key/value rows (docs/log/56 §9.2's "プレビュー", not a raw JSON
// dump): the command/URL as one line, env/header VALUE NAMES only (values stay
// masked or absent — this is a preview, not an editor).
function ServerSummary({ s }: { s: ProjectServer }) {
  const tr = useT();
  return (
    <div className="pmcp-summary">
      {s.transport === "http" ? (
        <Meta k={tr("pmcp.summary_url")} v={s.url} />
      ) : (
        <Meta k={tr("pmcp.summary_command")} v={[s.command, ...(s.args || [])].filter(Boolean).join(" ")} />
      )}
      {s.env && Object.keys(s.env).length > 0 && <Meta k={tr("pmcp.summary_env")} v={Object.keys(s.env).join(", ")} />}
      {s.headers && Object.keys(s.headers).length > 0 && <Meta k={tr("pmcp.summary_headers")} v={Object.keys(s.headers).join(", ")} />}
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
