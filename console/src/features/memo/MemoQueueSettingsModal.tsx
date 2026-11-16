// The auto-run settings of one memo category (ADR 0137 §3, §6, §10): the standing instruction, the
// consumer (agent, model, effort), the spend limit, a new worktree per run, how many sessions
// run at once, how many messages go to one session, when it runs by itself (any time, a time
// window, or once enough notes are waiting), runs per day, when a run counts as overdue, and on
// or off. Written through the Console only; nothing in the workspace can create or change them.
//
// A save replaces the whole row, so draftBody sends every setting the dialog loaded, including
// the route it does not show. The read-only state (paused, noReportAt, ...) is never sent.
//
// With the tenant's switch off the dialog says so instead of offering to save. Removing the
// settings stays possible then: the Control Plane does not gate DELETE.
import { useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { t, useT, type MsgKey } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { scheduledKinds } from "../../agents/registry.ts";
import { kindOfferedForRuns } from "../../agents/launchAccess.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useMemberCatalog } from "../settings/credentials/memberCatalog.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { memoQueueDelete, memoQueuePut } from "./api.ts";
import {
  QUEUE_BATCH_MAX,
  QUEUE_FIRES_PER_DAY_MAX,
  QUEUE_INSTRUCTION_MAX_BYTES,
  QUEUE_MAX_ACTIVE_MAX,
  QUEUE_OPEN_AFTER_MAX,
  QUEUE_SPEND_MAX,
  QUEUE_TASK_HOURS_MAX,
  QUEUE_WINDOW_MAX_MINUTES,
  type RunMode,
  draftBody,
  draftErrors,
  draftOf,
} from "./queues.ts";

const EFFORTS = ["low", "medium", "high", "xhigh", "max"];
const RUN_MODES: { mode: RunMode; label: MsgKey; hint: MsgKey }[] = [
  { mode: "always", label: "memo.q_when_always", hint: "memo.q_when_always_hint" },
  { mode: "window", label: "memo.q_when_window", hint: "memo.q_when_window_hint" },
  { mode: "threshold", label: "memo.q_when_threshold", hint: "memo.q_when_threshold_hint" },
];

interface Props {
  categoryName: string;
  categoryId: string;
  /** The category's current settings; undefined = it has none yet. */
  queue: MemoQueue | undefined;
  tenantEnabled: boolean;
  onClose: () => void;
  /** Saved or removed: the caller refetches. */
  onChanged: () => void;
}

export function MemoQueueSettingsModal({ categoryName, categoryId, queue, tenantEnabled, onClose, onChanged }: Props) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [draft, setDraft] = useState(() => draftOf(queue));
  const [busy, setBusy] = useState(false);
  // Errors show once the person has tried to save, so an untouched form is not red.
  const [tried, setTried] = useState(false);
  const errors = useMemo(() => draftErrors(draft), [draft]);
  const set = <K extends keyof typeof draft>(k: K, v: (typeof draft)[K]) => setDraft((d) => ({ ...d, [k]: v }));
  // Only the kinds the credential policy allows and the member has not switched off (ADR 0138
  // D10a), from the live catalogue; none without a fresh answer. The queue's own kind stays, so
  // an edit of another field never rewrites it.
  const running = useWorkspaceStore((st) => st.state) === "running";
  const catalog = useMemberCatalog(running);
  const kinds = useMemo(() => {
    const allowed = scheduledKinds.filter((k) => kindOfferedForRuns(k, catalog.kinds));
    return allowed.includes(draft.agentKind as never) ? allowed : [draft.agentKind, ...allowed];
  }, [draft.agentKind, catalog.kinds]);
  // {max} is the limit of the field the message is about; messages without it ignore it.
  const maxOf: Record<keyof typeof errors, number> = {
    instruction: QUEUE_INSTRUCTION_MAX_BYTES >> 10,
    spendCapUsd: QUEUE_SPEND_MAX,
    maxActive: QUEUE_MAX_ACTIVE_MAX,
    batch: QUEUE_BATCH_MAX,
    openCron: 0,
    openMinutes: QUEUE_WINDOW_MAX_MINUTES,
    tz: 0,
    openAfter: QUEUE_OPEN_AFTER_MAX,
    maxFiresPerDay: QUEUE_FIRES_PER_DAY_MAX,
    maxTaskHours: QUEUE_TASK_HOURS_MAX,
  };
  const show = (f: keyof typeof errors) => (tried && errors[f] ? tr(errors[f]!, { max: maxOf[f] }) : null);
  const blocked = !tenantEnabled;

  const save = async () => {
    if (blocked || busy) return;
    setTried(true);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    try {
      const res = await memoQueuePut(categoryId, draftBody(draft, queue?.route ?? ""));
      if (res?.error) {
        toast(errText(res.error) || t("memo.q_save_failed"), { kind: "warn" });
        return;
      }
      toast(t("memo.q_saved"), { kind: "success" });
      onChanged();
      onClose();
    } catch {
      toast(t("memo.q_save_failed"), { kind: "warn" });
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    const ok = await askConfirm({
      title: t("memo.q_remove"),
      body: t("memo.q_remove_confirm", { name: categoryName }),
      confirmLabel: t("memo.q_remove"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const res = await memoQueueDelete(categoryId);
      if (!res.ok) {
        toast(t("memo.q_remove_failed"), { kind: "warn" });
        return;
      }
      toast(t("memo.q_removed"));
      onChanged();
      onClose();
    } catch {
      toast(t("memo.q_remove_failed"), { kind: "warn" });
    } finally {
      setBusy(false);
    }
  };

  const fieldErr = (f: keyof typeof errors) => {
    const m = show(f);
    return m ? (
      <div className="ui-field-hint" data-invalid role="alert">
        {m}
      </div>
    ) : null;
  };

  return (
    <Modal
      title={tr("memo.autorun_settings_title", { name: categoryName })}
      onClose={onClose}
      className="memo-q-modal"
      as="form"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        {blocked && (
          <div className="ui-field-hint memo-q-notice" role="status">
            {queue ? tr("memo.q_tenant_off_existing") : tr("memo.q_tenant_off")}
          </div>
        )}

        <div className="ui-field">
          <label className="ui-field-label" htmlFor="memo-q-instruction">
            {tr("memo.q_instruction")}
          </label>
          <textarea
            id="memo-q-instruction"
            value={draft.instruction}
            rows={6}
            disabled={blocked}
            placeholder={tr("memo.q_instruction_ph")}
            onChange={(e) => set("instruction", e.target.value)}
            spellCheck={false}
          />
          <div className="ui-field-hint">{tr("memo.q_instruction_hint", { max: QUEUE_INSTRUCTION_MAX_BYTES >> 10 })}</div>
          {fieldErr("instruction")}
        </div>

        <div className="ui-field-row">
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-agent">
              {tr("memo.q_agent")}
            </label>
            <select id="memo-q-agent" value={draft.agentKind} disabled={blocked} onChange={(e) => set("agentKind", e.target.value)}>
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
          <div className="ui-field">
            <label className="ui-field-label" htmlFor="memo-q-model">
              {tr("memo.q_model")}
            </label>
            <input
              id="memo-q-model"
              type="text"
              value={draft.model}
              disabled={blocked}
              placeholder={tr("memo.q_model_ph")}
              onChange={(e) => set("model", e.target.value)}
              spellCheck={false}
            />
          </div>
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-effort">
              {tr("memo.q_effort")}
            </label>
            <input
              id="memo-q-effort"
              type="text"
              list="memo-q-efforts"
              value={draft.effort}
              disabled={blocked}
              placeholder={tr("memo.q_effort_ph")}
              onChange={(e) => set("effort", e.target.value)}
              spellCheck={false}
            />
            <datalist id="memo-q-efforts">
              {EFFORTS.map((x) => (
                <option key={x} value={x} />
              ))}
            </datalist>
          </div>
        </div>

        <div className="ui-field-row">
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-budget">
              {tr("memo.q_budget")}
            </label>
            <input
              id="memo-q-budget"
              type="text"
              inputMode="decimal"
              value={draft.spendCapUsd}
              disabled={blocked}
              onChange={(e) => set("spendCapUsd", e.target.value)}
            />
            {fieldErr("spendCapUsd") ?? <div className="ui-field-hint">{tr("memo.q_budget_hint")}</div>}
          </div>
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-max-active">
              {tr("memo.q_max_active")}
            </label>
            <input
              id="memo-q-max-active"
              type="text"
              inputMode="numeric"
              value={draft.maxActive}
              disabled={blocked}
              onChange={(e) => set("maxActive", e.target.value)}
            />
            {fieldErr("maxActive")}
          </div>
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-batch">
              {tr("memo.q_batch")}
            </label>
            <input
              id="memo-q-batch"
              type="text"
              inputMode="numeric"
              value={draft.batch}
              disabled={blocked}
              onChange={(e) => set("batch", e.target.value)}
            />
            {fieldErr("batch")}
          </div>
        </div>

        <label className="ui-field" style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
          <input type="checkbox" checked={draft.worktree} disabled={blocked} onChange={(e) => set("worktree", e.target.checked)} />
          <span>{tr("memo.q_worktree")}</span>
        </label>

        <fieldset className="ui-field memo-q-when" disabled={blocked}>
          <legend className="ui-field-label">{tr("memo.q_when")}</legend>
          {RUN_MODES.map((m) => (
            <label key={m.mode} className="memo-q-when-opt">
              <input type="radio" name="memo-q-when" checked={draft.runMode === m.mode} onChange={() => set("runMode", m.mode)} />
              <span>
                {tr(m.label)}
                <span className="ui-field-hint"> — {tr(m.hint)}</span>
              </span>
            </label>
          ))}
          {draft.runMode === "window" && (
            <div className="ui-field-row">
              <div className="ui-field">
                <label className="ui-field-label" htmlFor="memo-q-open-cron">
                  {tr("memo.q_open_cron")}
                </label>
                <input
                  id="memo-q-open-cron"
                  type="text"
                  value={draft.openCron}
                  placeholder="0 9 * * *"
                  onChange={(e) => set("openCron", e.target.value)}
                  spellCheck={false}
                />
                {fieldErr("openCron") ?? <div className="ui-field-hint">{tr("sched.spec_hint_cron")}</div>}
              </div>
              <div className="ui-field memo-q-narrow">
                <label className="ui-field-label" htmlFor="memo-q-open-minutes">
                  {tr("memo.q_open_minutes")}
                </label>
                <input
                  id="memo-q-open-minutes"
                  type="text"
                  inputMode="numeric"
                  value={draft.openMinutes}
                  onChange={(e) => set("openMinutes", e.target.value)}
                />
                {fieldErr("openMinutes")}
              </div>
              <div className="ui-field">
                <label className="ui-field-label" htmlFor="memo-q-tz">
                  {tr("sched.f_tz")}
                </label>
                <input id="memo-q-tz" type="text" value={draft.tz} placeholder="UTC" onChange={(e) => set("tz", e.target.value)} spellCheck={false} />
                {fieldErr("tz") ?? <div className="ui-field-hint">{tr("memo.q_tz_hint")}</div>}
              </div>
            </div>
          )}
          {draft.runMode === "threshold" && (
            <div className="ui-field memo-q-narrow">
              <label className="ui-field-label" htmlFor="memo-q-open-after">
                {tr("memo.q_open_after")}
              </label>
              <input
                id="memo-q-open-after"
                type="text"
                inputMode="numeric"
                value={draft.openAfter}
                onChange={(e) => set("openAfter", e.target.value)}
              />
              {fieldErr("openAfter")}
            </div>
          )}
        </fieldset>

        <div className="ui-field-row">
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-fires">
              {tr("memo.q_fires")}
            </label>
            <input
              id="memo-q-fires"
              type="text"
              inputMode="numeric"
              value={draft.maxFiresPerDay}
              disabled={blocked}
              onChange={(e) => set("maxFiresPerDay", e.target.value)}
            />
            {fieldErr("maxFiresPerDay") ?? <div className="ui-field-hint">{tr("memo.q_fires_hint")}</div>}
          </div>
          <div className="ui-field memo-q-narrow">
            <label className="ui-field-label" htmlFor="memo-q-task-hours">
              {tr("memo.q_task_hours")}
            </label>
            <input
              id="memo-q-task-hours"
              type="text"
              inputMode="numeric"
              value={draft.maxTaskHours}
              disabled={blocked}
              onChange={(e) => set("maxTaskHours", e.target.value)}
            />
            {fieldErr("maxTaskHours") ?? <div className="ui-field-hint">{tr("memo.q_task_hours_hint")}</div>}
          </div>
        </div>

        <label className="ui-field" style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
          <input type="checkbox" checked={draft.enabled} disabled={blocked} onChange={(e) => set("enabled", e.target.checked)} />
          <span>
            {tr("memo.q_enabled")}
            <span className="ui-field-hint"> — {tr("memo.q_enabled_hint")}</span>
          </span>
        </label>
      </div>

      <footer className="ui-modal-foot">
        {queue && (
          <Button variant="ghost" type="button" disabled={busy} onClick={() => void remove()}>
            {tr("memo.q_remove")}
          </Button>
        )}
        <Button variant="ghost" onClick={onClose} type="button">
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={blocked || busy}>
          {tr("common.save")}
        </Button>
      </footer>
    </Modal>
  );
}
