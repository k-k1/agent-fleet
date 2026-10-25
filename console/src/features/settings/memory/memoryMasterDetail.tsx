// MemoriesMasterDetail — the member's one place for AF-owned agent memory (#1985, #2324): a list of
// the memories of all scopes or of one (scope select, search over fields then bodies, filter chips
// All / Pinned / Forgotten) beside the detail pane of the one picked. The pane holds the body and
// metadata, Edit, the Pin toggle (#1703: a pinned memory sits first in the index agents read at the
// start of work), Forget, and in an overflow menu the purge of its past revisions (#1982), plus its
// own history. Edit and New open in the pane too, so nothing opens off-screen below a long list.
//
// Forget and unforget are the changes list's own revert route, on the newest change of the memory. A
// forget resolves that change on demand (member-latest) and the forgotten list comes from
// member-forgotten (the store's tombstones), so neither depends on how many recent changes were
// loaded. A save goes through member-save, which runs the agents' memory_save checks (revision,
// secret scan, limits) and records the member as author; the CP signs it, revert and pin so the
// Agent can tell them from a session (ADR 0113, #1997).
//
// A memory whose text fails the secret scan is never listed (the Agent sends a count), so no name or
// body of one reaches this DOM. Search matches the list's own fields at once (name, description,
// type, author) and, after a pause, bodies through member-search, which counts no use of the
// memories it finds.
//
// At phone width the list and the detail are two screens (list -> detail, Back returns). The CSS
// shows one of them by `data-pane`; the markup is the same at every width.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { PurgeHistoryDialog, type PurgeTarget } from "./memoryPurge.tsx";
import { MemoryHistory, useAllChanges } from "./memoryHistory.tsx";
import { useSettingsUI } from "../store.ts";
import { hasEveryWord, keyOf, matchMemoriesDetail, parseMemoryTarget, scopeKey } from "./memorySearch.ts";
import type { MemoryBody, MemoryChange, MemoryListed, MemorySearchHit, SecretFinding } from "./memoryTypes.ts";

const TYPES = ["user", "feedback", "project", "reference"] as const;
const projectQuery = (project?: string) => (project ? "?project=" + encodeURIComponent(project) : "");

/** Pause after the last keystroke before the body search is asked. */
const SEARCH_DELAY_MS = 300;

// The default selection: every scope at once (list, field filter and body search).
const ALL = "all";

type Filter = "all" | "pinned" | "forgotten";
const FILTERS = [
  ["all", "mem.md_f_all"],
  ["pinned", "mem.md_f_pinned"],
  ["forgotten", "mem.md_f_forgotten"],
] as const;


/** The memory the detail pane shows. A forgotten one has no body to read, only its last change. */
interface Selected {
  scope: "user" | "project";
  /** Project id; "" for the user scope. */
  project: string;
  name: string;
  gone: boolean;
}
const selKey = (s: Selected) => keyOf({ scope: s.scope, project: { id: s.project }, name: s.name });
const selFrom = (m: { scope: string; project?: { id: string }; name: string }, gone: boolean): Selected => ({
  scope: m.scope === "user" ? "user" : "project",
  project: m.scope === "user" ? "" : (m.project?.id ?? ""),
  name: m.name,
  gone,
});

// What is remembered while the page lives: leaving Memories for Changes and coming back finds the
// same scope, filter and pick. Never persisted, and a reload of the page starts clean.
const session: { scope: string; filter: Filter; sel: Selected | null } = { scope: ALL, filter: "all", sel: null };
/** Forget the remembered pick; tests call it between cases. */
export function resetMemoriesSession() {
  session.scope = ALL;
  session.filter = "all";
  session.sel = null;
}

interface Draft {
  /** null: a new memory. */
  base: MemoryBody | null;
  /** Where the form was opened, fixed: the scope selector may move while the form is open, and a
   *  save must never land in another scope with this revision and text. */
  scope: "user" | "project";
  project: string;
  kinds: string[];
  name: string;
  description: string;
  type: string;
  body: string;
}

export function MemoriesMasterDetail({
  reload,
  onChanged,
  onOpenChanges,
}: {
  reload: number;
  onChanged: () => void;
  /** Where "see all in Changes" goes; absent: the link is not offered. */
  onOpenChanges?: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [rows, setRows] = useState<MemoryListed[] | null>(null);
  const [withheld, setWithheld] = useState(0);
  const [forgotten, setForgotten] = useState<MemoryChange[]>([]);
  const [goneLoaded, setGoneLoaded] = useState(false);
  const [goneWithheld, setGoneWithheld] = useState(0);
  // The cursor of the next page of forgotten memories ("" = the list is complete), and whether one
  // is being read. `goneGen` drops an answer that a reload or a scope change has outdated.
  const [goneNext, setGoneNext] = useState("");
  const [goneMoreBusy, setGoneMoreBusy] = useState(false);
  const [goneMoreErr, setGoneMoreErr] = useState(false);
  const goneGen = useRef(0);
  const [goneErr, setGoneErr] = useState(false);
  const [goneIncomplete, setGoneIncomplete] = useState(false);
  // The projects that hold a forgotten memory, as the Agent last named them (it names them even
  // when their rows did not fit). Kept across scope changes so a project stays selectable.
  const [goneProjects, setGoneProjects] = useState<Map<string, string>>(new Map());
  const [loadErr, setLoadErr] = useState("");
  const [scope, setScope] = useState(session.scope);
  const [filter, setFilter] = useState<Filter>(session.filter);
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState<Selected | null>(session.sel);
  const [open, setOpen] = useState<(MemoryBody & { key: string }) | null>(null);
  const [openErr, setOpenErr] = useState("");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [findings, setFindings] = useState<SecretFinding[]>([]);
  const [formErr, setFormErr] = useState("");
  const [hits, setHits] = useState<MemorySearchHit[]>([]);
  // "any": no memory held every word, so the hits hold only some of them (member-search).
  const [fallback, setFallback] = useState("");
  const [searching, setSearching] = useState(false);
  const [searchErr, setSearchErr] = useState(false);
  const [busy, setBusy] = useState(false);
  // Bumped by this view's own writes: the list, the forgotten list, the body and the history are
  // read again (the parent's `reload` does the same for the status header and the other views).
  const [mine, setMine] = useState(0);
  const [purging, setPurging] = useState<PurgeTarget | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);

  const rootRef = useRef<HTMLDivElement>(null);
  const headRef = useRef<HTMLHeadingElement>(null);
  const focusPane = useRef(false);
  const focusRow = useRef("");
  // A memory just saved: its row is not in the list until the re-read lands.
  const justSaved = useRef("");

  useEffect(() => {
    session.scope = scope;
    session.filter = filter;
    session.sel = sel;
  }, [scope, filter, sel]);

  const load = useCallback(async (signal: AbortSignal) => {
    const list = await api("api/agents/memory/entries/list");
    if (signal.aborted) return true;
    if (isTransientErr(list)) return false;
    setLoadErr(list?.error ? errText(list.error) : "");
    setRows(list?.error ? [] : (list?.entries ?? []));
    setWithheld(list?.error ? 0 : (list?.withheld ?? 0));
    return true;
  }, []);
  useRetryLoad(load, [reload, mine]);
  const allChanges = useAllChanges(reload * 1_000_000 + mine);

  // The forgotten memories of the scope on screen ("all" sends no scope param, which makes the Agent
  // list the user scope and every project). Each memory's newest change is the one that took it
  // away; its revert brings the memory back.
  const goneScopeQ = useCallback(() => {
    const [kind, projectId = ""] = scope.split(":");
    return kind === ALL ? "" : kind === "user" ? "?scope=user" : `?scope=project&project=${encodeURIComponent(projectId)}`;
  }, [scope]);
  const loadGone = useCallback(
    async (signal: AbortSignal) => {
      const [kind] = scope.split(":");
      const res = await api("api/agents/memory/entries/member-forgotten" + goneScopeQ());
      if (signal.aborted) return true;
      if (isTransientErr(res)) return false;
      goneGen.current++;
      setGoneMoreBusy(false);
      setGoneMoreErr(false);
      setGoneNext(res?.error ? "" : (res?.next ?? ""));
      setGoneErr(!!res?.error);
      setForgotten(res?.error ? [] : (res?.forgotten ?? []));
      setGoneLoaded(true);
      if (!res?.error) {
        const named: { id: string; display: string }[] = res?.projects ?? [];
        // An answer for all scopes is the whole truth; one for a single scope only adds to it.
        setGoneProjects((prev) => {
          const next = new Map(kind === ALL ? [] : prev);
          for (const p of named) next.set("project:" + p.id, p.display);
          return next;
        });
      }
      setGoneWithheld(res?.error ? 0 : (res?.withheld ?? 0));
      setGoneIncomplete(!res?.error && !!res?.incomplete);
      return true;
    },
    [scope, goneScopeQ],
  );
  useRetryLoad(loadGone, [reload, mine, scope]);

  // The next page of the forgotten list, appended. A memory forgotten again since the first page is
  // already in the list under its newer row, so a row is replaced by key rather than repeated.
  const loadMoreGone = async () => {
    const gen = goneGen.current;
    const scopeQ = goneScopeQ();
    setGoneMoreBusy(true);
    setGoneMoreErr(false);
    const res = await api(
      "api/agents/memory/entries/member-forgotten" + scopeQ + (scopeQ ? "&" : "?") + "before=" + encodeURIComponent(goneNext),
    ).catch(() => ({ error: true }));
    if (gen !== goneGen.current) return;
    setGoneMoreBusy(false);
    if (res?.error) return setGoneMoreErr(true);
    const more: MemoryChange[] = res?.forgotten ?? [];
    setForgotten((prev) => {
      const again = new Set(more.map(keyOf));
      return [...prev.filter((c) => !again.has(keyOf(c))), ...more];
    });
    const named: { id: string; display: string }[] = res?.projects ?? [];
    if (named.length) {
      setGoneProjects((prev) => {
        const next = new Map(prev);
        for (const p of named) next.set("project:" + p.id, p.display);
        return next;
      });
    }
    setGoneNext(res?.next ?? "");
  };

  // The scopes there is something to browse in. "All scopes" comes first and is the default: a
  // query is expected to cover everything, and the user scope alone is often empty. User scope is
  // always offered: a new memory can start there. A project is offered once it holds a memory
  // (the Console cannot start one).
  const scopes = useMemo(() => {
    const out = new Map<string, string>([
      [ALL, tr("mem.br_scope_all")],
      ["user", tr("mem.af_scope_user")],
    ]);
    for (const m of rows ?? []) {
      if (m.scope === "project" && m.project) out.set(scopeKey(m), m.project.display);
    }
    for (const [k, display] of goneProjects) if (!out.has(k)) out.set(k, display);
    return [...out.entries()];
  }, [rows, goneProjects, tr]);
  // A remembered project scope is judged only once both lists have answered: before that the
  // project is not among the candidates yet.
  useEffect(() => {
    if (rows === null || !goneLoaded) return;
    if (!scopes.some(([k]) => k === scope)) setScope(ALL);
  }, [scopes, scope, rows, goneLoaded]);

  const q = query.trim().toLowerCase();
  // Body search: asked after a pause, for the scope on screen. "All" sends no scope param, which makes the
  // Agent search the user scope and every project. A newer question or a scope change
  // drops the answer of an older one.
  const searchSeq = useRef(0);
  useEffect(() => {
    const seq = ++searchSeq.current;
    setHits([]);
    setFallback("");
    setSearchErr(false);
    if (!q) {
      setSearching(false);
      return;
    }
    setSearching(true);
    const [kind, projectId = ""] = scope.split(":");
    const timer = setTimeout(() => {
      const scopeQ =
        kind === ALL ? "" : kind === "user" ? "scope=user&" : `scope=project&project=${encodeURIComponent(projectId)}&`;
      api(`api/agents/memory/entries/member-search?${scopeQ}limit=50&q=${encodeURIComponent(q)}`)
        .then((res) => {
          if (seq !== searchSeq.current) return;
          if (res?.error) setSearchErr(true);
          else {
            setHits(res?.hits ?? []);
            setFallback(res?.fallback ?? "");
          }
        })
        .catch(() => {
          if (seq === searchSeq.current) setSearchErr(true);
        })
        .finally(() => {
          if (seq === searchSeq.current) setSearching(false);
        });
    }, SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [q, scope]);

  // The rows of the scope that match by a listed field (instant) or by a body hit; a body hit
  // brings its matching lines. Order: listed matches as listed, then body-only hits in rank order.
  const snippets = new Map(hits.map((h) => [keyOf(h), h.snippets ?? []]));
  const inScope = (rows ?? []).filter((m) => scope === ALL || scopeKey(m) === scope);
  const { rows: matched, partial: partialMatch } = matchMemoriesDetail(inScope, hits, q, fallback);
  const shown = filter === "pinned" ? matched.filter((m) => m.pinned) : matched;
  // The answer may be for the scope shown before a switch while the new one is still loading: a row of
  // another scope must never be on screen, since its Unforget writes that scope.
  const goneInScope = forgotten.filter((c) => scope === ALL || scopeKey(c) === scope);
  const whereOf = (m: { scope: string; project?: { display: string } }) =>
    m.scope === "user" ? tr("mem.af_scope_user") : (m.project?.display ?? "");
  const who = (m: { authorKind: string; authorSession: string }) =>
    m.authorKind === "member" ? tr("mem.af_by_member") : `${m.authorKind} · ${m.authorSession}`;
  // Forgotten rows carry no description or body, so the search is over name, scope and author.
  const goneShown = q
    ? goneInScope.filter((c) => hasEveryWord(`${c.name}\n${whereOf(c)}\n${c.authorKind}\n${c.authorSession}`.toLowerCase(), q))
    : goneInScope;

  // The pick must still exist: a scope change, forget, unforget or purge can take it away.
  const key = sel ? selKey(sel) : "";
  const curRow = sel && !sel.gone ? (rows ?? []).find((m) => keyOf(m) === key) : undefined;
  const goneRow = sel?.gone ? goneInScope.find((c) => keyOf(c) === key) : undefined;
  useEffect(() => {
    if (!sel) return;
    const outOfScope = scope !== ALL && scopeKey({ scope: sel.scope, project: { id: sel.project } }) !== scope;
    if (outOfScope) return setSel(null);
    if (sel.gone) {
      if (filter !== "forgotten") return setSel(null);
      if (goneLoaded && !goneErr && !goneRow) setSel(null);
      return;
    }
    if (filter === "forgotten") return setSel(null);
    // The Pinned chip lists pinned memories only: a pick that is no longer one leaves with its row.
    if (curRow && filter === "pinned" && !curRow.pinned) return setSel(null);
    if (curRow) {
      if (justSaved.current === key) justSaved.current = "";
    } else if (rows !== null && justSaved.current !== key) setSel(null);
  }, [sel, scope, filter, rows, curRow, goneRow, goneLoaded, goneErr, key]);

  // The body of the pick, read again after this view's own writes.
  const readable = !!sel && !sel.gone;
  useEffect(() => {
    setOpen(null);
    setOpenErr("");
  }, [key]);
  useEffect(() => {
    if (!sel || !readable) return;
    let live = true;
    api(
      `api/agents/memory/entries/member-read?scope=${sel.scope}&project=${encodeURIComponent(sel.project)}&name=${encodeURIComponent(sel.name)}`,
    )
      .then((res) => {
        if (!live) return;
        if (res?.error) setOpenErr(errDetail(res.error));
        else {
          setOpenErr("");
          setOpen({ ...(curRow ?? {}), ...res, key });
        }
      })
      .catch(() => live && setOpenErr(tr("mem.af_failed")));
    return () => {
      live = false;
    };
    // curRow is deliberately not a dependency: a re-read of the list alone does not re-read the body.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, readable, mine, reload]);
  const body = open && open.key === key ? open : null;

  // Focus follows a pick to the detail heading, and Back to the row it came from.
  const paneKey = draft ? "draft:" + (draft.base?.name ?? "") : key;
  useEffect(() => {
    if (focusPane.current && paneKey) {
      focusPane.current = false;
      headRef.current?.focus();
    }
  }, [paneKey]);
  useEffect(() => {
    if (!paneKey && focusRow.current) {
      const k = focusRow.current;
      focusRow.current = "";
      rootRef.current?.querySelector<HTMLElement>(`[data-row-key="${CSS.escape(k)}"]`)?.focus();
    }
  }, [paneKey]);

  const pick = (m: { scope: string; project?: { id: string }; name: string }, gone: boolean) => {
    focusPane.current = true;
    setDraft(null);
    setMenuOpen(false);
    setSel(selFrom(m, gone));
  };
  // A memory the command palette asked for (openSettings target). Taken once the list has landed:
  // one that is not listed (forgotten since, or withheld by the secret scan) is dropped, leaving
  // the view as it was, and the request never lingers to fire on a later mount.
  const asked = useSettingsUI((st) => st.settingsTarget);
  const clearAsked = useSettingsUI((st) => st.clearSettingsTarget);
  useEffect(() => {
    const want = parseMemoryTarget(asked);
    if (!want || rows === null) return;
    clearAsked();
    const m = rows.find((r) => keyOf(r) === keyOf({ scope: want.scope, project: { id: want.project }, name: want.name }));
    if (!m) return;
    focusPane.current = true;
    setDraft(null);
    setMenuOpen(false);
    setQuery("");
    setScope(ALL);
    setFilter("all");
    setSel(selFrom(m, false));
  }, [asked, rows, clearAsked]);
  const back = () => {
    if (paneKey) focusRow.current = key;
    setDraft(null);
    setMenuOpen(false);
    setSel(null);
  };
  const onPaneKey = (e: ReactKeyboardEvent) => {
    // Escape leaves the dialog otherwise. While a form is open it is left alone: it would drop the text.
    if (e.key !== "Escape" || draft) return;
    e.stopPropagation();
    if (menuOpen) setMenuOpen(false);
    else back();
  };

  // The newest change of a memory, wherever it sits in the history. null: none could be resolved
  // (a toast says why).
  const newest = async (m: MemoryListed): Promise<MemoryChange | null> => {
    try {
      const res = await api(
        `api/agents/memory/entries/member-latest?scope=${m.scope}&project=${encodeURIComponent(m.project?.id ?? "")}&name=${encodeURIComponent(m.name)}`,
      );
      if (res?.error) toast(errDetail(res.error));
      else if (!res?.commit || !res.live || !res.revertible) toast(tr("mem.br_forget_unavailable", { name: m.name }));
      else return res as MemoryChange;
    } catch {
      toast(tr("mem.af_failed"));
    }
    return null;
  };

  const forget = async (m: MemoryListed) => {
    setBusy(true);
    const c = await newest(m);
    setBusy(false);
    if (!c) return;
    const ok = await askConfirm({
      title: tr("mem.af_forget_title"),
      body: <p>{tr("mem.af_forget_body", { name: m.name, where: whereOf(m) })}</p>,
      confirmLabel: tr("mem.af_forget"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // The same revert route as the changes list; the commit also goes in the query for the audit.
      const res = await apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(c.commit), "POST", {
        commit: c.commit,
        forget: true,
        ack: false,
        // The list and the changes are read apart: pin the forget to the revision this row showed.
        expectRevision: m.revision,
      });
      if (res?.error) toast(errDetail(res.error));
      else {
        toast(tr("mem.af_forgotten", { name: m.name }), { kind: "success" });
        if (key === keyOf(m)) back();
        onChanged();
      }
    } catch {
      toast(tr("mem.af_failed"));
    } finally {
      setBusy(false);
      setMine((n) => n + 1);
    }
  };

  const unforget = async (c: MemoryChange) => {
    setBusy(true);
    try {
      const res = await apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(c.commit), "POST", {
        commit: c.commit,
        forget: false,
        ack: false,
      });
      if (res?.error) toast(res.error.code === "memory_secret_detected" ? tr("mem.br_unforget_secret") : errDetail(res.error));
      else {
        toast(tr("mem.br_unforgotten", { name: c.name }), { kind: "success" });
        if (key === keyOf(c)) back();
        onChanged();
      }
    } catch {
      toast(tr("mem.af_failed"));
    } finally {
      setBusy(false);
      setMine((n) => n + 1);
    }
  };

  const togglePin = async (m: MemoryListed) => {
    const pinned = !m.pinned;
    setBusy(true);
    try {
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const res = await apiJSON("api/agents/memory/entries/pin" + projectQuery(m.project?.id), "POST", {
        scope: m.scope,
        project: m.project?.id ?? "",
        name: m.name,
        pinned,
      });
      if (res?.error) {
        toast(errDetail(res.error));
      } else {
        toast(tr(pinned ? "mem.pin_done" : "mem.pin_undone", { name: m.name }), { kind: "success" });
      }
    } catch {
      toast(tr("mem.af_failed"));
    } finally {
      setBusy(false);
      onChanged();
      setMine((n) => n + 1);
    }
  };

  const edit = (base: MemoryBody | null) => {
    setFindings([]);
    setFormErr("");
    focusPane.current = true;
    // A new memory starts in the scope on screen; with every scope shown, in the user scope.
    const [kind, projectId = ""] = scope.split(":");
    if (!base) setSel(null);
    setDraft({
      base,
      scope: base ? base.scope : kind === "project" ? "project" : "user",
      project: base ? (base.project?.id ?? "") : kind === "project" ? projectId : "",
      // Kept as read: a save replaces the kinds, and an edit of the body must not widen who sees it.
      kinds: base?.kinds ?? [],
      name: base?.name ?? "",
      description: base?.description ?? "",
      type: base?.type || "feedback",
      body: base?.body ?? "",
    });
  };

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    setFindings([]);
    setFormErr("");
    try {
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const name = draft.name.trim();
      const res = await apiJSON("api/agents/memory/entries/member-save" + projectQuery(draft.project), "POST", {
        scope: draft.scope,
        project: draft.project,
        name,
        description: draft.description,
        type: draft.type,
        kinds: draft.kinds,
        body: draft.body,
        revision: draft.base?.revision ?? 0,
      });
      if (res?.error?.code === "memory_secret_detected") {
        setFindings(res.findings ?? []);
        setFormErr(tr("mem.br_secret"));
      } else if (res?.error) {
        setFormErr(errDetail(res.error));
      } else {
        toast(tr("mem.br_saved", { name }), { kind: "success" });
        const saved = selFrom({ scope: draft.scope, project: { id: draft.project }, name }, false);
        justSaved.current = selKey(saved);
        focusPane.current = true;
        setDraft(null);
        setFilter("all");
        setSel(saved);
        onChanged();
        setMine((n) => n + 1);
      }
    } catch {
      setFormErr(tr("mem.af_failed"));
    } finally {
      setBusy(false);
    }
  };

  const rowKey = (m: { scope: string; project?: { id: string }; name: string }) => keyOf(m);
  const showDetail = !!sel || !!draft;
  const empty = (
    <li className="muted pad">
      {tr(
        q
          ? searching
            ? "mem.br_searching"
            : "mem.br_no_match"
          : filter === "pinned"
            ? "mem.md_pinned_empty"
            : "mem.br_empty",
      )}
    </li>
  );

  return (
    <div className="mem-browser mem-md" ref={rootRef} data-pane={showDetail ? "detail" : "list"}>
      <h4>{tr("mem.br_title")}</h4>
      <p className="muted ds-hint">{tr("mem.br_intro")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {withheld > 0 && <p className="mem-warn">{tr("mem.br_withheld", { n: withheld })}</p>}
      <div className="mem-md-cols">
        <div className="mem-md-list">
          <div className="mem-br-bar">
            <select aria-label={tr("mem.br_scope")} value={scope} onChange={(e) => setScope(e.target.value)}>
              {scopes.map(([k, label]) => (
                <option key={k} value={k}>
                  {label}
                </option>
              ))}
            </select>
            <input
              type="search"
              aria-label={tr("mem.br_search")}
              placeholder={tr("mem.br_search_ph")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <button type="button" disabled={busy} onClick={() => edit(null)}>
              {tr("mem.br_new")}
            </button>
          </div>
          <div className="mem-md-chips" role="group" aria-label={tr("mem.md_filter")}>
            {FILTERS.map(([f, label]) => (
              <button
                key={f}
                type="button"
                className={"mem-md-chip" + (f === filter ? " active" : "")}
                aria-pressed={f === filter}
                onClick={() => setFilter(f)}
              >
                {tr(label)}
              </button>
            ))}
          </div>
          {searchErr && <p className="mem-warn">{tr("mem.br_search_failed")}</p>}
          {partialMatch && filter !== "forgotten" && <p className="muted ds-hint">{tr("mem.br_search_partial")}</p>}
          {filter !== "forgotten" ? (
            <ul className="mem-pin-list mem-br-list">
              {rows === null ? (
                <li className="muted pad">{tr("common.loading")}</li>
              ) : shown.length === 0 ? (
                empty
              ) : (
                shown.map((m) => (
                  <li key={rowKey(m)} className="mem-br-row mem-md-row">
                    <button
                      type="button"
                      className={"mem-md-pick" + (rowKey(m) === key ? " active" : "")}
                      aria-current={rowKey(m) === key ? "true" : undefined}
                      data-row-key={rowKey(m)}
                      onClick={() => pick(m, false)}
                    >
                      <span className="mem-md-line">
                        <span className="mem-af-name">{m.name}</span>
                        {scope === ALL && <span className="mem-badge mem-br-where">{whereOf(m)}</span>}
                        {m.pinned && <span className="mem-badge mem-md-pinned">{tr("mem.br_pinned")}</span>}
                        {m.type && <span className="mem-badge">{m.type}</span>}
                      </span>
                      <span className="mem-snap-what muted" title={m.description}>
                        {m.description}
                      </span>
                      <span className="mem-snap-what muted">
                        {who(m)}
                        {" · "}
                        {fmtDateTime(m.updated, DATETIME_FULL)}
                        {" · "}
                        {tr("mem.pin_uses", { n: m.uses })}
                      </span>
                      {(snippets.get(rowKey(m)) ?? []).length > 0 && (
                        <span className="mem-snap-what muted mem-br-snip">{(snippets.get(rowKey(m)) ?? []).join(" … ")}</span>
                      )}
                    </button>
                  </li>
                ))
              )}
            </ul>
          ) : (
            <>
              <p className="muted ds-hint">{tr("mem.br_forgotten_note")}</p>
              {goneErr && <p className="mem-warn">{tr("mem.br_forgotten_failed")}</p>}
              {goneWithheld > 0 && <p className="mem-warn">{tr("mem.br_forgotten_withheld", { n: goneWithheld })}</p>}
              {goneIncomplete && <p className="mem-warn">{tr("mem.br_forgotten_incomplete")}</p>}
              <ul className="mem-pin-list mem-br-list">
                {!goneLoaded ? (
                  <li className="muted pad">{tr("common.loading")}</li>
                ) : goneShown.length === 0 ? (
                  !goneErr && <li className="muted pad">{tr(q ? "mem.br_no_match" : "mem.md_forgotten_empty")}</li>
                ) : (
                  goneShown.map((c) => (
                    <li key={c.commit} className="mem-br-row mem-md-row">
                      <button
                        type="button"
                        className={"mem-md-pick" + (rowKey(c) === key ? " active" : "")}
                        aria-current={rowKey(c) === key ? "true" : undefined}
                        data-row-key={rowKey(c)}
                        onClick={() => pick(c, true)}
                      >
                        <span className="mem-md-line">
                          <span className="mem-af-name">{c.name}</span>
                          {scope === ALL && <span className="mem-badge mem-br-where">{whereOf(c)}</span>}
                        </span>
                        <span className="mem-snap-what muted">
                          {fmtDateTime(c.at, DATETIME_FULL)}
                          {" · "}
                          {who(c)}
                        </span>
                      </button>
                      <button type="button" disabled={busy} onClick={() => setPurging({ ...c, where: whereOf(c) })}>
                        {tr("mem.pg_button")}
                      </button>
                      <button type="button" disabled={busy} onClick={() => void unforget(c)}>
                        {tr("mem.br_unforget")}
                      </button>
                    </li>
                  ))
                )}
              </ul>
              {goneMoreErr && <p className="mem-warn">{tr("mem.br_forgotten_more_failed")}</p>}
              {goneNext && (
                <button type="button" className="mem-md-link" disabled={goneMoreBusy} onClick={() => void loadMoreGone()}>
                  {tr("mem.br_forgotten_more")}
                </button>
              )}
            </>
          )}
        </div>
        <div className="mem-md-detail" onKeyDown={onPaneKey}>
          <button type="button" className="mem-md-back" onClick={back}>
            {tr("mem.md_back")}
          </button>
          {draft ? (
            <form
              className="mem-br-form"
              onSubmit={(e) => {
                e.preventDefault();
                void save();
              }}
            >
              <h5 ref={headRef} tabIndex={-1}>
                {draft.base ? tr("mem.br_edit_title", { name: draft.base.name }) : tr("mem.br_new_title")}
              </h5>
              <span className="muted">
                {draft.scope === "user"
                  ? tr("mem.af_scope_user")
                  : (scopes.find(([k]) => k === "project:" + draft.project)?.[1] ?? draft.project)}
              </span>
              {!draft.base && (
                <label>
                  {tr("mem.br_f_name")}
                  <input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
                </label>
              )}
              <label>
                {tr("mem.br_f_description")}
                <input value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
              </label>
              <label>
                {tr("mem.br_f_type")}
                <select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })}>
                  {TYPES.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                {tr("mem.br_f_body")}
                <textarea rows={8} value={draft.body} onChange={(e) => setDraft({ ...draft, body: e.target.value })} />
              </label>
              {formErr && <p className="mem-warn">{formErr}</p>}
              {findings.length > 0 && (
                <ul className="mem-findings">
                  {findings.map((f, i) => (
                    <li key={i}>
                      <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                    </li>
                  ))}
                </ul>
              )}
              <div className="mem-br-actions">
                <button type="submit" disabled={busy}>
                  {tr("mem.br_save")}
                </button>
                <button type="button" disabled={busy} onClick={() => setDraft(null)}>
                  {tr("mem.br_cancel")}
                </button>
              </div>
            </form>
          ) : sel && sel.gone ? (
            <div className="mem-br-detail">
              <h5 ref={headRef} tabIndex={-1} className="mem-af-name">
                {sel.name}
              </h5>
              {goneRow && (
                <>
                  <dl className="mem-md-meta">
                    <dt>{tr("mem.br_scope")}</dt>
                    <dd>{whereOf(goneRow)}</dd>
                    <dt>{tr("mem.md_m_forgotten")}</dt>
                    <dd>{fmtDateTime(goneRow.at, DATETIME_FULL)}</dd>
                    <dt>{tr("mem.md_m_author")}</dt>
                    <dd>{who(goneRow)}</dd>
                  </dl>
                  <div className="mem-br-actions">
                    <button type="button" disabled={busy} onClick={() => void unforget(goneRow)}>
                      {tr("mem.br_unforget")}
                    </button>
                    <button type="button" disabled={busy} onClick={() => setPurging({ ...goneRow, where: whereOf(goneRow) })}>
                      {tr("mem.pg_button")}
                    </button>
                  </div>
                </>
              )}
              <MemoryHistory
                key={key}
                target={{ scope: sel.scope, project: sel.project, name: sel.name }}
                data={allChanges}
                onOpenChanges={onOpenChanges}
              />
            </div>
          ) : sel ? (
            <div className="mem-br-detail">
              <div className="mem-md-head">
                <h5 ref={headRef} tabIndex={-1} className="mem-af-name">
                  {sel.name}
                </h5>
                {curRow?.pinned && <span className="mem-badge mem-md-pinned">{tr("mem.br_pinned")}</span>}
              </div>
              {openErr && <p className="mem-warn">{openErr}</p>}
              {curRow && (
                <>
                  <div className="mem-br-actions">
                    <button type="button" disabled={busy || !body} onClick={() => body && edit(body)}>
                      {tr("mem.br_edit")}
                    </button>
                    <button
                      type="button"
                      aria-pressed={curRow.pinned}
                      className={curRow.pinned ? "active" : undefined}
                      disabled={busy}
                      onClick={() => void togglePin(curRow)}
                    >
                      {tr(curRow.pinned ? "mem.pin_unpin" : "mem.pin_pin")}
                    </button>
                    <button type="button" disabled={busy} onClick={() => void forget(curRow)}>
                      {tr("mem.af_forget")}
                    </button>
                    <OverflowMenu
                      label={tr("mem.md_more")}
                      open={menuOpen}
                      onOpen={setMenuOpen}
                      items={[
                        {
                          label: tr("mem.pg_button"),
                          disabled: busy,
                          onSelect: () => setPurging({ ...curRow, where: whereOf(curRow) }),
                        },
                      ]}
                    />
                  </div>
                  <dl className="mem-md-meta">
                    <dt>{tr("mem.br_scope")}</dt>
                    <dd>{whereOf(curRow)}</dd>
                    {curRow.type && (
                      <>
                        <dt>{tr("mem.br_f_type")}</dt>
                        <dd>{curRow.type}</dd>
                      </>
                    )}
                    <dt>{tr("mem.md_m_author")}</dt>
                    <dd>{who(curRow)}</dd>
                    <dt>{tr("mem.md_m_updated")}</dt>
                    <dd>{fmtDateTime(curRow.updated, DATETIME_FULL)}</dd>
                    <dt>{tr("mem.md_m_uses")}</dt>
                    <dd>{curRow.uses}</dd>
                  </dl>
                  <p className="muted">{curRow.description}</p>
                  <details className="mem-md-pinhelp">
                    <summary>{tr("mem.md_pin_help")}</summary>
                    <p className="muted ds-hint">{tr("mem.pin_intro")}</p>
                  </details>
                </>
              )}
              {body && <pre className="mem-br-body">{body.body}</pre>}
              <MemoryHistory
                key={key}
                target={{ scope: sel.scope, project: sel.project, name: sel.name }}
                data={allChanges}
                onOpenChanges={onOpenChanges}
              />
            </div>
          ) : (
            <p className="muted pad mem-md-placeholder">{tr("mem.md_pick")}</p>
          )}
        </div>
      </div>
      {purging && (
        <PurgeHistoryDialog
          target={purging}
          onClose={() => setPurging(null)}
          onDone={() => {
            onChanged();
            setMine((n) => n + 1);
          }}
        />
      )}
    </div>
  );
}

interface MenuItem {
  label: string;
  disabled?: boolean;
  onSelect: () => void;
}

// A small overflow menu: a button that opens a list of actions. Escape (handled by the pane, which
// closes the menu first) or a click outside closes it; picking an item closes it too.
function OverflowMenu({
  label,
  open,
  onOpen,
  items,
}: {
  label: string;
  open: boolean;
  onOpen: (v: boolean) => void;
  items: MenuItem[];
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    ref.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    return () => document.removeEventListener("mousedown", onDown);
  }, [open, onOpen]);
  return (
    <div className="mem-md-menu" ref={ref}>
      <button type="button" aria-haspopup="menu" aria-expanded={open} aria-label={label} title={label} onClick={() => onOpen(!open)}>
        …
      </button>
      {open && (
        <div role="menu" className="mem-md-menu-list">
          {items.map((it) => (
            <button
              key={it.label}
              type="button"
              role="menuitem"
              disabled={it.disabled}
              onClick={() => {
                onOpen(false);
                it.onSelect();
              }}
            >
              {it.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
