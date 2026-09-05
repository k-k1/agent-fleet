import type { ReactNode } from "react";
import { useState } from "react";
import { Button } from "../../../ui/Button.tsx";
import { ModelPicker } from "../../../ui/ModelPicker.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { agentLaunchDefault, useSettings, setSettings, ASSISTANT_RECOMMENDED_MODEL, CLAUDE_MODELS } from "../../../lib/settings.ts";
import { useEffortOptions, useModelOptions } from "../../../lib/agentModels.ts";
import { modelMatchesHidden } from "../../../lib/modelDeny.ts";
import { forgetHiddenRepoModels } from "../../../lib/repoLast.ts";
import { agentOf, nonPlanModeLabel } from "../../../agents/registry.ts";
import { Choice, OnOff, Select } from "../parts/controls.tsx";

// A labeled settings row inside a card's behavior-settings group.
export function SettingRow({ label, sub, children }: { label: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="ps-row">
      <span className="ps-label">
        {label}
        {sub && <span className="sub">{sub}</span>}
      </span>
      {children}
    </div>
  );
}

// CardSettings: the per-agent behavior-settings disclosure — collapsed by default so the card
// reads as "connect" first, with behavior a deliberate second level. Its body is the
// client launch defaults (always usable) + any container-backed toggles the card passes.
export function CardSettings({ children }: { children?: ReactNode }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className={"p-settings" + (open ? " open" : "")}>
      <button type="button" className="ps-disclosure" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="ps-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        {tr("agents.behavior")}
      </button>
      {open && <div className="ps-body">{children}</div>}
    </div>
  );
}

// ThinkingRow: "expand thinking" (kind-scoped, off by default). The mirror renders its thinking
// blocks collapsed, so only people who always want to read them make expanded the default here.
// Only on the cards of backends that emit thinking (codex / opencode); each kind is independent.
export function ThinkingRow({ kind }: { kind: string }) {
  const s = useSettings();
  const tr = useT();
  return (
    <>
      <SettingRow label={tr("agents.expand_thinking")}>
        <OnOff
          value={s.expandThinking[kind] === true}
          onChange={(v) => setSettings({ expandThinking: { ...s.expandThinking, [kind]: v } })}
        />
      </SettingRow>
      <p className="ps-note">{tr("agents.expand_thinking_note")}</p>
    </>
  );
}

// The connection body shown while the workspace is stopped: launch defaults below stay
// reachable, but the auth flow (Agent-proxied) waits for start.
export function ConnPaused() {
  const tr = useT();
  return <div className="p-desc muted">{tr("agents.conn_paused")}</div>;
}

// LaunchDefaults: the common, per-agent starting point. A repo's last-used values
// still win in the launch dialog, so these are useful global defaults without
// repeatedly overwriting deliberate per-repo choices.
export function LaunchDefaults({ kind }: { kind: "claude" | "codex" | "cursor" | "kiro" | "agy" | "opencode" | "copilot" }) {
  const s = useSettings();
  const tr = useT();
  const desc = agentOf(kind);
  const row = agentLaunchDefault(s, kind);
  const models = useModelOptions(kind) || [["", tr("common.default")]] as [string, string][];
  const efforts = useEffortOptions(kind, row.model);
  const update = (patch: Partial<typeof row>) => {
    const next = { ...row, ...patch };
    setSettings({
      agentLaunchDefaults: { ...s.agentLaunchDefaults, [kind]: next },
      // Keep the legacy key in sync while older Console images may still read it.
      ...(kind === "claude" ? { defaultModel: next.model } : {}),
    });
  };
  return (
    <>
      <SettingRow label={tr("agents.default_model")}>
        {/* opencode offers dozens of candidates, which a segmented control cannot fit, so long
            lists use a Select. */}
        {kind === "claude" ? (
          <ModelPicker kind={kind} model={row.model} onChange={(model) => update({ model, effort: "" })} />
        ) : models.length > 8 ? (
          <Select value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        ) : (
          <Choice value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        )}
      </SettingRow>
      {kind === "claude" && <ClaudeCustomModelsRow />}
      {/* agy bakes the effort equivalent into the model name ("(Medium)" and so on), so the row
          is omitted entirely. */}
      {desc.caps.effort && (
        <SettingRow label={tr("agents.default_effort")}>
          <Choice value={row.effort} options={efforts} onChange={(effort) => update({ effort })} />
        </SettingRow>
      )}
      <HiddenModelsRow kind={kind} />
      {/* A kind without planMode (the chat plan toggle) can still set a default start mode if it
          supports tuiStartMode, i.e. launching in plan (cursor/copilot/kiro — the same gate as
          the launch UI). */}
      {(desc.caps.planMode || desc.caps.tuiStartMode) && (
        <SettingRow label={tr("agents.start_mode")}>
          <Choice
            value={row.startMode}
            options={[["normal", nonPlanModeLabel(kind, row.skipPermissions) || tr("agents.mode_normal")], ["plan", "Plan"]]}
            onChange={(startMode) => update({ startMode: startMode === "plan" ? "plan" : "normal" })}
          />
        </SettingRow>
      )}
      {/* Whether to skip permission prompts (docs/log/76). Only shown for kinds whose pending
          approvals can be answered from the Console (claude / cursor / copilot / kiro / agy):
          allowing it off for a kind that cannot be answered would let a stuck session be
          created. */}
      {desc.caps.permissionChoice && (
        <SettingRow label={tr("agents.skip_permissions")} sub={tr("agents.skip_permissions_sub")}>
          <OnOff value={row.skipPermissions} onChange={(skipPermissions) => update({ skipPermissions })} />
        </SettingRow>
      )}
      {desc.caps.permissionChoice && !row.skipPermissions && (
        <p className="ps-note">{tr("agents.skip_permissions_off_note")}</p>
      )}
      <p className="ps-note">{tr("agents.note_launch_defaults")}</p>
    </>
  );
}

// Claude Code OAuth exposes no account-aware model catalog. These user-owned full ids are
// therefore the durable catalog shared by launch pickers and MCP list_models.
function ClaudeCustomModelsRow() {
  const s = useSettings();
  const tr = useT();
  const [value, setValue] = useState("");
  const id = value.trim();
  const duplicate = s.claudeCustomModels.some((m) => m.toLowerCase() === id.toLowerCase());
  const valid = /^claude-[a-z0-9][a-z0-9._\-[\]]*$/i.test(id) && !duplicate;
  const add = () => {
    if (!valid) return;
    setSettings({ claudeCustomModels: [...s.claudeCustomModels, id] });
    setValue("");
  };
  return (
    <SettingRow label={tr("agents.claude_custom_models")} sub={tr("agents.claude_custom_models_sub")}>
      <div className="hidden-models">
        {s.claudeCustomModels.length > 0 && (
          <div className="hm-chips">
            {s.claudeCustomModels.map((model) => (
              <span key={model} className="hm-chip">
                {model}
                <button
                  type="button"
                  className="hm-chip-x"
                  aria-label={tr("agents.claude_custom_models_remove", { model })}
                  onClick={() => setSettings({ claudeCustomModels: s.claudeCustomModels.filter((m) => m !== model) })}
                >×</button>
              </span>
            ))}
          </div>
        )}
        <div className="ui-field-row">
          <input
            className="ds-select"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }}
            placeholder="claude-opus-4-8"
            aria-label={tr("agents.claude_custom_models_input")}
            spellCheck={false}
          />
          <Button small icon="add" disabled={!valid} onClick={add}>{tr("agents.claude_custom_models_add")}</Button>
        </div>
      </div>
    </SettingRow>
  );
}

// HiddenModelsRow: the per-kind exclusion list (settings.hiddenModels), shown as "models not to
// use". It exists to prevent billing accidents — on Claude's Team plan, Fable is charged as API
// credits. An excluded model disappears from the Console picker and from MCP's list_models (the
// Agent reads the same ui-prefs), and a launch naming one is refused by the Agent.
//
// The editor is: current exclusions as chips (x removes) plus a select of the remaining
// candidates to add. Those candidates are already filtered, so no separate raw catalogue is
// needed to round-trip.
function HiddenModelsRow({ kind }: { kind: string }) {
  const tr = useT();
  const s = useSettings();
  const visible = (useModelOptions(kind) || []).filter(([id]) => id); // the "default" entry has no id
  const hidden = s.hiddenModels?.[kind] || [];
  // claude has four fixed tiers and no "default" option, so hiding them all would leave nothing
  // launchable. Refuse to hide the last one; the Agent has the same failsafe, but not letting a
  // dead end be created in the first place is kinder.
  const canAdd = visible.length > (kind === "claude" ? 1 : 0);

  const apply = (next: string[]) => {
    const patch: Parameters<typeof setSettings>[0] = {
      hiddenModels: { ...s.hiddenModels, [kind]: next },
    };
    const isHidden = (m: string) => !!m && next.some((h) => modelMatchesHidden(m, h));
    // Sweep the stored selections. Left alone, settings would show a model as excluded while
    // the launch flow still defaults to it, and every launch would be rejected by the Agent's
    // guard.
    const row = agentLaunchDefault(s, kind);
    if (isHidden(row.model)) {
      const fallback = kind === "claude" ? CLAUDE_MODELS.find(([id]) => !isHidden(id))?.[0] || "" : "";
      patch.agentLaunchDefaults = { ...s.agentLaunchDefaults, [kind]: { ...row, model: fallback, effort: "" } };
      if (kind === "claude") patch.defaultModel = fallback;
    }
    if (isHidden(s.assistantModels?.[kind] || "")) {
      patch.assistantModels = { ...s.assistantModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (isHidden(s.aiShortModels?.[kind] || "")) {
      patch.aiShortModels = { ...s.aiShortModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (isHidden(s.aiProseModels?.[kind] || "")) {
      patch.aiProseModels = { ...s.aiProseModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (kind === "claude" && isHidden(s.assistantAutoTurnModel)) patch.assistantAutoTurnModel = "";
    setSettings(patch);
    forgetHiddenRepoModels(kind, next); // sweep the per-repo "last used model" too
  };

  return (
    <SettingRow label={tr("agents.hidden_models")} sub={tr("agents.hidden_models_sub")}>
      <div className="hidden-models">
        {hidden.length > 0 && (
          <div className="hm-chips">
            {hidden.map((id) => (
              <span key={id} className="hm-chip">
                {id}
                <button
                  type="button"
                  className="hm-chip-x"
                  aria-label={tr("agents.hidden_models_remove", { model: id })}
                  onClick={() => apply(hidden.filter((h) => h !== id))}
                >
                  ×
                </button>
              </span>
            ))}
          </div>
        )}
        <select
          className="ds-select"
          value=""
          disabled={!canAdd}
          aria-label={tr("agents.hidden_models_add")}
          onChange={(e) => e.target.value && apply([...hidden, e.target.value])}
        >
          <option value="">{tr("agents.hidden_models_add")}</option>
          {visible.map(([id, label]) => (
            <option key={id} value={id}>
              {label}
            </option>
          ))}
        </select>
      </div>
    </SettingRow>
  );
}

// RtkRow: the shared "RTK (token saving)" settings row — a toggle when the workspace
// has rtk, else an "unavailable" note. Used by all three agent cards.
export function RtkRow({
  available,
  value,
  onChange,
}: {
  available?: boolean;
  value?: boolean;
  onChange: (v: boolean) => void;
}) {
  const tr = useT();
  return (
    <SettingRow label={tr("agents.rtk_row")}>
      {available ? (
        <OnOff value={value} onChange={onChange} />
      ) : (
        <span className="muted">{tr("agents.rtk_unavailable")}</span>
      )}
    </SettingRow>
  );
}
