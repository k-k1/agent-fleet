// A message's own run recipe (agent, model, effort; ADR 0137 addendum, #4845): the badge that says
// a message does not follow the category's setting, and the editor row that sets it. A field left
// empty follows the category, so a message with no badge runs exactly as before.
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useMemo } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { scheduledKinds } from "../../agents/registry.ts";
import { kindOfferedForRuns } from "../../agents/launchAccess.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useMemberCatalog } from "../settings/credentials/memberCatalog.ts";
import type { Memo } from "../../types/memo.ts";
import { hasRecipe, recipeParts } from "./queues.ts";

export const RECIPE_EFFORTS = ["low", "medium", "high", "xhigh", "max"];

type Recipe = Pick<Memo, "agentKind" | "model" | "effort" | "route">;

/** The recipe as one line: "codex · gpt-x · high"; the route is told apart by its word. */
export function recipeLine(m: Recipe, route: (r: string) => string): string {
  return [m.agentKind, m.model, m.effort, m.route ? route(m.route) : ""].filter((v): v is string => !!v).join(" · ");
}

/** Nothing for a message that follows the category's setting. */
export function MemoRecipeBadge({ memo, className = "memo-qorigin memo-qrecipe" }: { memo: Recipe; className?: string }) {
  const tr = useT();
  if (!hasRecipe(memo) || recipeParts(memo).length === 0) return null;
  const line = recipeLine(memo, (r) => tr("memo.recipe_route", { route: r }));
  return (
    <span className={className} title={tr("memo.recipe_badge_title", { value: line })} data-recipe-badge="">
      {line}
    </span>
  );
}

export interface RecipeDraft {
  agentKind: string;
  model: string;
  effort: string;
}

export const recipeDraftOf = (m: Recipe): RecipeDraft => ({ agentKind: m.agentKind ?? "", model: m.model ?? "", effort: m.effort ?? "" });

/** The fields the member changed, to send in a patch (absent keeps one, "" clears it). */
export function recipePatch(was: RecipeDraft, now: RecipeDraft): Partial<RecipeDraft> {
  const out: Partial<RecipeDraft> = {};
  const model = now.model.trim();
  const effort = now.effort.trim();
  if (now.agentKind !== was.agentKind) out.agentKind = now.agentKind;
  if (model !== was.model) out.model = model;
  if (effort !== was.effort) out.effort = effort;
  return out;
}

interface FieldsProps {
  value: RecipeDraft;
  onChange: (v: RecipeDraft) => void;
  /** The category's own agent, said in the empty choice. */
  categoryAgent: string;
  /** The note is being moved out of every auto-run category: its own setting is dropped. */
  dropped?: boolean;
}

/** The editor row: agent (default "category setting"), model and effort. */
export function MemoRecipeFields({ value, onChange, categoryAgent, dropped }: FieldsProps) {
  const tr = useT();
  const running = useWorkspaceStore((st) => st.state) === "running";
  const catalog = useMemberCatalog(running);
  // The kinds the credential policy allows; the one the memo already names stays on offer, so
  // opening an edit never rewrites it.
  const kinds = useMemo(() => {
    const allowed = scheduledKinds.filter((k) => kindOfferedForRuns(k, catalog.kinds));
    return value.agentKind && !allowed.includes(value.agentKind as never) ? [value.agentKind, ...allowed] : [...allowed];
  }, [value.agentKind, catalog.kinds]);
  return (
    <fieldset className="memo-edit-recipe">
      <legend>{tr("memo.recipe_label")}</legend>
      <div className="memo-edit-recipe-row">
        <select aria-label={tr("memo.q_agent")} value={value.agentKind} onChange={(e) => onChange({ ...value, agentKind: e.target.value })}>
          <option value="">{categoryAgent ? tr("memo.recipe_default_with", { value: categoryAgent }) : tr("memo.recipe_default")}</option>
          {kinds.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
        <input {...NO_AUTOFILL}
          type="text"
          aria-label={tr("memo.q_model")}
          value={value.model}
          placeholder={tr("memo.recipe_model_ph")}
          spellCheck={false}
          onChange={(e) => onChange({ ...value, model: e.target.value })}
        />
        <input {...NO_AUTOFILL}
          type="text"
          list="memo-recipe-efforts"
          aria-label={tr("memo.q_effort")}
          value={value.effort}
          placeholder={tr("memo.recipe_effort_ph")}
          spellCheck={false}
          onChange={(e) => onChange({ ...value, effort: e.target.value })}
        />
        <datalist id="memo-recipe-efforts">
          {RECIPE_EFFORTS.map((x) => (
            <option key={x} value={x} />
          ))}
        </datalist>
      </div>
      <div className="memo-edit-hint">{tr(dropped ? "memo.recipe_dropped" : "memo.recipe_hint")}</div>
    </fieldset>
  );
}
