// RunWithStrip — the "run with" strip under the first prompt of the launch dialog and of the Start
// hub's space form (ADR 0142 decision 7, dated note on the strip). Two lines, ordered by how often
// they change: line 1 holds the agent and the model route (rarely changed, each a chip that opens
// its choices), line 2 the model (changed with almost every prompt) as ONE single-choice row.
// The panels stay mounted while closed (`hidden`): the choices keep their DOM identity, and the
// narrow layout can show them from the one combined chip.
import { useMemo, useState } from "react";
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

const CLAUDE_ALIASES = ["fable", "opus", "sonnet", "haiku"];

/** The label a model button shows: the common `claude-` prefix dropped; the full id is the tooltip. */
export function shortModelLabel(label: string): string {
  return label.replace(/^claude-/, "");
}

type Panel = "agent" | "route" | "both" | null;

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
    /** The launch cannot start until a route is chosen. */
    problem: boolean;
    panel: ReactNode;
  };
  /** Under line 2: what blocks or explains the launch (route reasons, Bedrock fields, notices). */
  below: ReactNode;
  /** Line 2; null when the agent has no model choice. */
  model: ReactNode;
  disabled?: boolean;
}

export function RunWithStrip({ kinds, kind, onPickKind, noAgents, route, below, model, disabled }: RunWithStripProps) {
  const tr = useT();
  const [open, setOpen] = useState<Panel>(null);
  const toggle = (p: Exclude<Panel, null>) => setOpen((o) => (o === p ? null : p));
  // Picking an agent is the end of that choice; the route and model rows then follow it.
  const pick = (k: string) => {
    onPickKind(k);
    setOpen(null);
  };
  const a = agentOf(kind);
  const routeText = route ? tr("launch.strip.route", { v: route.label }) : "";
  return (
    <div className="run-strip" data-testid="run-strip">
      <div className="run-strip-line run-strip-line1">
        {kinds.length > 0 && (
          <>
            <button
              type="button"
              className={"run-chip run-chip--agent " + kindColorClasses(a.cssClass, "seg-btn")}
              aria-expanded={open === "agent"}
              aria-label={tr("launch.strip.agent_aria", { v: kindDisplayName(kind) })}
              disabled={disabled}
              onClick={() => toggle("agent")}
            >
              <Icon name={a.icon} className="seg-ic" />
              {kindDisplayName(kind)}
              <Icon name="chevron-down" className="run-chip-chev" />
            </button>
            {route && (
              <button
                type="button"
                className={"run-chip run-chip--route" + (route.custom ? " run-chip--accent" : "") + (route.problem ? " run-chip--warn" : "")}
                aria-expanded={open === "route"}
                data-custom={route.custom || undefined}
                disabled={disabled}
                onClick={() => toggle("route")}
              >
                {routeText}
                <Icon name="chevron-down" className="run-chip-chev" />
              </button>
            )}
            {/* Narrow widths (ui.css): the two chips above fold into this one. */}
            <button
              type="button"
              className={"run-chip run-chip--combo" + (route?.custom ? " run-chip--accent" : "") + (route?.problem ? " run-chip--warn" : "")}
              aria-expanded={open === "both"}
              disabled={disabled}
              onClick={() => toggle("both")}
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
      <div className="run-strip-panel" hidden={open !== "agent" && open !== "both"}>
        {/* Wrapping grid (ui.css .ui-seg.big): every installed agent stays visible. */}
        <div className="ui-seg big">
          {kinds.map((k) => {
            const ak = agentOf(k);
            return (
              <button
                key={k}
                type="button"
                title={tr(ak.launchHintKey)}
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
        <div className="run-strip-panel" hidden={open !== "route" && open !== "both"}>
          {route.panel}
        </div>
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
            {shortModelLabel(text)}
          </option>
        ))}
        {!options.some(([v]) => v === model) && model && <option value={model}>{shortModelLabel(model)}</option>}
      </select>
      {byId.has(model) && <ModelInfoLine kind={kind} model={model} />}
    </div>
  );
}
