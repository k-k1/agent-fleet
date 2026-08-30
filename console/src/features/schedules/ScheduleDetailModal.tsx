// ScheduleDetailModal (docs/log/38 P5.2) — view a schedule's full detail and edit the
// structured, no-NL-needed fields (label / prompt / timing spec+tz / wake policy /
// agent+model). Reached from the row's ⋯ menu. The advanced execution fields
// (session_mode / reuse / rotation / repo / worktree) are shown read-only: those are
// changed by asking the operator in chat (ADR0021 decision 7 — the NL->spec translation
// lives with the operator; the Console edits structured fields directly).
//
// Only the fields the user actually changed are PATCHed, so re-saving an unchanged spec
// never resets next_run (an interval's initialNextRun is now+interval). The CP validates
// the spec/enums and recomputes next_run when the timing changed.
import { useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { scheduleUpdate } from "./api.ts";
import { type ScheduleDTO, type ScheduleEditable, scheduleTitle } from "./read.ts";

// Known agent kinds for the edit picker; the schedule's current kind is always included so
// an unfamiliar value is never silently dropped from the select.
const AGENT_KINDS = ["claude", "codex", "opencode", "copilot", "cursor", "kiro"];
const SPEC_KINDS = ["cron", "interval", "once"];
const WAKE_POLICIES = ["wake", "skip", "catch_up"];

interface Props {
  s: ScheduleDTO;
  onClose: () => void;
  onSaved: (dto: ScheduleDTO) => void;
}

export function ScheduleDetailModal({ s, onClose, onSaved }: Props) {
  const tr = useT();
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  // Edit state, seeded from the schedule. Prompt is kept verbatim (leading/trailing space
  // can matter); the rest are trimmed by the CP on save.
  const [specKind, setSpecKind] = useState(s.spec_kind || "cron");
  const [spec, setSpec] = useState(s.spec || "");
  const [tz, setTz] = useState(s.tz || "");
  const [label, setLabel] = useState(s.spec_label || "");
  const [prompt, setPrompt] = useState(s.prompt || "");
  const [wake, setWake] = useState(s.wake_policy || "wake");
  const [agent, setAgent] = useState(s.agent_kind || "claude");
  const [model, setModel] = useState(s.model || "");
  const [report, setReport] = useState(!!s.report);

  const kinds = useMemo(
    () => (AGENT_KINDS.includes(agent) ? AGENT_KINDS : [agent, ...AGENT_KINDS]),
    [agent],
  );

  // Build the patch of only-changed fields (undefined = unchanged → omitted from the body).
  const patch = useMemo<ScheduleEditable>(() => {
    const p: ScheduleEditable = {};
    if (specKind !== (s.spec_kind || "cron")) p.spec_kind = specKind;
    if (spec !== (s.spec || "")) p.spec = spec;
    if (tz !== (s.tz || "")) p.tz = tz;
    if (label !== (s.spec_label || "")) p.spec_label = label;
    if (prompt !== (s.prompt || "")) p.prompt = prompt;
    if (wake !== (s.wake_policy || "wake")) p.wake_policy = wake;
    if (agent !== (s.agent_kind || "claude")) p.agent_kind = agent;
    if (model !== (s.model || "")) p.model = model;
    if (report !== !!s.report) p.report = report;
    return p;
  }, [s, specKind, spec, tz, label, prompt, wake, agent, model, report]);

  const dirty = Object.keys(patch).length > 0;
  const canSave = dirty && !busy && prompt.trim().length > 0 && spec.trim().length > 0;

  const save = async () => {
    if (!canSave) return;
    setBusy(true);
    try {
      const res = await scheduleUpdate(s.id, patch);
      if (res && typeof res === "object" && "id" in res) {
        onSaved(res as ScheduleDTO);
        toast(t("sched.saved"), { kind: "success" });
        onClose();
        return;
      }
      const err = (res as { error?: unknown })?.error;
      toast(errText(err as never) || t("sched.action_failed"), { kind: "warn" });
    } catch {
      toast(t("sched.action_failed"), { kind: "warn" });
    } finally {
      setBusy(false);
    }
  };

  const specHint =
    specKind === "interval"
      ? tr("sched.spec_hint_interval")
      : specKind === "once"
        ? tr("sched.spec_hint_once")
        : tr("sched.spec_hint_cron");

  return (
    <Modal
      title={tr("sched.detail_title", { name: scheduleTitle(s) })}
      onClose={onClose}
      className="sched-detail-modal"
      as="form"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field">
          <label className="ui-field-label" htmlFor="sched-label">
            {tr("sched.f_label")}
          </label>
          <input
            id="sched-label"
            type="text"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder={tr("sched.f_label_ph")}
          />
        </div>

        <div className="ui-field-row">
          <div className="ui-field sched-narrow">
            <label className="ui-field-label" htmlFor="sched-kind">
              {tr("sched.f_spec_kind")}
            </label>
            <select id="sched-kind" value={specKind} onChange={(e) => setSpecKind(e.target.value)}>
              {SPEC_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
          <div className="ui-field">
            <label className="ui-field-label" htmlFor="sched-spec">
              {tr("sched.f_spec")}
            </label>
            <input id="sched-spec" type="text" value={spec} onChange={(e) => setSpec(e.target.value)} spellCheck={false} />
            <div className="ui-field-hint">{specHint}</div>
          </div>
        </div>

        <div className="ui-field-row">
          <div className="ui-field">
            <label className="ui-field-label" htmlFor="sched-tz">
              {tr("sched.f_tz")}
            </label>
            <input id="sched-tz" type="text" value={tz} onChange={(e) => setTz(e.target.value)} placeholder="UTC" spellCheck={false} />
          </div>
          <div className="ui-field sched-narrow">
            <label className="ui-field-label" htmlFor="sched-wake">
              {tr("sched.f_wake")}
            </label>
            <select id="sched-wake" value={wake} onChange={(e) => setWake(e.target.value)}>
              {WAKE_POLICIES.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
        </div>

        <div className="ui-field-row">
          <div className="ui-field sched-narrow">
            <label className="ui-field-label" htmlFor="sched-agent">
              {tr("sched.f_agent")}
            </label>
            <select id="sched-agent" value={agent} onChange={(e) => setAgent(e.target.value)}>
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
          <div className="ui-field">
            <label className="ui-field-label" htmlFor="sched-model">
              {tr("sched.f_model")}
            </label>
            <input id="sched-model" type="text" value={model} onChange={(e) => setModel(e.target.value)} placeholder={tr("sched.f_model_ph")} spellCheck={false} />
          </div>
        </div>

        {/* Completion-report opt-in (default off). Hidden for session_mode=assistant —
            there the fire itself lands in the conversation, so a report is meaningless. */}
        {s.session_mode !== "assistant" && (
          <label className="ui-field" style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
            <input type="checkbox" checked={report} onChange={(e) => setReport(e.target.checked)} />
            <span>
              {tr("sched.f_report")}
              <span className="ui-field-hint"> — {tr("sched.f_report_hint")}</span>
            </span>
          </label>
        )}

        <div className="ui-field">
          <label className="ui-field-label" htmlFor="sched-prompt">
            {tr("sched.f_prompt")}
          </label>
          <textarea
            id="sched-prompt"
            className="sched-prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            spellCheck={false}
            rows={6}
          />
        </div>

        {/* Read-only: the advanced execution fields, changed via the operator chat. */}
        <div className="sched-readonly">
          <div className="sched-readonly-head">{tr("sched.detail_readonly")}</div>
          <dl className="sched-dl">
            <ReadRow label={tr("sched.f_session_mode")} value={s.session_mode} />
            {s.session_mode === "reuse" && (
              <>
                <ReadRow label={tr("sched.f_reuse_target")} value={s.reuse_target} />
                <ReadRow label={tr("sched.f_reuse_session")} value={s.reuse_session} />
                <ReadRow label={tr("sched.f_overlap")} value={s.overlap_policy} />
                <ReadRow label={tr("sched.f_rotation")} value={s.rotation} />
                <ReadRow label={tr("sched.f_missing")} value={s.missing_target_policy} />
              </>
            )}
            <ReadRow label={tr("sched.f_repo")} value={s.repo} />
            {s.worktree && <ReadRow label={tr("sched.f_worktree")} value={s.worktree} />}
            <ReadRow label={tr("sched.f_next")} value={s.enabled ? s.next_run_local : tr("sched.paused_tag")} />
            <ReadRow label={tr("sched.f_last_status")} value={s.last_status} />
            <ReadRow label={tr("sched.f_updated")} value={s.updated_at} />
          </dl>
        </div>
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} type="button">
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" disabled={!canSave} type="submit">
          {tr("common.save")}
        </Button>
      </footer>
    </Modal>
  );
}

function ReadRow({ label, value }: { label: string; value?: string | number }) {
  const v = value === undefined || value === null || value === "" ? "—" : String(value);
  return (
    <>
      <dt className="sched-dt">{label}</dt>
      <dd className="sched-dd">{v}</dd>
    </>
  );
}
