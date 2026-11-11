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
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT, type MsgKey } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { scheduleUpdate } from "./api.ts";
import {
  DELIVERY_TARGETS,
  type ScheduleDTO,
  type ScheduleEditable,
  canonicalTargets,
  gateFormError,
  gateFormOf,
  gatePatchOf,
  gateVerdictLine,
  nextRunDetail,
  scheduleTargets,
  scheduleTitle,
} from "./read.ts";
import { scheduledKinds } from "../../agents/registry.ts";
import { workItemList } from "../workitems/api.ts";
import { readWorkItems, type WorkItemQuery } from "../workitems/read.ts";

// The agent kinds this picker offers come from the registry cap (AgentCaps.scheduledRuns),
// never from a list kept here: the hand-kept copy this replaced had already lost agy, whose
// scheduled runs shipped without the Console ever offering them. The schedule's current kind
// is still always included, so an unfamiliar value is never silently dropped from the select.
const AGENT_KINDS: string[] = scheduledKinds;
const SPEC_KINDS = ["cron", "interval", "once"];
const WAKE_POLICIES = ["wake", "skip", "catch_up"];
const TARGET_LABELS: Record<(typeof DELIVERY_TARGETS)[number], MsgKey> = {
  operator: "sched.target_operator",
  notifications: "sched.target_notifications",
  discord: "sched.target_discord",
  slack: "sched.target_slack",
};

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
  const [stopAfterRun, setStopAfterRun] = useState(!!s.stop_after_run);
  const [targets, setTargets] = useState<string[]>(() => canonicalTargets(scheduleTargets(s)));
  const [silent, setSilent] = useState(!!s.silent);
  // The gate (ADR 0021 addendum). A gate can only sit on a recurring session_mode=new schedule; one
  // that already has a gate keeps its section so it can be removed. A kind this Console does not
  // know is shown read-only and never patched, so saving another field cannot rewrite it.
  const [gateForm, setGateForm] = useState(() => gateFormOf(s.gate));
  const gateKnown = !s.gate || s.gate.kind === "work_items" || s.gate.kind === "none";
  const gateFits = s.session_mode === "new" && specKind !== "once";
  const showGate = gateKnown ? gateFits || !!s.gate : !!s.gate;
  const [queries, setQueries] = useState<WorkItemQuery[] | null>(null);
  const [queriesFailed, setQueriesFailed] = useState(false);
  useEffect(() => {
    if (!showGate || !gateKnown) return;
    let live = true;
    void workItemList()
      .then((res) => {
        if (!live) return;
        const { payload } = readWorkItems(res);
        if (payload) setQueries(payload.queries);
        else setQueriesFailed(true);
      })
      .catch(() => live && setQueriesFailed(true));
    return () => {
      live = false;
    };
  }, [showGate, gateKnown]);
  const toggleTarget = (target: string, on: boolean) =>
    setTargets((cur) => canonicalTargets(on ? [...cur, target] : cur.filter((x) => x !== target)));

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
    if (stopAfterRun !== !!s.stop_after_run) p.stop_after_run = stopAfterRun;
    if (targets.join(",") !== canonicalTargets(scheduleTargets(s)).join(",")) p.deliver_to = targets;
    if (silent !== !!s.silent) p.silent = silent;
    if (gateKnown) {
      const g = gatePatchOf(s.gate, gateForm);
      if (g) p.gate = g;
    }
    return p;
  }, [s, specKind, spec, tz, label, prompt, wake, agent, model, report, stopAfterRun, targets, silent, gateKnown, gateForm]);

  const dirty = Object.keys(patch).length > 0;
  // Reporting on with no target would deliver nowhere while looking switched on.
  const targetsOk = !report || targets.length > 0;
  const gateErr = gateKnown ? gateFormError(gateForm) : "";
  const canSave = dirty && !busy && targetsOk && gateErr === "" && prompt.trim().length > 0 && spec.trim().length > 0;

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

        {/* Delivery targets (#1560): where the report goes. Only while reporting is on; an
            empty choice is refused by canSave rather than saved as "report to nobody". */}
        {s.session_mode !== "assistant" && report && (
          <fieldset className="ui-field sched-targets">
            <legend className="ui-field-label">{tr("sched.f_deliver_to")}</legend>
            <div className="sched-target-list">
              {DELIVERY_TARGETS.map((target) => (
                <label key={target} className="sched-target">
                  <input
                    type="checkbox"
                    checked={targets.includes(target)}
                    onChange={(e) => toggleTarget(target, e.target.checked)}
                  />
                  <span>{tr(TARGET_LABELS[target])}</span>
                </label>
              ))}
            </div>
            <div className="ui-field-hint">{targetsOk ? tr("sched.f_deliver_to_hint") : tr("sched.f_deliver_to_empty")}</div>
          </fieldset>
        )}

        {/* The silent sentinel (#1560), independent of the report: even with reporting off it
            keeps a [SILENT] answer out of the notifications and marks the run as silent. */}
        {s.session_mode !== "assistant" && (
          <label className="ui-field" style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
            <input type="checkbox" checked={silent} onChange={(e) => setSilent(e.target.checked)} />
            <span>
              {tr("sched.f_silent")}
              <span className="ui-field-hint"> — {tr("sched.f_silent_hint")}</span>
            </span>
          </label>
        )}

        {/* Stop-after-run opt-in (docs/log/85), default off. Hidden for
            session_mode=assistant for the same reason as the report above: that mode drives a
            conversation and holds no session to stop. */}
        {s.session_mode !== "assistant" && (
          <label className="ui-field" style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
            <input type="checkbox" checked={stopAfterRun} onChange={(e) => setStopAfterRun(e.target.checked)} />
            <span>
              {tr("sched.f_stop_after_run")}
              <span className="ui-field-hint"> — {tr("sched.f_stop_after_run_hint")}</span>
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

        {showGate && (
          <fieldset className="ui-field sched-gate">
            <legend className="ui-field-label">{tr("sched.gate_title")}</legend>
            {gateKnown ? (
              <>
                <label className="sched-target">
                  <input
                    type="checkbox"
                    checked={gateForm.on}
                    onChange={(e) => setGateForm((f) => ({ ...f, on: e.target.checked }))}
                  />
                  <span>{tr("sched.gate_enable")}</span>
                </label>
                <div className="ui-field-hint">{tr("sched.gate_hint")}</div>
                {gateForm.on && (
                  <>
                    <div className="ui-field">
                      <label className="ui-field-label" htmlFor="sched-gate-query">
                        {tr("sched.gate_query")}
                      </label>
                      <select
                        id="sched-gate-query"
                        value={gateForm.queryId}
                        onChange={(e) => setGateForm((f) => ({ ...f, queryId: e.target.value }))}
                      >
                        <option value="">{tr("sched.gate_query_ph")}</option>
                        {/* The stored query stays selectable when the list does not hold it (not loaded yet,
                            deleted since): otherwise the select would silently show "choose" and a save
                            would point the gate somewhere else. */}
                        {gateForm.queryId && !(queries ?? []).some((q) => q.id === gateForm.queryId) && (
                          <option value={gateForm.queryId}>{tr("sched.gate_query_unknown", { id: gateForm.queryId })}</option>
                        )}
                        {(queries ?? []).map((q) => (
                          <option key={q.id} value={q.id}>
                            {q.label || q.query}
                          </option>
                        ))}
                      </select>
                      {queriesFailed && <div className="ui-field-hint">{tr("sched.gate_queries_failed")}</div>}
                      {queries && queries.length === 0 && <div className="ui-field-hint">{tr("sched.gate_queries_none")}</div>}
                      {gateErr === "query" && <div className="ui-field-hint">{tr("sched.gate_query_ph")}</div>}
                    </div>
                    <div className="ui-field">
                      <label className="ui-field-label" htmlFor="sched-gate-fp">
                        {tr("sched.gate_fingerprint")}
                      </label>
                      <select
                        id="sched-gate-fp"
                        value={gateForm.fingerprint}
                        onChange={(e) => setGateForm((f) => ({ ...f, fingerprint: e.target.value }))}
                      >
                        <option value="keys">{tr("sched.gate_fp_keys")}</option>
                        <option value="keys+updated">{tr("sched.gate_fp_updated")}</option>
                      </select>
                      <div className="ui-field-hint">{tr("sched.gate_fp_hint")}</div>
                    </div>
                    <div className="ui-field-row">
                      <div className="ui-field sched-narrow">
                        <label className="ui-field-label" htmlFor="sched-gate-max">
                          {tr("sched.gate_max_fires")}
                        </label>
                        <input
                          id="sched-gate-max"
                          type="text"
                          inputMode="numeric"
                          value={gateForm.maxFires}
                          placeholder="2"
                          onChange={(e) => setGateForm((f) => ({ ...f, maxFires: e.target.value }))}
                        />
                      </div>
                      <div className="ui-field sched-narrow">
                        <label className="ui-field-label" htmlFor="sched-gate-gap">
                          {tr("sched.gate_min_gap")}
                        </label>
                        <input
                          id="sched-gate-gap"
                          type="text"
                          inputMode="numeric"
                          value={gateForm.minGap}
                          placeholder="21600"
                          onChange={(e) => setGateForm((f) => ({ ...f, minGap: e.target.value }))}
                        />
                      </div>
                    </div>
                    <div className="ui-field-hint" data-invalid={gateErr === "caps" || undefined}>
                      {tr("sched.gate_caps_hint")}
                    </div>
                  </>
                )}
              </>
            ) : (
              <div className="ui-field-hint">{tr("sched.gate_unsupported", { kind: s.gate?.kind ?? "" })}</div>
            )}
            {s.gate && <GateLastLine gate={s.gate} />}
          </fieldset>
        )}

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
            <ReadRow label={tr("sched.f_next")} value={s.enabled ? nextRunDetail(s) : tr("sched.paused_tag")} />
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

// The last evaluation, read-only. An unknown verdict is shown as the CP sent it.
function GateLastLine({ gate }: { gate: NonNullable<ScheduleDTO["gate"]> }) {
  const tr = useT();
  const { at, text } = gateVerdictLine(gate);
  return (
    <div className="ui-field-hint sched-gate-last">
      {text ? (at ? tr("sched.gate_last", { at, text }) : text) : tr("sched.gate_last_never")}
    </div>
  );
}
