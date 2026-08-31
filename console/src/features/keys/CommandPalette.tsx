// Command palette — a VS Code-style multi-mode quick-open. Opened by Ctrl/⌘+P (dispatcher).
// Joins the Esc + browser-back overlay stacks like every other dialog, so while it is open
// hasOpenOverlay() is true and the dispatcher stays inert (the input owns the keyboard).
//
// Modes (cycle with Tab / Shift+Tab, re-pressing Ctrl/⌘+P, or the mode tabs):
//   - command : every available command + the session list (fuzzy, all-locale search).
//   - changed : working-tree changed files across all dirty repos → open each file's diff.
//   - file    : recursive filename search under ~/repos (server-side via /fs/search = ripgrep
//               --files, .gitignore-honouring) → open the file. Unlike command/changed (a
//               static list client-fuzzed), this queries the backend per keystroke.
//   - session : the files the ACTIVE session's agent edited (docs/log/68), joined with the
//               working tree the same way the mirror's 変更ファイル strip joins them.
//               A different axis from `changed`, which is per working copy and cannot
//               tell two sessions in the same worktree apart. Appended last on purpose:
//               inserting it earlier would renumber every existing mode's Tab position.
//
// Search matches across ALL locales (cmdSearch) so typing English or Japanese finds a
// command regardless of the current UI language; display uses the current locale.
//
// Focus: opening from a composer/input must not strand focus. We remember the opener and,
// on a CANCEL (Esc / browser-back / backdrop), return focus to it. Running a command/opening
// a file does NOT restore — it may move focus deliberately (e.g. focus a pane).
import { useEffect, useMemo, useRef, useState } from "react";
import { Kbd } from "../../ui/Kbd.tsx";
import { paletteCommands } from "../../lib/keys/registry.ts";
import type { Command } from "../../lib/keys/registry.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { t, useLocale, type MsgKey } from "../../lib/i18n/index.ts";
import { coarsePointer } from "../../lib/device.ts";
import { api, fsSearch } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useKeysStore } from "./store.ts";
import { useEffectiveCommands, boundChord, APP_LEADER, APP_PALETTE } from "./bindings.ts";
import { eventChordString } from "../../lib/keys/chords.ts";
import { cmdLabel, cmdSearch } from "./labels.ts";
import { buildContext } from "./dispatcher.ts";
import { useSessionsStore } from "../sessions/store.ts";
import {
  openSessionChat,
  openSessionChatSplit,
  openSessionTerminal,
  openSessionTerminalSplit,
} from "../sessions/open.ts";
import { useReposStore } from "../repos/store.ts";
import { revealRepoInRail } from "../repos/reveal.ts";
import { openFileDiff } from "../scm/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { activePane } from "../../layout/ops.ts";
import {
  joinChanges,
  openRow,
  sortRows,
  useSessionFilesStore,
  type FsChange,
  type SessionFile,
} from "../mirror/sessionFiles.ts";

type Mode = "command" | "changed" | "file" | "session";
const MODES: Mode[] = ["command", "changed", "file", "session"];
const MODE_LABEL: Record<Mode, MsgKey> = {
  command: "keys.palette.mode_command",
  changed: "keys.palette.mode_changed",
  session: "keys.palette.mode_session",
  file: "keys.palette.mode_file",
};
// File search is rooted at ~/repos: the working-copy scope, so results are code files (the
// backend excludes caches/packages), shown repo-relative like the changed-files mode.
const FILE_ROOT = "repos";

interface Item {
  id: string;
  title: string;
  sub: string;
  /** All-locale text the fuzzy filter matches against (kept apart from the display title). */
  search: string;
  /** The effective shortcut as a chord sequence, rendered as keycaps on the right. A
   * direct accelerator is one chord (["alt+1"]); a leader command is the leader plus each
   * step (["mod+k","p","r"]). Empty when the command has no key (or a session/file row). */
  keys: string[];
  /** Activate the row. `split` (Ctrl/⌘+Enter) opens file/diff rows in a NEW pane; the
   * plain Enter opens in the active pane. Command/session rows ignore split. */
  run: (split: boolean) => void;
}

// One working-tree change from api/repos/{repo}/changes.
interface Change {
  path: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

// The shortcut a palette row advertises: the command's direct accelerator if it has one
// (already reflecting the user's rebind — applyOverrides rewrote `keys`), otherwise the
// leader chord followed by its sequence keys. Mirrors the cheat-sheet's rendering so the
// two never disagree. `leader` is the live-bound leader ("" only if the user cleared it) —
// an unbound leader makes every leader sequence unreachable, so advertise no shortcut
// rather than an unusable one.
function shortcutChords(c: Command, leader: string): string[] {
  if (c.keys && c.keys.length) return [c.keys[0]];
  if (c.seq) return leader ? [leader, ...c.seq.split(" ")] : [];
  return [];
}

// Subsequence match: every character of the query appears in order in the text.
function fuzzy(query: string, text: string): boolean {
  if (!query) return true;
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  let i = 0;
  for (let j = 0; j < t.length && i < q.length; j++) if (t[j] === q[i]) i++;
  return i === q.length;
}

// Load changed files across every dirty working copy. Refreshes the repo list first so the
// dirty flags (hence which repos we hit for changes) are current, then fetches each dirty
// repo's changes in parallel. A file's row opens its working diff (same target as ChangesView).
async function loadChangedItems(): Promise<Item[]> {
  await useReposStore.getState().refresh();
  const repos = useReposStore.getState().repos.filter((r) => r.dirty);
  const lists = await Promise.all(
    repos.map(async (r) => {
      try {
        const d = await api(`api/repos/${encodeURIComponent(r.name)}/changes`);
        const changes: Change[] = d.changes || [];
        return changes.map((c): Item => {
          const staged = !c.untracked && c.index !== " ";
          return {
            id: "chg:" + r.name + ":" + c.path,
            title: c.path,
            sub: r.name,
            search: c.path + " " + r.name,
            keys: [],
            run: (split) =>
              split
                ? useLayoutStore
                    .getState()
                    .openTargetInNew({ content: { kind: "wtdiff", scmRepo: r.name, filePath: c.path, diffStaged: staged } })
                : openFileDiff(r.name, c.path, staged),
          };
        });
      } catch {
        return [] as Item[];
      }
    }),
  );
  return lists.flat();
}

// Load the ACTIVE session's edited files (docs/log/68). The list itself normally rides the
// mirror's transcript poll, so it is read from the store first; a session whose mirror was
// never open is fetched once here, with the smallest window the API accepts — the `files`
// aggregate is whole-transcript regardless of how many turns come back with it.
async function loadSessionItems(session: string): Promise<Item[]> {
  let files = useSessionFilesStore.getState().bySession[session];
  const [fetched, changesRes, committedRes] = await Promise.all([
    files
      ? Promise.resolve(null)
      : api(`api/sessions/${encodeURIComponent(session)}/messages?since=0&tail=1&limit=50`).catch(() => null),
    api("api/fs/changes").catch(() => null),
    api(`api/sessions/${encodeURIComponent(session)}/committed`).catch(() => null),
  ]);
  if (!files) {
    files = Array.isArray(fetched?.files) ? (fetched.files as SessionFile[]) : [];
    useSessionFilesStore.getState().set(session, files);
  }
  const changes: FsChange[] = Array.isArray(changesRes?.changes) ? changesRes.changes : [];
  const committed: string[] = Array.isArray(committedRes?.committed) ? committedRes.committed : [];
  return sortRows(joinChanges(files, changes, committed), "recent").map((row) => ({
    id: "sf:" + row.path,
    title: row.rel || row.path,
    sub: row.repo || "",
    search: row.path + " " + (row.repo || ""),
    keys: [],
    run: (split: boolean) => openRow(row, split),
  }));
}

// One /fs/search hit (a home-relative path like "repos/<repo>/<...>") → a palette row. Split
// off the repo segment for the badge, show the in-repo path as the title, open it as a file.
function fileItem(homeRel: string): Item {
  const rel = homeRel.startsWith(FILE_ROOT + "/") ? homeRel.slice(FILE_ROOT.length + 1) : homeRel;
  const slash = rel.indexOf("/");
  const repo = slash > 0 ? rel.slice(0, slash) : "";
  const inRepo = slash > 0 ? rel.slice(slash + 1) : rel;
  return {
    id: "file:" + homeRel,
    title: inRepo,
    sub: repo || rel,
    search: rel,
    keys: [],
    run: (split) => {
      const st = useLayoutStore.getState();
      const content = { kind: "file", filePath: homeRel } as const;
      if (split) st.openTargetInNew({ content });
      else st.openTarget({ content });
    },
  };
}

export function CommandPalette() {
  const open = useKeysStore((s) => s.paletteOpen);
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  // Subscribed (not a getState peek): which session is active decides whether the session
  // mode exists at all, and the palette must not open with a stale answer.
  const layout = useLayoutStore((s) => s.layout);
  const commands = useEffectiveCommands();
  const locale = useLocale(); // re-render + recompute items when the UI language changes
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [mode, setMode] = useState<Mode>("command");
  const [changed, setChanged] = useState<Item[] | null>(null); // null = loading
  const [sessionFiles, setSessionFiles] = useState<Item[] | null>(null); // null = loading
  const [fileHits, setFileHits] = useState<Item[] | null>(null); // null = searching (file mode)
  // The session mode needs a session to be about. With none in the active pane the mode is
  // not offered at all rather than offered empty — an empty mode reads as "this session
  // changed nothing", which is a different claim from "there is no session here".
  const activeSessionName = activePane(layout)?.session ?? null;
  const activeKind = sessions.find((x) => x.name === activeSessionName)?.kind;
  const modes = MODES.filter(
    (m) => m !== "session" || (!!activeSessionName && !!activeKind && agentOf(activeKind).caps.transcript),
  );

  const inputRef = useRef<HTMLInputElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const selRef = useRef<HTMLDivElement | null>(null);

  const close = () => useKeysStore.getState().closePalette();
  // Cancel = close without running anything → hand focus back to whoever opened us.
  const cancel = () => {
    close();
    const o = openerRef.current;
    openerRef.current = null;
    if (o && document.contains(o) && !coarsePointer()) o.focus?.();
  };
  useEscLayer(open ? cancel : undefined, open);
  useBackClose(open ? cancel : undefined, open);

  useEffect(() => {
    if (!open) return;
    // Capture the opener BEFORE we steal focus to the search input (the rAF below).
    openerRef.current = (document.activeElement as HTMLElement) ?? null;
    setQ("");
    setSel(0);
    setMode("command"); // always reopen in command mode
    void useReposStore.getState().refresh(); // repos+worktrees are searchable in command mode
    const id = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, [open]);

  // Fetch changed files whenever the changed mode is (re)entered while open. Re-shows the
  // loading state each time so a stale list never flashes before the fresh fetch lands.
  useEffect(() => {
    if (!open || mode !== "changed") return;
    let cancelled = false;
    setChanged(null);
    void loadChangedItems().then((items) => {
      if (!cancelled) setChanged(items);
    });
    return () => {
      cancelled = true;
    };
  }, [open, mode]);

  // Same shape as the changed-files fetch: re-entering the mode always reloads, so a stale
  // list from a previous session can never flash before the fresh one lands.
  useEffect(() => {
    if (!open || mode !== "session" || !activeSessionName) return;
    let cancelled = false;
    setSessionFiles(null);
    void loadSessionItems(activeSessionName).then((items) => {
      if (!cancelled) setSessionFiles(items);
    });
    return () => {
      cancelled = true;
    };
  }, [open, mode, activeSessionName]);

  // File mode searches the backend per keystroke (debounced). An empty query yields nothing
  // (there's no whole-tree listing to fuzz locally — the point of /fs/search is to not).
  useEffect(() => {
    if (!open || mode !== "file") return;
    const query = q.trim();
    if (!query) {
      setFileHits([]);
      return;
    }
    let alive = true;
    setFileHits(null); // searching
    const timer = setTimeout(() => {
      fsSearch(FILE_ROOT, query)
        .then((r) => alive && setFileHits(r.results.map(fileItem)))
        .catch(() => alive && setFileHits([]));
    }, 160);
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [open, mode, q]);

  const commandItems = useMemo<Item[]>(() => {
    if (!open) return [];
    void locale; // dep: recompute labels/search when the language changes
    const ctx = buildContext();
    const leader = boundChord(APP_LEADER); // "" = user unbound the leader (no fallback — the chord truly doesn't fire)
    const cmds: Item[] = paletteCommands(commands, ctx).map((c) => ({
      id: c.id,
      title: cmdLabel(c.title),
      sub: t("keys.item.command"),
      search: cmdSearch(c.title),
      keys: shortcutChords(c, leader),
      run: () => c.run(ctx), // commands ignore split
    }));
    const sess: Item[] = sessions.map((s) => {
      const title = s.title || s.name;
      const chat = agentOf(s.kind).caps.chat;
      return {
        id: "session:" + s.name,
        title,
        sub: t("keys.item.session"),
        search: title + " " + s.name,
        keys: [],
        // Enter → open in the active pane; Ctrl/⌘+Enter → open in a new (split) pane.
        run: (split) =>
          (chat
            ? split ? openSessionChatSplit : openSessionChat
            : split ? openSessionTerminalSplit : openSessionTerminal)(s.name),
      };
    });
    // Repos + worktrees: Enter → reveal + focus the working copy in the rail;
    // Ctrl/⌘+Enter → open its Source Control in a NEW pane (the split convention every
    // other row follows). A worktree row is tagged so a search for "wt"/"worktree"
    // surfaces it.
    const repoItems: Item[] = repos.map((r) => ({
      id: "repo:" + r.name,
      title: r.name,
      sub:
        t(r.worktree ? "keys.item.worktree" : "keys.item.repo") +
        (r.branch ? " · " + r.branch : ""),
      search: r.name + " " + (r.branch || "") + " " + (r.worktree ? "worktree wt" : "repo"),
      keys: [],
      run: (split) =>
        split
          ? useLayoutStore.getState().openTargetInNew({ content: { kind: "scm", scmRepo: r.name } })
          : revealRepoInRail(r.name),
    }));
    return [...cmds, ...sess, ...repoItems];
  }, [open, sessions, repos, commands, locale]);

  // command/changed are static lists filtered client-side; file is already server-filtered by q.
  // File "loading" only counts with a live query — an empty query shows the type-to-search hint,
  // never a spurious spinner from the initial null state.
  const loading =
    (mode === "changed" && changed === null) ||
    (mode === "session" && sessionFiles === null) ||
    (mode === "file" && fileHits === null && !!q.trim());
  const items =
    mode === "command"
      ? commandItems
      : mode === "changed"
        ? (changed ?? [])
        : mode === "session"
          ? (sessionFiles ?? [])
          : (fileHits ?? []);
  const filtered = useMemo(
    () => (mode === "file" ? items : items.filter((it) => fuzzy(q, it.search + " " + it.sub))),
    [items, q, mode],
  );

  // Keep the highlighted row visible: arrow-key navigation moves `sel` but the list is a
  // fixed-height scroller, so a selection past the fold would otherwise vanish. `nearest`
  // scrolls only when the row is out of view (no jump while it's already visible).
  useEffect(() => {
    selRef.current?.scrollIntoView({ block: "nearest" });
  }, [sel, mode, filtered.length]);

  const switchMode = (m: Mode) => {
    setMode(m);
    setSel(0);
    inputRef.current?.focus();
  };
  const cycleMode = (dir: number) => {
    const i = modes.indexOf(mode);
    switchMode(modes[(i + dir + modes.length) % modes.length]);
  };

  if (!open) return null;

  // split (Ctrl/⌘+Enter or Ctrl/⌘+click) opens a file/diff row in a new pane; plain opens
  // in the active pane. The opened viewer takes focus via Pane's own scroller auto-focus.
  const run = (it?: Item, split = false) => {
    if (!it) return;
    openerRef.current = null; // running/opening owns focus; don't restore to the opener
    close();
    it.run(split);
  };

  return (
    <div className="cp-overlay" onMouseDown={cancel}>
      <div
        className="cp-panel"
        role="dialog"
        aria-modal="true"
        aria-label={t("keys.app.palette")}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="cp-input"
          value={q}
          placeholder={t(
            mode === "changed"
              ? "keys.palette.placeholder_changed"
              : mode === "session"
                ? "keys.palette.placeholder_session"
                : mode === "file"
                  ? "keys.palette.placeholder_file"
                  : "keys.palette.placeholder",
          )}
          aria-label={t("keys.palette.aria")}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return;
            // Mode cycling: Tab / Shift+Tab, or re-pressing the palette chord (Ctrl/⌘+P by
            // default — boundChord so a rebind keeps working; preventDefault also stops the
            // browser's print dialog on Ctrl+P).
            const palChord = boundChord(APP_PALETTE);
            if (e.key === "Tab") {
              e.preventDefault();
              cycleMode(e.shiftKey ? -1 : 1);
            } else if (palChord && eventChordString(e.nativeEvent) === palChord) {
              e.preventDefault();
              cycleMode(1);
            } else if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => Math.min(s + 1, filtered.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => Math.max(s - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              run(filtered[sel], e.ctrlKey || e.metaKey); // Ctrl/⌘+Enter → new pane
            }
          }}
        />
        <div className="cp-modes" role="tablist">
          {modes.map((m) => (
            <button
              key={m}
              type="button"
              role="tab"
              aria-selected={m === mode}
              className={"cp-mode" + (m === mode ? " on" : "")}
              onMouseDown={(e) => {
                e.preventDefault(); // keep focus in the search input
                switchMode(m);
              }}
            >
              {t(MODE_LABEL[m])}
            </button>
          ))}
          <span className="cp-mode-hint">{t("keys.palette.mode_hint")}</span>
        </div>
        <div className="cp-list">
          {mode === "file" && !q.trim() ? (
            <div className="cp-empty">{t("keys.palette.file_hint")}</div>
          ) : loading ? (
            <div className="cp-empty">{t(mode === "file" ? "keys.palette.file_searching" : "keys.palette.changed_loading")}</div>
          ) : filtered.length === 0 ? (
            <div className="cp-empty">
              {mode === "changed"
                ? t("keys.palette.changed_empty")
                : mode === "session"
                  ? t("keys.palette.session_empty")
                  : t("keys.palette.empty")}
            </div>
          ) : (
            filtered.map((it, i) => (
              <div
                key={it.id}
                ref={i === sel ? selRef : null}
                className={"cp-item" + (i === sel ? " sel" : "")}
                onMouseMove={() => setSel(i)}
                onMouseDown={(e) => {
                  e.preventDefault();
                  run(it, e.ctrlKey || e.metaKey); // Ctrl/⌘+click → new pane
                }}
              >
                <span className="cp-title">{it.title}</span>
                <span className="cp-sub">{it.sub}</span>
                {it.keys.length > 0 && (
                  <span className="cp-kbd">
                    {it.keys.map((ch, k) => (
                      <Kbd key={k} chord={ch} />
                    ))}
                  </span>
                )}
              </div>
            ))
          )}
        </div>
        {mode !== "command" && (
          <div className="cp-foot">
            <span>
              <Kbd chord="enter" /> {t("keys.palette.open_here")}
            </span>
            <span>
              <Kbd chord="mod+enter" /> {t("keys.palette.open_split")}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
