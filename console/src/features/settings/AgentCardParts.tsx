import type { ReactNode } from "react";
import { useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { ModelPicker } from "../../ui/ModelPicker.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { agentLaunchDefault, useSettings, setSettings, ASSISTANT_RECOMMENDED_MODEL, CLAUDE_MODELS } from "../../lib/settings.ts";
import { useEffortOptions, useModelOptions } from "../../lib/agentModels.ts";
import { modelMatchesHidden } from "../../lib/modelDeny.ts";
import { forgetHiddenRepoModels } from "../../lib/repoLast.ts";
import { agentOf, nonPlanModeLabel } from "../../agents/registry.ts";
import { Choice, OnOff, Select } from "./controls.tsx";

// A labeled settings row inside a card's 動作設定 group.
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

// CardSettings: the per-agent 動作設定 disclosure — collapsed by default so the card
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

// ThinkingRow: 「思考を展開して表示」（kind スコープ・既定オフ）。ミラーの「思考」ブロックは
// 既定で畳んだまま出るので、思考を常に読みたい人だけがここで開いた状態を既定にできる。
// 思考を出す backend（codex / opencode）のカードにだけ置き、kind ごとに独立して効く。
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
        {/* opencode は候補が数十個になりセグメントだと敷き詰まるため、長いリストは Select に。 */}
        {kind === "claude" ? (
          <ModelPicker kind={kind} model={row.model} onChange={(model) => update({ model, effort: "" })} />
        ) : models.length > 8 ? (
          <Select value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        ) : (
          <Choice value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        )}
      </SettingRow>
      {kind === "claude" && <ClaudeCustomModelsRow />}
      {/* agy は effort 相当がモデル名に織り込まれている（(Medium) 等）ため行ごと出さない。 */}
      {desc.caps.effort && (
        <SettingRow label={tr("agents.default_effort")}>
          <Choice value={row.effort} options={efforts} onChange={(effort) => update({ effort })} />
        </SettingRow>
      )}
      <HiddenModelsRow kind={kind} />
      {/* planMode（チャットの plan トグル）が無くても tuiStartMode（plan 起動）対応なら
          既定の開始モードを設定できる（cursor/copilot/kiro — 起動 UI のゲートと同型）。 */}
      {(desc.caps.planMode || desc.caps.tuiStartMode) && (
        <SettingRow label={tr("agents.start_mode")}>
          <Choice
            value={row.startMode}
            options={[["normal", nonPlanModeLabel(kind, row.skipPermissions) || tr("agents.mode_normal")], ["plan", "Plan"]]}
            onChange={(startMode) => update({ startMode: startMode === "plan" ? "plan" : "normal" })}
          />
        </SettingRow>
      )}
      {/* 権限確認をスキップするか（docs/log/76）。承認待ちを Console から答えられる kind
          （claude / cursor / copilot / kiro / agy）だけに出す — 答えられない kind で
          オフにできると、固まったセッションを作れてしまう。 */}
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

// HiddenModelsRow:「使わないモデル」— kind ごとの除外リスト（settings.hiddenModels）。
// 動機は課金事故の予防で、Claude の Team プランでは Fable が API クレジット扱いになる。
// 除外すると（Agent が同じ ui-prefs を読むので）Console のピッカーからも MCP の
// list_models からも消え、除外モデルを指定した起動は Agent 側で断られる。
//
// 編集 UI は「現在の除外＝チップ（×で解除）」＋「追加＝残っている候補の select」。
// 追加側の候補が既に絞り込み済みなので、生カタログを別途持たなくても往復できる。
function HiddenModelsRow({ kind }: { kind: string }) {
  const tr = useT();
  const s = useSettings();
  const visible = (useModelOptions(kind) || []).filter(([id]) => id); // 「既定」は id ではない
  const hidden = s.hiddenModels?.[kind] || [];
  // claude は固定4ティアで「既定」の選択肢が無い＝全部隠すと起動できるモデルが消える。
  // 最後の1つは隠させない（Agent 側にも同じフェイルセーフがあるが、行き止まりの状態を
  // 作らせない方が親切）。
  const canAdd = visible.length > (kind === "claude" ? 1 : 0);

  const apply = (next: string[]) => {
    const patch: Parameters<typeof setSettings>[0] = {
      hiddenModels: { ...s.hiddenModels, [kind]: next },
    };
    const isHidden = (m: string) => !!m && next.some((h) => modelMatchesHidden(m, h));
    // 保存済みの選択値を掃く。放置すると「設定画面には除外と出ているのに起動導線は
    // 除外モデルを既定に持っている」状態になり、起動のたびに Agent 側ガードで弾かれる。
    const row = agentLaunchDefault(s, kind);
    if (isHidden(row.model)) {
      const fallback = kind === "claude" ? CLAUDE_MODELS.find(([id]) => !isHidden(id))?.[0] || "" : "";
      patch.agentLaunchDefaults = { ...s.agentLaunchDefaults, [kind]: { ...row, model: fallback, effort: "" } };
      if (kind === "claude") patch.defaultModel = fallback;
    }
    if (isHidden(s.assistantModels?.[kind] || "")) {
      patch.assistantModels = { ...s.assistantModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (isHidden(s.assistantUtilityModels?.[kind] || "")) {
      patch.assistantUtilityModels = { ...s.assistantUtilityModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (kind === "claude" && isHidden(s.assistantAutoTurnModel)) patch.assistantAutoTurnModel = "";
    setSettings(patch);
    forgetHiddenRepoModels(kind, next); // リポジトリごとの「前回使ったモデル」も掃く
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

// RtkRow: the shared "RTK（トークン節約）" settings row — a toggle when the workspace
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
