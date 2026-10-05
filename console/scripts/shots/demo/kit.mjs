// Shared pieces of the demo scenarios (console/scripts/shots/demo/*.mjs).
//
// A scenario module exports two halves that run in different processes:
//   fixtures(locale, fx) → { route, setPhase }   loaded by `server.mjs --demo <name>`
//   meta, seed(locale), script(ctx)               loaded by demo.mjs, which drives the page
// so nothing here may touch the browser or the network.

export const L = (locale, ja, en) => (locale === "ja" ? ja : en);

export const ago = (min) => new Date(Date.now() - min * 60_000).toISOString();

// Transcript builders in the wire shape of workspace/agent/internal/transcript/transcript.go.
export const user = (idx, text, ts, extra = {}) => ({ role: "user", idx, ts, text, parts: [{ kind: "text", text }], ...extra });
export const assistant = (idx, ts, parts, model) => ({ role: "assistant", idx, ts, model, text: "", parts });
export const txt = (text) => ({ kind: "text", text });
export const tool = (name, info, output) => ({ kind: "tool", tool: name, info, output });

// A /api/repos row (console/src/features/repos/store.ts). A worktree names its parent and shares
// its remote, which is what files it under the parent's group in the overview and the rail.
export const repoRow = (name, branch, over = {}) => ({
  name,
  path: `/home/dev/repos/${name}`,
  branch,
  provider: "github",
  remote: "github.com",
  remotePath: `demo/${name.split("@")[0]}`,
  ...(name.includes("@") ? { worktree: true, parent: name.split("@")[0], createdAt: ago(60) } : {}),
  ...over,
});

// The body GET /api/sessions/<name>/messages answers (console/src/features/mirror/MirrorView.tsx).
export const messagesBody = (name, turns, status, extra = {}) => ({
  name,
  messages: turns,
  cursor: turns.length * 4,
  status,
  alive: true,
  reset: true,
  firstLine: 0,
  hasMore: false,
  jsonlLines: turns.length * 4,
  jsonlMtime: new Date().toISOString(),
  ...extra,
});

// What every scenario answers the same way.
const COMMON_EXACT = {
  // The engine pills belong to another story; an empty answer keeps the top bar to the demo.
  "/api/engines/status": () => ({ engines: [] }),
};
const COMMON_RE = [
  // The launch dialog's live model list (console/src/lib/agentModels.ts). Unanswered, codex's
  // picker sits on "Loading models…" through a whole launch.
  [
    /^\/api\/agents\/([^/]+)\/models$/,
    (m) =>
      m[1] === "codex"
        ? { models: [{ id: "gpt-5.6-luna", label: "gpt-5.6-luna", efforts: ["low", "medium", "high"], defaultEffort: "medium" }] }
        : null,
  ],
];

// Routes a scenario owns: `exact` by path, `re` by pattern. A handler returning null or undefined
// falls through (to the common routes, then fixtures.mjs), so a scenario only answers what its
// state changes.
export function router(ownExact, ownRe) {
  const exact = { ...COMMON_EXACT, ...ownExact };
  const re = [...ownRe, ...COMMON_RE];
  return (pathname, query, method, body) => {
    if (exact[pathname]) {
      const v = exact[pathname](query, method, body);
      if (v != null) return v;
    }
    for (const [rx, fn] of re) {
      const m = rx.exec(pathname);
      if (m) {
        const v = fn(m, query, method, body);
        if (v != null) return v;
      }
    }
    return undefined;
  };
}

// POST /__demo/phase?phase=<p>: the recorder moves the story on between scenes.
export function phases(names, state) {
  return (p) => {
    if (!names.includes(p)) return { error: `unknown phase ${p}` };
    state.phase = p;
    return { phase: p };
  };
}
