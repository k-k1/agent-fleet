// RunWithStrip — the "run with" strip under the first prompt of the launch dialog and of the Start
// hub's space form (ADR 0142 decision 7, dated note on the strip). Two lines, ordered by how often
// they change: line 1 holds the agent and the model route (rarely changed, each a chip that opens
// its choices), line 2 the model (changed with almost every prompt) as ONE single-choice row.
// The panels stay mounted while closed (`hidden`): the choices keep their DOM identity, and the
// narrow layout can show them from the one combined chip.
import { useId, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { kindColorClasses, kindDisplayName } from "../../lib/sessionkind.ts";
import { ModelPicker } from "../../ui/ModelPicker.tsx";
import { ModelInfoLine } from "../../ui/ModelInfoLine.tsx";
import { modelIsRetiring, useModelInfos, useModelOptions } from "../../lib/agentModels.ts";
import type { ModelOption } from "../../lib/agentModels.ts";
import { RECENT_MODELS_MAX, readRecentModels } from "../../lib/recentModels.ts";
import { useEscLayer } from "../../lib/escLayer.ts";

const CLAUDE_ALIASES = ["fable", "opus", "sonnet", "haiku"];

/** A tier alias is not a registered model: only registered ids are remembered as recents. */
export const isTierAlias = (id: string): boolean => CLAUDE_ALIASES.includes(id);

/** The label a model button shows: the common `claude-` prefix dropped; the full id is the tooltip. */
export function shortModelLabel(label: string): string {
  return label.replace(/^claude-/, "");
}

export interface RunWithStripProps {
  kinds: string[];
  kind: string;
  onPickKind: (k: string) => void;
  /** Shown instead of the agent tiles when nothing can be launched (checking / policy / none). */
  noAgents: ReactNode;
  /** Absent when the agent offers no route to choose. */
  route: null | {
    /** The chosen route's name, or the default's. */
    label: string;
    /** A route other than the default: the billed account changes, so the chip is accented. */
    custom: boolean;
    /** The launch cannot start until something about the route is fixed; `why` says what. */
    problem: boolean;
    panel: ReactNode;
  };
  /** The reason a route-caused block shows under the strip, also for an agent with no route chip. */
  why: ReactNode;
  /** Under line 2: what blocks or explains the launch (route reasons, Bedrock fields, notices). */
  below: ReactNode;
  /** Line 2; null when the agent has no model choice. */
  model: ReactNode;
  disabled?: boolean;
}

export function RunWithStrip({ kinds, kind, onPickKind, noAgents, route, why, below, model, disabled }: RunWithStripProps) {
  const tr = useT();
  const ids = useId();
  const agentPanelId = ids + "-agent";
  const routePanelId = ids + "-route";
  const whyId = ids + "-why";
  // Which panels are open. The narrow layout's combined chip opens both; the open state is kept
  // per panel, so every chip reports what is on screen whichever layout the width currently shows.
  const [agentOpen, setAgentOpen] = useState(false);
  const [routeOpen, setRouteOpen] = useState(false);
  const agentChip = useRef<HTMLButtonElement>(null);
  const comboChip = useRef<HTMLButtonElement>(null);
  // An open route panel that no longer exists (the agent changed to one with no route to choose)
  // counts as closed: chips and Esc report only what is on screen.
  const routeShown = routeOpen && !!route;
  const closeAll = () => {
    setAgentOpen(false);
    setRouteOpen(false);
  };
  // Back to a trigger that is on screen: the wide chip, or the combined one when CSS hides it.
  const refocus = () => {
    agentChip.current?.focus();
    if (document.activeElement !== agentChip.current) comboChip.current?.focus();
  };
  // Esc peels the panels before the dialog (the layered stack the Modal also joins).
  useEscLayer(() => {
    closeAll();
    refocus();
  }, agentOpen || routeShown);
  // Picking an agent is the end of that choice; the route and model rows then follow it.
  const pick = (k: string) => {
    onPickKind(k);
    setAgentOpen(false);
    refocus();
  };
  const a = agentOf(kind);
  const routeText = route ? tr("launch.strip.route", { v: route.label }) : "";
  const describedBy = why ? whyId : undefined;
  return (
    <div className="run-strip" data-testid="run-strip">
      <div className="run-strip-line run-strip-line1">
        {kinds.length > 0 && (
          <>
            <button
              ref={agentChip}
              type="button"
              className={"run-chip run-chip--agent " + kindColorClasses(a.cssClass, "seg-btn")}
              aria-expanded={agentOpen}
              aria-controls={agentPanelId}
              aria-label={tr("launch.strip.agent_aria", { v: kindDisplayName(kind) })}
              disabled={disabled}
              onClick={() => setAgentOpen((o) => !o)}
            >
              <Icon name={a.icon} className="seg-ic" />
              {kindDisplayName(kind)}
              <Icon name="chevron-down" className="run-chip-chev" />
            </button>
            {route && (
              <button
                type="button"
                className={"run-chip run-chip--route" + (route.custom ? " run-chip--accent" : "") + (route.problem ? " run-chip--warn" : "")}
                aria-expanded={routeShown}
                aria-controls={routePanelId}
                aria-describedby={describedBy}
                data-custom={route.custom || undefined}
                disabled={disabled}
                onClick={() => setRouteOpen((o) => !o)}
              >
                {routeText}
                <Icon name="chevron-down" className="run-chip-chev" />
              </button>
            )}
            {/* Narrow widths (repos.css): the two chips above fold into this one. */}
            <button
              ref={comboChip}
              type="button"
              className={"run-chip run-chip--combo" + (route?.custom ? " run-chip--accent" : "") + (route?.problem ? " run-chip--warn" : "")}
              aria-expanded={agentOpen || routeShown}
              aria-controls={route ? agentPanelId + " " + routePanelId : agentPanelId}
              aria-describedby={describedBy}
              disabled={disabled}
              onClick={() => {
                const next = !(agentOpen || routeShown);
                setAgentOpen(next);
                setRouteOpen(next && !!route);
              }}
            >
              <Icon name={a.icon} className="seg-ic" />
              {kindDisplayName(kind)}
              {route ? ` · ${route.label}` : ""}
              <Icon name="chevron-down" className="run-chip-chev" />
            </button>
          </>
        )}
      </div>
      {!kinds.length && noAgents}
      <div id={agentPanelId} className="run-strip-panel" hidden={!agentOpen}>
        {/* Wrapping grid (ui.css .ui-seg.big): every installed agent stays visible. */}
        <div className="ui-seg big">
          {kinds.map((k) => {
            const ak = agentOf(k);
            return (
              <button
                key={k}
                type="button"
                title={tr(ak.launchHintKey)}
                aria-pressed={kind === k}
                className={"seg-btn " + kindColorClasses(ak.cssClass, "seg-btn") + (kind === k ? " seg-btn--active" : "")}
                onClick={() => pick(k)}
              >
                <Icon name={ak.icon} className="seg-ic" />
                {kindDisplayName(k)}
              </button>
            );
          })}
        </div>
      </div>
      {route && (
        <div id={routePanelId} className="run-strip-panel" hidden={!routeOpen}>
          {route.panel}
        </div>
      )}
      {why && (
        <span id={whyId} className="ui-field-hint ui-field-hint--warn run-strip-why" role="status">
          {why}
        </span>
      )}
      {model && <div className="run-strip-line run-strip-line2">{model}</div>}
      {below}
    </div>
  );
}

interface RunModelRowProps {
  kind: string;
  model: string;
  onChange: (model: string) => void;
  /** Narrows the choices to what the chosen route can run. */
  filter?: (id: string) => boolean;
}

// RunModelRow is line 2: one single-choice row, exactly one active button. claude: the tier
// aliases, a divider, then the registered full ids most recently launched with (at most
// RECENT_MODELS_MAX, the picked one included) and "+N" for the rest — a pick from "+N" joins the
// row. The other agents' catalogs are live and unbounded, so they keep the searchable picker.
export function RunModelRow({ kind, model, onChange, filter }: RunModelRowProps) {
  // A live catalog is unbounded; the fixed-tier agent (no live models) gets the row below.
  if (agentOf(kind).caps.liveModels) {
    return (
      <div className="run-model run-model--catalog" role="group">
        <ModelPicker kind={kind} model={model} filter={filter} onChange={onChange} />
      </div>
    );
  }
  return <ClaudeModelRow kind={kind} model={model} onChange={onChange} filter={filter} />;
}

function ClaudeModelRow({ kind, model, onChange, filter }: RunModelRowProps) {
  const tr = useT();
  const fetched = useModelOptions(kind);
  const options = useMemo(() => (fetched && filter ? fetched.filter(([v]) => filter(v)) : fetched), [fetched, filter]);
  const registered = useMemo(() => (options ?? []).filter(([v]) => !CLAUDE_ALIASES.includes(v)), [options]);
  const infos = useModelInfos(kind, registered.length > 0);
  // The recent list is read once per mount, so a launch made in another
  // dialog cannot reorder the row under the cursor.
  const [recent] = useState(() => readRecentModels(kind));
  if (!options) return null;
  const aliases = options.filter(([v]) => CLAUDE_ALIASES.includes(v));
  const byId = new Map(registered);
  const active = byId.has(model) ? [model] : [];
  const fromRecent = recent.filter((id) => byId.has(id) && id !== model);
  const rowIds = [...active, ...fromRecent].slice(0, RECENT_MODELS_MAX);
  const rest = registered.filter(([v]) => !rowIds.includes(v));
  const label = (id: string, text: string) => {
    const short = shortModelLabel(text);
    return modelIsRetiring(infos?.get(id) ?? null) ? tr("ui.mi_retiring_suffix", { label: short }) : short;
  };
  const btn = (v: string, text: string, title?: string) => (
    <button
      key={v || "default"}
      type="button"
      title={title ?? v}
      aria-pressed={model === v}
      className={"seg-btn" + (model === v ? " seg-btn--active" : "")}
      onClick={() => onChange(v)}
    >
      {text}
    </button>
  );
  return (
    <div className="run-model run-model--claude" role="group" aria-label={tr("launch.field.model")}>
      <div className="ui-seg run-model-seg">
        {aliases.map(([v, text]) => btn(v, text))}
        {rowIds.length > 0 && <span className="run-model-divider" aria-hidden="true" />}
        {rowIds.map((id) => btn(id, label(id, byId.get(id) ?? id)))}
      </div>
      {rest.length > 0 && (
        <label className="run-model-more">
          <span className="run-model-more-label">
            +{rest.length} <Icon name="chevron-down" />
          </span>
          <select
            value=""
            aria-label={tr("launch.strip.model_more", { n: rest.length })}
            onChange={(e) => e.target.value && onChange(e.target.value)}
          >
            <option value="">{tr("ui.claude_registered_model")}</option>
            {rest.map(([v, text]) => (
              <option key={v} value={v} title={v}>
                {label(v, text)}
              </option>
            ))}
          </select>
        </label>
      )}
      {/* Narrow widths (ui.css): the row above folds into this one control. */}
      <select
        className="run-model-narrow"
        value={model}
        aria-label={tr("launch.field.model")}
        onChange={(e) => onChange(e.target.value)}
      >
        {(options as ModelOption[]).map(([v, text]) => (
          <option key={v} value={v}>
            {label(v, text)}
          </option>
        ))}
      </select>
      {byId.has(model) && (
        <div className="run-model-info">
          <ModelInfoLine kind={kind} model={model} />
        </div>
      )}
    </div>
  );
}
