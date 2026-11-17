// The rule editor and the read-only rule summary of the Model access pages (ADR 0138 D1, D16).
// The tenant page edits the tenant's own rule with it; the admin page the deployment rule and a
// tenant's cap. What is sent is ruleFromDraft's: the rule as loaded with the edited fields over it.
//
// A refusal stays on the page beside the save button, with the edits kept, so the administrator
// can read why and fix the one value rather than start again. A toast would vanish, and a reload
// would throw the edits away.
import { useState } from "react";
import { errDetail } from "../../../core/api/client.ts";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { useReportDirty } from "../parts/dirtyScope.tsx";
import {
  ROUTE_CLASSES,
  ROUTE_DESTINATIONS,
  draftOf,
  endpointLines,
  isPerConnection,
  restricts,
  ruleEqual,
  ruleFromDraft,
  withExtra,
  type RouteRule,
  type RuleDraft,
} from "./routePolicyRule.ts";

type Tr = ReturnType<typeof useT>;

const CLASS_KEY = {
  subscription: "admin.mpol_class_subscription",
  "member-key": "admin.mpol_class_member_key",
  "member-cloud": "admin.mpol_class_member_cloud",
  provided: "admin.mpol_class_provided",
  "self-hosted": "admin.mpol_class_self_hosted",
} as const;

const DEST_KEY = {
  anthropic: "admin.mpol_dest_anthropic",
  openai: "admin.mpol_dest_openai",
  google: "admin.mpol_dest_google",
  meta: "admin.mpol_dest_meta",
  github: "admin.mpol_dest_github",
  cursor: "admin.mpol_dest_cursor",
  kiro: "admin.mpol_dest_kiro",
  "opencode.ai": "admin.mpol_dest_opencode_ai",
  openrouter: "admin.mpol_dest_openrouter",
  sakana: "admin.mpol_dest_sakana",
  "aws-bedrock": "admin.mpol_dest_aws_bedrock",
  "af-engine": "admin.mpol_dest_af_engine",
  "self-hosted": "admin.mpol_dest_self_hosted",
} as const;

const LAYER_KEY = {
  ceiling: "admin.mpol_layer_ceiling",
  deployment: "admin.mpol_layer_deployment",
  tenant_cap: "admin.mpol_layer_tenant_cap",
  tenant: "admin.mpol_layer_tenant",
  vendor: "admin.mpol_layer_vendor",
} as const;

// A value the Console has no label for (a newer CP's) is shown as its id rather than dropped.
export const classLabel = (tr: Tr, c: string) => (Object.hasOwn(CLASS_KEY, c) ? tr(CLASS_KEY[c as keyof typeof CLASS_KEY]) : c);
export const destLabel = (tr: Tr, d: string) => (Object.hasOwn(DEST_KEY, d) ? tr(DEST_KEY[d as keyof typeof DEST_KEY]) : d);
export const layerLabel = (tr: Tr, l: string) => (Object.hasOwn(LAYER_KEY, l) ? tr(LAYER_KEY[l as keyof typeof LAYER_KEY]) : l);
export const layersLabel = (tr: Tr, ls: string[] | undefined) => (ls ?? []).map((l) => layerLabel(tr, l)).join(", ");

/** RouteName: the member-facing route name. The id the policy and the CP's refusals use is on
 *  hover; a route the Console has no name for is shown as its id. */
export function RouteName({ id }: { id: string }) {
  const name = tMaybe("cred.route." + id);
  return name ? (
    <span className="mpol-route" title={id}>
      {name}
    </span>
  ) : (
    <code className="mpol-route">{id}</code>
  );
}

export const routeLabel = (id: string) => tMaybe("cred.route." + id) ?? id;

const CLASS_DESC = {
  subscription: "admin.mpol_class_desc_subscription",
  "member-key": "admin.mpol_class_desc_member_key",
  "member-cloud": "admin.mpol_class_desc_member_cloud",
  provided: "admin.mpol_class_desc_provided",
  "self-hosted": "admin.mpol_class_desc_self_hosted",
} as const;
const classDesc = (tr: Tr, c: string) =>
  Object.hasOwn(CLASS_DESC, c) ? tr(CLASS_DESC[c as keyof typeof CLASS_DESC]) : undefined;

/** RuleSummary draws one layer's rule read-only. ceiling marks the operator's layer, whose unset
 *  endpoint list means "none" once it restricts destinations (ADR 0138 §2). */
export function RuleSummary({ rule, ceiling = false }: { rule: RouteRule; ceiling?: boolean }) {
  const tr = useT();
  const list = (l: string[] | undefined, label: (v: string) => string) =>
    l === undefined ? tr("admin.mpol_all") : l.length === 0 ? tr("admin.mpol_none") : l.map(label).join(", ");
  const endpoints =
    rule.endpoints !== undefined
      ? rule.endpoints.length
        ? rule.endpoints.join(", ")
        : tr("admin.mpol_none")
      : ceiling && rule.destinations !== undefined
        ? tr("admin.mpol_ceiling_endpoints_none")
        : tr("admin.mpol_endpoints_all");
  // The heading stays outside the list: a dt inside it needs a dd, and that dd would take the
  // next row's label cell and pair every label below with the wrong value.
  return (
    <>
      {!restricts(rule) && <p className="mpol-summary-head">{tr("admin.mpol_unrestricted")}</p>}
      <dl className="mpol-summary">
        <dt>{tr("admin.mpol_classes")}</dt>
        <dd>{list(rule.classes, (c) => classLabel(tr, c))}</dd>
        <dt>{tr("admin.mpol_destinations")}</dt>
        <dd>{list(rule.destinations, (d) => destLabel(tr, d))}</dd>
        {(rule.except?.length ?? 0) > 0 && (
          <>
            <dt>{tr("admin.mpol_except_short")}</dt>
            <dd>{rule.except!.map(routeLabel).join(", ")}</dd>
          </>
        )}
        {(rule.deny?.length ?? 0) > 0 && (
          <>
            <dt>{tr("admin.mpol_deny_short")}</dt>
            <dd>{rule.deny!.map(routeLabel).join(", ")}</dd>
          </>
        )}
        <dt>{tr("admin.mpol_endpoints_short")}</dt>
        <dd>{endpoints}</dd>
      </dl>
    </>
  );
}

const toggle = (l: string[], v: string, on: boolean) => (on ? [...l.filter((x) => x !== v), v] : l.filter((x) => x !== v));

/** AllOrOnly: the "every value" / "only these" choice and, under "only these", one tick per
 *  value. Ticking nothing under "only these" is a real rule (none allowed), not the same as all. */
function AllOrOnly({
  name,
  legend,
  all,
  values,
  options,
  label,
  describe,
  disabled,
  onChange,
}: {
  name: string;
  legend: string;
  all: boolean;
  values: string[];
  options: string[];
  label: (v: string) => string;
  /** One line under a value saying what it covers; nothing for a value without one. */
  describe?: (v: string) => string | undefined;
  disabled: boolean;
  onChange: (all: boolean, values: string[]) => void;
}) {
  const tr = useT();
  return (
    <fieldset className="admin-fgroup mpol-field" data-field={name}>
      <legend className="admin-subhead">{legend}</legend>
      <span className="seg sm" role="radiogroup" aria-label={legend}>
        <button
          type="button"
          role="radio"
          aria-checked={all}
          className={"seg-btn" + (all ? " active" : "")}
          disabled={disabled}
          onClick={() => onChange(true, values)}
        >
          {tr("admin.mpol_all")}
        </button>
        <button
          type="button"
          role="radio"
          aria-checked={!all}
          className={"seg-btn" + (!all ? " active" : "")}
          disabled={disabled}
          onClick={() => onChange(false, values)}
        >
          {tr("admin.mpol_only")}
        </button>
      </span>
      {!all && (
        <div className="mpol-ticks">
          {options.map((o) => (
            <label key={o} className="admin-check">
              <input
                type="checkbox"
                checked={values.includes(o)}
                disabled={disabled}
                onChange={(e) => onChange(false, toggle(values, o, e.target.checked))}
              />
              <span>
                {label(o)}
                {describe?.(o) && <span className="mpol-desc">{describe(o)}</span>}
              </span>
            </label>
          ))}
        </div>
      )}
    </fieldset>
  );
}

function RouteTicks({
  name,
  legend,
  hint,
  values,
  options,
  disabledIds,
  disabled,
  onChange,
}: {
  name: string;
  legend: string;
  hint: string;
  values: string[];
  options: string[];
  disabledIds?: (id: string) => boolean;
  disabled: boolean;
  onChange: (values: string[]) => void;
}) {
  return (
    <fieldset className="admin-fgroup mpol-field" data-field={name}>
      <legend className="admin-subhead">{legend}</legend>
      <p className="admin-hint">{hint}</p>
      <div className="mpol-ticks">
        {options.map((id) => (
          <label key={id} className="admin-check">
            <input
              type="checkbox"
              aria-label={`${legend}: ${id}`}
              checked={values.includes(id)}
              // A value already stored stays untickable so it can be removed, even where it could
              // not be added.
              disabled={disabled || (!values.includes(id) && !!disabledIds?.(id))}
              onChange={(e) => onChange(toggle(values, id, e.target.checked))}
            />
            <RouteName id={id} />
          </label>
        ))}
      </div>
    </fieldset>
  );
}

export interface HostState {
  approved: boolean;
  excludedBy?: string[];
}

/** ServerRows edits the endpoint list as rows: one per host or pattern, a remove button each, and
 *  a field to add more. The draft keeps its text form (one entry per line), so what is saved is
 *  still ruleFromDraft's reading of that text. */
function ServerRows({
  label,
  text,
  stateOf,
  disabled,
  onChange,
}: {
  label: string;
  text: string;
  stateOf?: (host: string) => HostState | undefined;
  disabled: boolean;
  onChange: (text: string) => void;
}) {
  const tr = useT();
  const [adding, setAdding] = useState("");
  const rows = endpointLines(text);
  const add = () => {
    const more = [...new Set(endpointLines(adding))].filter((h) => !rows.includes(h));
    if (more.length > 0) onChange([...rows, ...more].join("\n"));
    setAdding("");
  };
  return (
    <div className="mpol-servers">
      {rows.length === 0 ? (
        <p className="muted">{tr("admin.mpol_servers_none")}</p>
      ) : (
        <ul className="mpol-server-rows" aria-label={label}>
          {rows.map((h) => {
            const st = stateOf?.(h);
            return (
              <li key={h} data-server={h}>
                <code>{h}</code>
                {stateOf &&
                  (st === undefined ? (
                    <span className="muted">{tr("admin.mpol_server_unsaved")}</span>
                  ) : st.approved ? (
                    <span className="mpol-ok">{tr("admin.mpol_approved")}</span>
                  ) : (
                    <span className="mpol-no">{tr("admin.mpol_excluded_by", { layers: layersLabel(tr, st.excludedBy) })}</span>
                  ))}
                <button
                  type="button"
                  className="btn xs"
                  aria-label={tr("admin.mpol_server_remove", { host: h })}
                  disabled={disabled}
                  onClick={() => onChange(rows.filter((x) => x !== h).join("\n"))}
                >
                  ×
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <div className="mpol-server-add">
        <input
          type="text"
          aria-label={tr("admin.mpol_server_new")}
          placeholder={tr("admin.mpol_server_new")}
          value={adding}
          disabled={disabled}
          onChange={(e) => setAdding(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) {
              e.preventDefault();
              add();
            }
          }}
        />
        <button type="button" className="btn" disabled={disabled || endpointLines(adding).length === 0} onClick={add}>
          {tr("admin.mpol_server_add")}
        </button>
      </div>
    </div>
  );
}

/** RuleEditor edits one layer's rule. save sends the body and answers the API result; a result
 *  with an error is shown and the edits kept. Re-key it on the loaded revision so a fresh load
 *  replaces the draft. */
export function RuleEditor({
  base,
  revision,
  currentRevision,
  routeOptions,
  endpointsMode,
  hostState,
  save,
  onSaved,
}: {
  base: RouteRule;
  /** The layer's revision base was loaded at. */
  revision: number;
  /** Reads the layer's stored revision now; null when it cannot be read. */
  currentRevision: () => Promise<number | null>;
  routeOptions: string[];
  /** patterns: the deployment rule and a cap (absent = does not narrow); hosts: the tenant
   *  rule's approved exact hosts. */
  endpointsMode: "patterns" | "hosts";
  /** A stored host's effect (the tenant page's endpoint decisions); undefined for one not saved. */
  hostState?: (host: string) => HostState | undefined;
  save: (body: RouteRule) => Promise<unknown>;
  onSaved: () => void;
}) {
  const tr = useT();
  const [draft, setDraft] = useState<RuleDraft>(() => draftOf(base));
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const body = ruleFromDraft(base, draft);
  const dirty = !ruleEqual(body, base);
  useReportDirty(dirty);
  const edit = (patch: Partial<RuleDraft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    setRefusal(null);
  };

  const submit = async () => {
    setBusy(true);
    setRefusal(null);
    try {
      // The PUT replaces the whole layer and carries no revision, so a save made over another
      // administrator's newer one would silently undo it. Re-read first and refuse on a moved
      // (or unreadable) revision; the edits stay. A save in the gap between the two requests can
      // still race, which only a conditional PUT on the CP would close.
      const now = await currentRevision();
      if (now === null) {
        setRefusal(tr("admin.mpol_load_failed"));
        return;
      }
      if (now !== revision) {
        setRefusal(tr("admin.mpol_stale"));
        return;
      }
      const res = (await save(body)) as { error?: Parameters<typeof errDetail>[0] } | null | undefined;
      if (res?.error) {
        setRefusal(errDetail(res.error));
        return;
      }
      onSaved();
    } catch {
      setRefusal(tr("admin.mpol_save_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mpol-editor">
      <AllOrOnly
        name="classes"
        legend={tr("admin.mpol_classes")}
        all={draft.classesAll}
        values={draft.classes}
        options={withExtra(ROUTE_CLASSES, base.classes)}
        label={(c) => classLabel(tr, c)}
        describe={(c) => classDesc(tr, c)}
        disabled={busy}
        onChange={(all, classes) => edit({ classesAll: all, classes })}
      />
      <AllOrOnly
        name="destinations"
        legend={tr("admin.mpol_destinations")}
        all={draft.destinationsAll}
        values={draft.destinations}
        options={withExtra(ROUTE_DESTINATIONS, base.destinations)}
        label={(d) => destLabel(tr, d)}
        disabled={busy}
        onChange={(all, destinations) => edit({ destinationsAll: all, destinations })}
      />
      {/* Folded: forty-odd ticks are rarely what an administrator came to change. */}
      <details className="admin-fgroup mpol-individual" data-field="individual">
        <summary>
          {draft.except.length + draft.deny.length === 0
            ? tr("admin.mpol_individual_none")
            : tr("admin.mpol_individual", { n: draft.except.length + draft.deny.length })}
        </summary>
        <RouteTicks
          name="except"
          legend={tr("admin.mpol_except")}
          hint={tr("admin.mpol_except_hint")}
          values={draft.except}
          options={withExtra(routeOptions, base.except)}
          disabledIds={isPerConnection}
          disabled={busy}
          onChange={(except) => edit({ except })}
        />
        <RouteTicks
          name="deny"
          legend={tr("admin.mpol_deny")}
          hint={tr("admin.mpol_deny_hint")}
          values={draft.deny}
          options={withExtra(routeOptions, base.deny)}
          disabled={busy}
          onChange={(deny) => edit({ deny })}
        />
      </details>
      <fieldset className="admin-fgroup mpol-field" data-field="endpoints">
        <legend className="admin-subhead">
          {tr(endpointsMode === "hosts" ? "admin.mpol_endpoints_hosts" : "admin.mpol_endpoints_patterns")}
        </legend>
        <p className="admin-hint">
          {tr(endpointsMode === "hosts" ? "admin.mpol_endpoints_hosts_hint" : "admin.mpol_endpoints_patterns_hint")}
        </p>
        {endpointsMode === "patterns" && (
          <span className="seg sm" role="radiogroup" aria-label={tr("admin.mpol_endpoints_patterns")}>
            <button
              type="button"
              role="radio"
              aria-checked={draft.endpointsAll}
              className={"seg-btn" + (draft.endpointsAll ? " active" : "")}
              disabled={busy}
              onClick={() => edit({ endpointsAll: true })}
            >
              {tr("admin.mpol_endpoints_all")}
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={!draft.endpointsAll}
              className={"seg-btn" + (!draft.endpointsAll ? " active" : "")}
              disabled={busy}
              onClick={() => edit({ endpointsAll: false })}
            >
              {tr("admin.mpol_only")}
            </button>
          </span>
        )}
        {(endpointsMode === "hosts" || !draft.endpointsAll) && (
          <ServerRows
            label={tr(endpointsMode === "hosts" ? "admin.mpol_endpoints_hosts" : "admin.mpol_endpoints_patterns")}
            text={draft.endpoints}
            stateOf={endpointsMode === "hosts" ? hostState : undefined}
            disabled={busy}
            onChange={(text) =>
              // The tenant rule has no "does not narrow": its list is the hosts it approves, and a
              // rule loaded without one stays without one until a host is added.
              edit(
                endpointsMode === "hosts"
                  ? { endpoints: text, endpointsAll: base.endpoints === undefined && text.trim() === "" }
                  : { endpoints: text },
              )
            }
          />
        )}
      </fieldset>
      {refusal && (
        <p className="admin-hint warn" role="alert">
          {tr("admin.mpol_refused", { reason: refusal })}
        </p>
      )}
      <div className="admin-actions">
        <button type="button" className="btn primary" disabled={busy || !dirty} onClick={submit}>
          {tr("admin.mpol_save")}
        </button>
        <button
          type="button"
          className="btn"
          disabled={busy || !dirty}
          onClick={() => {
            setDraft(draftOf(base));
            setRefusal(null);
          }}
        >
          {tr("admin.mpol_reset")}
        </button>
      </div>
    </div>
  );
}
