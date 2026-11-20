// MemoryRailSection — the Memory view of the left pane (ADR 0141 decisions 1–3): the filter, the
// consolidator's proposals and the memories grouped by scope. It lists; it never decides or edits.
// A proposal opens alone in the Memory pane, where Approve and Reject live, and a memory opens in
// the pane's viewer with that memory picked.
//
// The view mounts on first show (App: `lazy`), because the memory list is a read of the Agent and a
// hidden view must not make one (ADR 0140 decision 3). The proposals come from the shared store,
// which the activity bar keeps fresh for its badge whether or not this view was ever opened.
//
// A memory whose text fails the secret scan is never listed (the Agent sends a count), so no name
// or description of one reaches this DOM.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, errText, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import { useT, tMaybe } from "../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../lib/intl.ts";
import { Section, SoloSection } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { hasEveryWord, keyOf, matchMemoriesDetail, memoryTarget, scopeKey } from "../settings/memory/memorySearch.ts";
import { isAdviceOnly, matchesFilter } from "../settings/memory/memoryProposals.tsx";
import { proposalSubject } from "../settings/memory/memoryProposalPreview.tsx";
import type { MemoryChange, MemoryListed } from "../settings/memory/memoryTypes.ts";
import { ALL_SCOPES, useMemoryBodySearch } from "./bodySearch.ts";
import { openMemoryView } from "./open.ts";
import { pendingProposals, useMemoryStore } from "./store.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useShownValue } from "../../lib/viewShown.ts";

const TYPES = ["user", "feedback", "project", "reference"] as const;
type Filter = "all" | "pinned" | "forgotten";
const FILTERS = [
  ["all", "mem.md_f_all"],
  ["pinned", "mem.md_f_pinned"],
  ["forgotten", "mem.md_f_forgotten"],
] as const;

const scopeQuery = (scope: string): string => {
  const [kind, projectId = ""] = scope.split(":");
  return kind === ALL_SCOPES ? "" : kind === "user" ? "?scope=user" : `?scope=project&project=${encodeURIComponent(projectId)}`;
};

export function MemoryRailSection() {
  const tr = useT();
  const version = useMemoryStore((s) => s.version);
  const proposals = useMemoryStore((s) => s.proposals);
  const proposalsErr = useMemoryStore((s) => s.proposalsErr);
  const openSettings = useSettingsUI((s) => s.openSettings);
  // The proposal the Memory pane shows, so its row reads as the current one.
  const openProposal = useLayoutStore((s) => {
    const c = allViews(s.layout).find((p) => p.content.kind === "memory")?.content;
    return c?.kind === "memory" ? (c.proposalId ?? "") : "";
  });

  const [rows, setRows] = useState<MemoryListed[] | null>(null);
  const [withheld, setWithheld] = useState(0);
  const [loadErr, setLoadErr] = useState("");
  const [scope, setScope] = useState(ALL_SCOPES);
  const [filter, setFilter] = useState<Filter>("all");
  const [type, setType] = useState("");
  const [query, setQuery] = useState("");
  const [allProposals, setAllProposals] = useState(false);

  // A write or a tenant switch while the view is hidden reloads on the next show, not for nobody
  // (ADR 0140 decision 3). A retry already scheduled when the view was hidden still runs once.
  const tenant = useTenantStore((s) => s.tenant);
  const loadVersion = useShownValue(version);
  const loadTenant = useShownValue(tenant);
  const load = useCallback(async (signal: AbortSignal) => {
    const list = await api("api/agents/memory/entries/list");
    if (signal.aborted) return true;
    if (isTransientErr(list)) return false;
    setLoadErr(list?.error ? errText(list.error) : "");
    setRows(list?.error ? [] : (list?.entries ?? []));
    setWithheld(list?.error ? 0 : (list?.withheld ?? 0));
    return true;
  }, []);
  // Another tenant is another workspace: its list replaces this one, so this one is not shown
  // while that answer is on its way.
  const listTenant = useRef(loadTenant);
  useEffect(() => {
    if (listTenant.current === loadTenant) return;
    listTenant.current = loadTenant;
    setRows(null);
    setWithheld(0);
    setLoadErr("");
  }, [loadTenant]);
  useRetryLoad(load, [loadVersion, loadTenant]);

  // Every scope there is a memory in. A chosen scope that no longer holds one falls back to all,
  // and every read below asks for the scope on screen, never the stale choice.
  const scopes = useMemo(() => {
    const out = new Map<string, string>([
      [ALL_SCOPES, tr("mem.br_scope_all")],
      ["user", tr("mem.af_scope_user")],
    ]);
    for (const m of rows ?? []) if (m.scope === "project" && m.project) out.set(scopeKey(m), m.project.display);
    return [...out.entries()];
  }, [rows, tr]);
  const scopeShown = scopes.some(([k]) => k === scope) ? scope : ALL_SCOPES;

  const forgotten = useForgotten(filter === "forgotten", scopeShown, loadTenant, `${loadVersion}/${loadTenant}`);

  const q = query.trim().toLowerCase();
  const search = useMemoryBodySearch(filter === "forgotten" ? "" : q, scopeShown);

  const inScope = (rows ?? []).filter((m) => scopeShown === ALL_SCOPES || scopeKey(m) === scopeShown);
  const { rows: matched, partial } = matchMemoriesDetail(inScope, search.hits, q, search.fallback);
  const shown = matched.filter((m) => (filter !== "pinned" || m.pinned) && (!type || m.type === type));
  const groups = useMemo(() => groupByScope(shown, tr("mem.af_scope_user")), [shown, tr]);

  const whereOf = (c: { scope: string; project?: { display: string } }) =>
    c.scope === "user" ? tr("mem.af_scope_user") : (c.project?.display ?? "");
  const goneShown = (forgotten.rows ?? []).filter(
    (c) =>
      (scopeShown === ALL_SCOPES || scopeKey(c) === scopeShown) &&
      (!q || hasEveryWord(`${c.name}\n${whereOf(c)}\n${c.authorKind}\n${c.authorSession}`.toLowerCase(), q)),
  );

  const props = (proposals ?? []).filter((p) => matchesFilter(p.status, allProposals ? "all" : "pending"));
  const pending = pendingProposals(proposals);
  // What an empty memory list says: a body search still on its way is not "no match" yet.
  const emptyKey =
    q && search.searching
      ? "mem.br_searching"
      : q || type
        ? "mem.br_no_match"
        : filter === "pinned"
          ? "mem.md_pinned_empty"
          : "mem.rv_list_empty";

  // The view's head section (solo: always open) holds the filter; Proposals and Memories are its
  // siblings, as the Sessions view stacks its sections. Nested, their sticky heads would pin over
  // the view's own head and hide its settings button.
  return (
    <div className="memrail">
      <SoloSection>
        <Section
          title={tr("rail.view.memory")}
          icon="archive"
          actions={<IconButton icon="settings-gear" label={tr("mem.mv_open_settings")} onClick={() => openSettings("memory")} />}
        >
          <div className="memrail-filter">
            <input
              type="search"
              aria-label={tr("mem.br_search")}
              placeholder={tr("mem.br_search")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <div className="memrail-selects">
              <select aria-label={tr("mem.br_scope")} value={scopeShown} onChange={(e) => setScope(e.target.value)}>
                {scopes.map(([k, label]) => (
                  <option key={k} value={k}>
                    {label}
                  </option>
                ))}
              </select>
              <select aria-label={tr("mem.br_f_type")} value={type} disabled={filter === "forgotten"} onChange={(e) => setType(e.target.value)}>
                <option value="">{tr("mem.rv_type_all")}</option>
                {TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
            </div>
            <div className="memrail-chips" role="group" aria-label={tr("mem.md_filter")}>
              {FILTERS.map(([f, label]) => (
                <button key={f} type="button" className={"memrail-chip" + (f === filter ? " active" : "")} aria-pressed={f === filter} onClick={() => setFilter(f)}>
                  {tr(label)}
                </button>
              ))}
            </div>
          </div>
        </Section>
      </SoloSection>

      <Section
        id="memory-proposals"
        title={tr("mem.view_proposals")}
        count={pending}
        actions={
          // A fixed label with its pressed state: a label that flips with the state reads backwards
          // to a screen reader ("Pending, not pressed" while pending ones are shown).
          <button
            type="button"
            className={"memrail-toggle" + (allProposals ? " active" : "")}
            aria-pressed={allProposals}
            title={tr("mem.rv_prop_show_all")}
            onClick={() => setAllProposals((v) => !v)}
          >
            {tr("mem.md_f_all")}
          </button>
        }
      >
        {proposalsErr && <p className="memrail-note warn">{proposalsErr}</p>}
        <ul className="memrail-list" aria-label={tr("mem.view_proposals")}>
          {proposals === null ? (
            <li className="memrail-note">{tr("common.loading")}</li>
          ) : props.length === 0 ? (
            !proposalsErr && <li className="memrail-note">{tr(allProposals ? "mem.cs_empty" : "mem.rv_prop_empty")}</li>
          ) : (
            props.map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  className={"memrail-row" + (p.id === openProposal ? " active" : "")}
                  aria-current={p.id === openProposal ? "true" : undefined}
                  data-proposal-id={p.id}
                  onClick={() => openMemoryView("proposals", { proposalId: p.id })}
                >
                  <span className="memrail-line">
                    <span className="memrail-name">{proposalSubject(p)}</span>
                    {allProposals && <span className={"mem-badge memrail-status cs-" + p.status}>{tMaybe("mem.cs_status_" + p.status) ?? p.status}</span>}
                  </span>
                  <span className="memrail-sub">
                    {fmtDateTime(p.created, DATETIME_FULL)}
                    {" · "}
                    {isAdviceOnly(p) ? tr("mem.cs_advice") : tr("mem.cs_items", { n: p.items.length })}
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
      </Section>

      <Section id="memory-list" title={tr("mem.view_memories")} count={filter === "forgotten" ? goneShown.length : shown.length}>
        {loadErr && <p className="memrail-note warn">{loadErr}</p>}
        {withheld > 0 && filter !== "forgotten" && <p className="memrail-note warn">{tr("mem.br_withheld", { n: withheld })}</p>}
        {search.failed && <p className="memrail-note warn">{tr("mem.br_search_failed")}</p>}
        {partial && filter !== "forgotten" && <p className="memrail-note">{tr("mem.br_search_partial")}</p>}
        {filter === "forgotten" ? (
          <>
            {forgotten.err && <p className="memrail-note warn">{tr("mem.br_forgotten_failed")}</p>}
            <ul className="memrail-list" aria-label={tr("mem.md_f_forgotten")}>
              {forgotten.rows === null ? (
                <li className="memrail-note">{tr("common.loading")}</li>
              ) : goneShown.length === 0 ? (
                !forgotten.err && <li className="memrail-note">{tr(q ? "mem.br_no_match" : "mem.md_forgotten_empty")}</li>
              ) : (
                goneShown.map((c) => (
                  <li key={c.commit}>
                    <button
                      type="button"
                      className="memrail-row gone"
                      data-row-key={keyOf(c)}
                      onClick={() => openMemoryView("memories", { target: memoryTarget(c, true) })}
                    >
                      <span className="memrail-line">
                        <span className="memrail-name">{c.name}</span>
                      </span>
                      <span className="memrail-sub">
                        {whereOf(c)}
                        {" · "}
                        {fmtDateTime(c.at, DATETIME_FULL)}
                      </span>
                    </button>
                  </li>
                ))
              )}
            </ul>
            {forgotten.moreErr && <p className="memrail-note warn">{tr("mem.br_forgotten_more_failed")}</p>}
            {forgotten.next && (
              <button type="button" className="memrail-link" disabled={forgotten.moreBusy} onClick={() => void forgotten.more()}>
                {tr("mem.br_forgotten_more")}
              </button>
            )}
          </>
        ) : rows === null ? (
          <p className="memrail-note">{tr("common.loading")}</p>
        ) : shown.length === 0 ? (
          !loadErr && <p className="memrail-note">{tr(emptyKey)}</p>
        ) : (
          groups.map(([k, label, list]) => (
            <div key={k} className="memrail-group">
              <div className="memrail-group-head" title={label}>
                {label}
              </div>
              <ul className="memrail-list">
                {list.map((m) => (
                  <li key={keyOf(m)}>
                    <button
                      type="button"
                      className="memrail-row"
                      data-row-key={keyOf(m)}
                      onClick={() => openMemoryView("memories", { target: memoryTarget(m) })}
                    >
                      <span className="memrail-line">
                        {m.pinned && <Icon name="pinned" className="memrail-pin" title={tr("mem.br_pinned")} />}
                        <span className="memrail-name">{m.name}</span>
                        {m.type && <span className="memrail-type">{m.type}</span>}
                      </span>
                      {m.description && (
                        <span className="memrail-sub" title={m.description}>
                          {m.description}
                        </span>
                      )}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ))
        )}
      </Section>

      <button type="button" className="memrail-link" onClick={() => openMemoryView("changes")}>
        <Icon name="history" /> {tr("mem.rv_open_changes")}
      </button>
    </div>
  );
}

/** The rows by scope as [scope key, label, rows]: the user scope first, then each project by name;
 *  pinned first within each. Keyed by scope, not label: two projects can share a display name. */
export function groupByScope(rows: MemoryListed[], userLabel: string): [string, string, MemoryListed[]][] {
  const by = new Map<string, { label: string; list: MemoryListed[] }>();
  for (const m of rows) {
    const k = scopeKey(m);
    const label = m.scope === "user" ? userLabel : (m.project?.display ?? "");
    const g = by.get(k) ?? { label, list: [] };
    g.list.push(m);
    by.set(k, g);
  }
  const keys = [...by.keys()].sort((a, b) => (a === "user" ? -1 : b === "user" ? 1 : by.get(a)!.label.localeCompare(by.get(b)!.label)));
  // Array.prototype.sort is stable, so the list's own order holds within pinned and unpinned.
  return keys.map((k) => [k, by.get(k)!.label, [...by.get(k)!.list].sort((a, b) => Number(b.pinned) - Number(a.pinned))]);
}

/** The forgotten memories of a scope, read only while the Forgotten chip is on, with paging.
 *  `tenant` and `reloadKey` are as of the last show; `reloadKey` also moves on a write. */
function useForgotten(active: boolean, scope: string, tenant: string, reloadKey: string) {
  const [rows, setRows] = useState<MemoryChange[] | null>(null);
  const [err, setErr] = useState(false);
  const [moreErr, setMoreErr] = useState(false);
  const [next, setNext] = useState("");
  const [moreBusy, setMoreBusy] = useState(false);
  // The generation of the list on screen. Moved on before any read starts, so an older page that
  // lands later (another scope's, or one asked before a reload) is dropped instead of merged.
  const gen = useRef(0);

  // The cursor belongs to the list it came from: a new scope, chip or reload starts without one,
  // so "more" can never send another scope's cursor while the new first page is on its way.
  useEffect(() => {
    gen.current++;
    setNext("");
    setMoreBusy(false);
    setMoreErr(false);
  }, [active, scope, reloadKey]);
  // Another scope or tenant is another list: the old one is not shown while the new one loads.
  const listOf = useRef(scope + "\n" + tenant);
  useEffect(() => {
    const now = scope + "\n" + tenant;
    if (listOf.current !== now) setRows(null);
    listOf.current = now;
  }, [scope, tenant]);

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!active) return true;
      const mine = gen.current;
      const res = await api("api/agents/memory/entries/member-forgotten" + scopeQuery(scope));
      if (signal.aborted || mine !== gen.current) return true;
      if (isTransientErr(res)) return false;
      setErr(!!res?.error);
      setRows(res?.error ? [] : (res?.forgotten ?? []));
      setNext(res?.error ? "" : (res?.next ?? ""));
      return true;
    },
    [active, scope],
  );
  useRetryLoad(load, [active, scope, reloadKey]);

  const more = async () => {
    const mine = gen.current;
    const cursor = next;
    if (!cursor) return;
    const sq = scopeQuery(scope);
    setMoreBusy(true);
    setMoreErr(false);
    const res = await api("api/agents/memory/entries/member-forgotten" + sq + (sq ? "&" : "?") + "before=" + encodeURIComponent(cursor)).catch(() => ({
      error: true,
    }));
    if (mine !== gen.current) return;
    setMoreBusy(false);
    if (res?.error) return setMoreErr(true);
    const page: MemoryChange[] = res?.forgotten ?? [];
    // A memory forgotten again since the first page is already listed under its newer row.
    setRows((prev) => {
      const again = new Set(page.map(keyOf));
      return [...(prev ?? []).filter((c) => !again.has(keyOf(c))), ...page];
    });
    setNext(res?.next ?? "");
  };

  return { rows, err, moreErr, next, moreBusy, more };
}
