// Phase 1 MVP Console: workspace controls, session list, and an xterm.js
// terminal bridged to /ws/terminal (which the Control Plane proxies to the
// Workspace Agent's PTY).

const $ = (id) => document.getElementById(id);

// Resolve URLs relative to where the Console is mounted, so it works both at
// the host root (http://localhost:8099/) and behind a path-stripping proxy
// (e.g. Tailscale Funnel + Caddy at /agent-fleet/). Use the trailing-slash URL.
const rel = (p) => new URL(p, document.baseURI).toString();
const api = (path, opts) => fetch(rel(path), opts).then((r) => r.json());

// --- tenant selection (P3-2) ---
// The active tenant is sent on every request as X-AF-Tenant so the Control Plane
// resolves the right per-membership workspace. Single-membership users never see
// a picker. We inject the header globally so all existing fetch() calls carry it.
let selectedTenant = localStorage.getItem("af-tenant") || "";
const _fetch = window.fetch.bind(window);
window.fetch = (input, init = {}) => {
  if (selectedTenant) {
    const h = new Headers(init.headers || {});
    if (!h.has("X-AF-Tenant")) h.set("X-AF-Tenant", selectedTenant);
    init = { ...init, headers: h };
  }
  return _fetch(input, init);
};

async function initTenants() {
  let data;
  try {
    data = await api("api/tenants");
  } catch {
    return; // dev/single-tenant or CP without the endpoint: carry on
  }
  const tenants = data.tenants || [];
  const wrap = $("tenant-wrap");
  const sel = $("tenant-picker");
  if (tenants.length <= 1) {
    selectedTenant = tenants[0] ? tenants[0].slug : "";
    localStorage.setItem("af-tenant", selectedTenant);
    if (wrap) wrap.style.display = "none";
    return;
  }
  sel.innerHTML = "";
  for (const t of tenants) {
    const o = document.createElement("option");
    o.value = t.slug;
    o.textContent = `${t.name} (${t.role})`;
    sel.appendChild(o);
  }
  if (!tenants.some((t) => t.slug === selectedTenant)) selectedTenant = tenants[0].slug;
  sel.value = selectedTenant;
  localStorage.setItem("af-tenant", selectedTenant);
  if (wrap) wrap.style.display = "";
  sel.onchange = () => {
    selectedTenant = sel.value;
    localStorage.setItem("af-tenant", selectedTenant);
    reloadAll();
  };
}

// reloadAll re-syncs every panel for the current tenant (called on tenant switch).
function reloadAll() {
  if (typeof ws !== "undefined" && ws) {
    try { ws.close(); } catch {}
  }
  $("term-title").textContent = "no session attached";
  refreshWorkspace();
  refreshConnections();
  refreshRepos();
  refreshSessions();
}

// --- workspace lifecycle ---
async function refreshWorkspace() {
  try {
    const ws = await api("api/workspace");
    $("ws-state").textContent = ws.state;
  } catch {
    $("ws-state").textContent = "unknown";
  }
}
$("ws-start").onclick = async () => {
  $("ws-state").textContent = "starting…";
  await api("api/workspace/start", { method: "POST" });
  await refreshWorkspace();
  await refreshSessions();
};
$("ws-stop").onclick = async () => {
  await api("api/workspace/stop", { method: "POST" });
  await refreshWorkspace();
  await refreshSessions();
};

// --- sessions ---
async function refreshSessions() {
  const list = $("session-list");
  list.innerHTML = "";
  let data;
  try {
    data = await api("api/sessions");
  } catch {
    return;
  }
  for (const s of data.sessions || []) {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = s.name;
    name.title = s.dir || "";
    name.onclick = () => attach(s.name);
    const dir = document.createElement("span");
    dir.className = "dir";
    dir.textContent = s.dir || "";
    const stop = document.createElement("button");
    stop.textContent = "✕";
    stop.title = "stop";
    stop.onclick = async () => {
      await fetch(rel(`api/sessions/${encodeURIComponent(s.name)}/stop`), { method: "POST" });
      refreshSessions();
    };
    li.append(name, dir, stop);
    list.append(li);
  }
}
$("sessions-refresh").onclick = refreshSessions;
$("new-session").onsubmit = async (e) => {
  e.preventDefault();
  const name = $("ns-name").value.trim();
  const dir = $("ns-dir").value.trim();
  // Optional clone-then-start: when a URL is given the Agent clones (or reuses)
  // the repo under ~/repos and uses it as the session CWD, ignoring dir.
  const remote_url = $("ns-url").value.trim();
  const branch = $("ns-branch").value.trim();
  const kind = $("ns-kind") ? $("ns-kind").value : "claude";
  const btn = e.target.querySelector("button");
  btn.disabled = true;
  btn.textContent = remote_url ? "Cloning…" : "…";
  try {
    const res = await api("api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, dir, remote_url, branch, kind }),
    });
    if (res && res.error) {
      alert("create failed: " + (res.error.message || res.error));
      return;
    }
    ["ns-name", "ns-dir", "ns-url", "ns-branch"].forEach((id) => ($(id).value = ""));
    await refreshSessions();
    if (remote_url) await refreshRepos(); // a new working copy may have appeared
    attach(name);
  } finally {
    btn.disabled = false;
    btn.textContent = "New";
  }
};

// --- connections ---
// Per-provider credentials the Workspace consumes (git tokens; Claude in Stage 3).
// The auth flow happens here in the WebUI; the token is stored in the container
// home and used by git/claude inside — no terminal CLI auth.
async function refreshConnections() {
  const list = $("conn-list");
  let data;
  try {
    data = await api("api/connections");
  } catch {
    list.innerHTML = "";
    return;
  }
  list.innerHTML = "";
  list.append(claudeConnRow(data.claude));
  list.append(gitConnRow("GitHub", "github.com", data.github, [{ key: "token", ph: "Personal Access Token", pw: true }], githubOAuthFlow));
  list.append(gitConnRow("Bitbucket", "bitbucket.org", data.bitbucket, [
    { key: "username", ph: "Atlassian email" },
    { key: "token", ph: "API token", pw: true },
  ], bitbucketOAuthFlow));
}

// Claude uses a multi-step flow: start (get authorize URL) → user approves in
// browser → paste code → complete. Driven here so no terminal interaction.
function claudeConnRow(st) {
  const li = document.createElement("li");
  li.className = "conn";
  const dot = document.createElement("span");
  dot.className = "cdot " + (st && st.connected ? "ok" : "off");
  dot.textContent = "●";
  const name = document.createElement("span");
  name.className = "cname";
  name.textContent = "Claude";
  li.append(dot, name);

  if (st && st.connected) {
    const who = document.createElement("span");
    who.className = "cwho";
    who.textContent = "connected";
    li.append(who, mkBtn("✕", "disconnect", async () => {
      await fetch(rel("api/connections/claude"), { method: "DELETE" });
      refreshConnections();
    }));
  } else {
    li.append(mkBtn("接続", "sign in to Claude", (e) => startClaudeFlow(li, e.target)));
  }
  return li;
}

async function startClaudeFlow(li, btn) {
  btn.disabled = true;
  btn.textContent = "…";
  let res;
  try {
    res = await api("api/connections/claude/start", { method: "POST" });
  } catch {
    res = null;
  }
  if (!res || res.error || !res.url) {
    alert("Claude 認証開始に失敗: " + (res && res.error ? res.error.message || res.error : ""));
    refreshConnections();
    return;
  }
  btn.remove();
  const flow = document.createElement("div");
  flow.className = "claude-flow";
  const link = document.createElement("a");
  link.href = res.url;
  link.target = "_blank";
  link.rel = "noopener";
  link.textContent = "① ブラウザでサインイン";
  link.title = res.url;
  const code = document.createElement("input");
  code.className = "cinput";
  code.placeholder = "② コードを貼付";
  const done = mkBtn("完了", "complete sign-in", async () => {
    const c = code.value.trim();
    if (!c) return code.focus();
    done.disabled = true;
    done.textContent = "…";
    const r = await api("api/connections/claude/complete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: res.flow_id, code: c }),
    });
    if (r && r.error) {
      alert("接続に失敗: " + (r.error.message || r.error));
      done.disabled = false;
      done.textContent = "完了";
      return;
    }
    refreshConnections();
  });
  flow.append(link, code, done);
  li.append(flow);
  code.focus();
}

// Bitbucket Authorization Code Grant: open the authorize page in a new tab; the
// CP callback stores the tokens. We poll the connections status until connected.
async function bitbucketOAuthFlow(li, btn) {
  btn.disabled = true;
  btn.textContent = "…";
  let res;
  try {
    res = await api("api/connections/git/bitbucket/oauth/start");
  } catch {
    res = null;
  }
  if (!res || res.error || !res.authorize_url) {
    if (res && res.error && res.error.code === "not_configured") {
      alert("Bitbucket OAuth は未設定です（key/secret）。「token」からトークン貼付を使ってください。");
    } else {
      alert("OAuth 開始に失敗: " + (res && res.error ? res.error.message || res.error : ""));
    }
    refreshConnections();
    return;
  }
  window.open(res.authorize_url, "_blank", "noopener");
  btn.remove();
  const panel = document.createElement("div");
  panel.className = "oauth-flow";
  const link = document.createElement("a");
  link.href = res.authorize_url;
  link.target = "_blank";
  link.rel = "noopener";
  link.textContent = "承認ページを開く";
  const status = document.createElement("span");
  status.className = "oauth-status";
  status.textContent = "別タブで承認してください…";
  panel.append(link, status);
  li.append(panel);

  const deadline = Date.now() + 5 * 60 * 1000;
  const tick = async () => {
    if (Date.now() > deadline) {
      status.textContent = "タイムアウト。やり直してください";
      return;
    }
    let d;
    try {
      d = await api("api/connections");
    } catch {
      d = null;
    }
    if (d && d.bitbucket && d.bitbucket.connected) return refreshConnections();
    setTimeout(tick, 2000);
  };
  setTimeout(tick, 2500);
}

function gitConnRow(label, host, st, fields, oauthFn) {
  const li = document.createElement("li");
  li.className = "conn";
  const dot = document.createElement("span");
  dot.className = "cdot " + (st && st.connected ? "ok" : "off");
  dot.textContent = "●";
  const name = document.createElement("span");
  name.className = "cname";
  name.textContent = label;
  li.append(dot, name);

  if (st && st.connected) {
    const who = document.createElement("span");
    who.className = "cwho";
    who.textContent = st.username || "connected";
    who.title = st.username || "";
    li.append(who, mkBtn("✕", "disconnect", async () => {
      await fetch(rel(`api/connections/git/${host}`), { method: "DELETE" });
      refreshConnections();
    }));
  } else if (oauthFn) {
    // OAuth primary; token paste available via the "token" toggle.
    li.append(mkBtn("OAuth 接続", "sign in with OAuth", (e) => oauthFn(li, e.target)));
    const tog = mkBtn("token", "paste a token instead", () => {
      tog.remove();
      appendTokenForm(li, host, fields);
    });
    li.append(tog);
  } else {
    appendTokenForm(li, host, fields);
  }
  return li;
}

function appendTokenForm(li, host, fields) {
  const inputs = {};
  for (const f of fields) {
    const inp = document.createElement("input");
    inp.placeholder = f.ph;
    if (f.pw) inp.type = "password";
    inp.className = "cinput";
    inputs[f.key] = inp;
    li.append(inp);
  }
  li.append(mkBtn("接続", "connect", async () => {
    const body = {};
    for (const k in inputs) body[k] = inputs[k].value.trim();
    if (!body.token) return inputs.token.focus();
    const res = await api(`api/connections/git/${host}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (res && res.error) return alert("connect failed: " + (res.error.message || res.error));
    refreshConnections();
  }));
}

// GitHub Device Flow: show user_code + verification link, poll until approved.
async function githubOAuthFlow(li, btn) {
  btn.disabled = true;
  btn.textContent = "…";
  let res;
  try {
    res = await api("api/connections/git/github/oauth/start", { method: "POST" });
  } catch {
    res = null;
  }
  if (!res || res.error) {
    if (res && res.error && res.error.code === "not_configured") {
      alert("GitHub OAuth は未設定です（client_id）。「token」からトークン貼付を使ってください。");
    } else {
      alert("OAuth 開始に失敗: " + (res && res.error ? res.error.message || res.error : ""));
    }
    refreshConnections();
    return;
  }
  btn.remove();
  const panel = document.createElement("div");
  panel.className = "oauth-flow";
  const code = document.createElement("span");
  code.className = "oauth-code";
  code.textContent = res.user_code; // big, selectable, not truncated
  const link = document.createElement("a");
  link.href = res.verification_uri;
  link.target = "_blank";
  link.rel = "noopener";
  link.textContent = "→ " + res.verification_uri + " で入力";
  const status = document.createElement("span");
  status.className = "oauth-status";
  status.textContent = "承認待ち…";
  panel.append(code, link, status);
  li.append(panel);

  const deadline = Date.now() + (res.expires_in || 900) * 1000;
  let iv = (res.interval || 5) * 1000;
  const tick = async () => {
    if (Date.now() > deadline) {
      status.textContent = "期限切れ。やり直してください";
      return;
    }
    let p;
    try {
      p = await api("api/connections/git/github/oauth/poll", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: res.flow_id }),
      });
    } catch {
      p = null;
    }
    if (p && p.connected) return refreshConnections();
    if (p && p.error) {
      status.textContent = "失敗: " + (p.error.message || p.error.code || "");
      return;
    }
    if (p && p.interval) iv = p.interval * 1000; // slow_down
    setTimeout(tick, iv);
  };
  setTimeout(tick, iv);
}

$("conn-refresh").onclick = refreshConnections;

// --- repos ---
// A repo is a working copy under ~/repos/<name>; the folder name is its id.
const repoSafeSession = (name) => name.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 40) || "repo";

async function refreshRepos() {
  const list = $("repo-list");
  let data;
  try {
    data = await api("api/repos");
  } catch {
    list.innerHTML = "";
    return;
  }
  list.innerHTML = "";
  for (const r of data.repos || []) {
    list.append(repoRow(r));
  }
}

function repoRow(r) {
  const li = document.createElement("li");

  const dot = document.createElement("span");
  dot.className = "dot " + (r.dirty ? "dirty" : "clean");
  dot.title = r.dirty ? "uncommitted changes" : "clean";
  dot.textContent = "●";

  const name = document.createElement("span");
  name.className = "name";
  name.textContent = r.name;
  name.title = r.path;

  // Branch switcher: options are filled lazily on first interaction to avoid
  // an N+1 branch fetch for every repo on list.
  const branch = document.createElement("select");
  branch.className = "branch";
  branch.innerHTML = `<option>${r.branch || "?"}</option>`;
  let loaded = false;
  const loadBranches = async () => {
    if (loaded) return;
    loaded = true;
    try {
      const b = await api(`api/repos/${encodeURIComponent(r.name)}/branches`);
      branch.innerHTML = "";
      for (const name of b.local || []) {
        const o = document.createElement("option");
        o.value = name;
        o.textContent = name;
        if (name === b.current) o.selected = true;
        branch.append(o);
      }
    } catch {
      loaded = false;
    }
  };
  branch.onmousedown = loadBranches;
  branch.onchange = async () => {
    await api(`api/repos/${encodeURIComponent(r.name)}/checkout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ branch: branch.value }),
    });
    refreshRepos();
  };

  const ab = document.createElement("span");
  ab.className = "ab";
  ab.textContent = (r.ahead ? `↑${r.ahead}` : "") + (r.behind ? `↓${r.behind}` : "");

  const fetchBtn = mkBtn("⤓", "git fetch --prune", async () => {
    fetchBtn.disabled = true;
    await api(`api/repos/${encodeURIComponent(r.name)}/fetch`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prune: true }),
    });
    await refreshRepos();
  });

  const sessBtn = mkBtn("▶", "start a Claude session in this repo", async () => {
    const sname = repoSafeSession(r.name);
    await fetch(rel("api/sessions"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: sname, dir: r.path }),
    });
    await refreshSessions();
    attach(sname);
  });

  const delBtn = mkBtn("✕", "delete working copy", async () => {
    if (!confirm(`Delete working copy "${r.name}"? (history/remote untouched)`)) return;
    await fetch(rel(`api/repos/${encodeURIComponent(r.name)}`), { method: "DELETE" });
    refreshRepos();
  });

  const scmBtn = mkBtn("⎇", "source control (changes / diff / history)", () => openSCM(r.name));

  li.append(dot, name, branch, ab, fetchBtn, scmBtn, sessBtn, delBtn);
  return li;
}

// --- source control panel (docs/17 P3-5) ---
let scmRepo = null;

function openSCM(name) {
  scmRepo = name;
  $("scm-title").textContent = name;
  $("scm-diff").textContent = "";
  $("scm-pane").hidden = false;
  refreshSCM();
}

async function refreshSCM() {
  if (!scmRepo) return;
  const enc = encodeURIComponent(scmRepo);
  try {
    const st = await api(`api/repos/${enc}/status`);
    $("scm-branch").textContent =
      `${st.branch || "?"}${st.ahead ? ` ↑${st.ahead}` : ""}${st.behind ? ` ↓${st.behind}` : ""}`;
  } catch {}
  const cl = $("scm-changes");
  cl.innerHTML = "";
  try {
    const d = await api(`api/repos/${enc}/changes`);
    const changes = d.changes || [];
    if (!changes.length) cl.innerHTML = '<li class="muted">no changes</li>';
    for (const c of changes) cl.append(changeRow(c));
  } catch {
    cl.innerHTML = '<li class="muted">unavailable</li>';
  }
  const lg = $("scm-log");
  lg.innerHTML = "";
  try {
    const d = await api(`api/repos/${enc}/log?limit=50`);
    for (const c of d.commits || []) {
      const li = document.createElement("li");
      const code = document.createElement("code");
      code.textContent = c.short;
      const sub = document.createElement("span");
      sub.className = "subj";
      sub.textContent = " " + c.subject;
      const meta = document.createElement("span");
      meta.className = "muted";
      meta.textContent = `  ${c.author} · ${(c.date || "").slice(0, 10)}`;
      li.append(code, sub, meta);
      lg.append(li);
    }
  } catch {}
}

function changeRow(c) {
  const li = document.createElement("li");
  const staged = !c.untracked && c.index !== " ";
  const tag = document.createElement("span");
  tag.className = "chg " + (c.untracked ? "untracked" : staged ? "staged" : "unstaged");
  tag.textContent = c.untracked ? "U" : staged ? c.index : c.worktree;
  const nm = document.createElement("span");
  nm.className = "chg-name";
  nm.textContent = c.path;
  nm.title = c.path;
  nm.onclick = () => showDiff(c.path, staged);
  const acts = document.createElement("span");
  acts.className = "chg-acts";
  if (staged) acts.append(mkBtn("−", "unstage", () => scmOp("unstage", [c.path])));
  else acts.append(mkBtn("+", "stage", () => scmOp("stage", [c.path])));
  if (!c.untracked)
    acts.append(mkBtn("⤺", "discard changes", () => {
      if (confirm(`Discard changes to ${c.path}? This cannot be undone.`)) scmOp("discard", [c.path]);
    }));
  li.append(tag, nm, acts);
  return li;
}

async function showDiff(path, staged) {
  const enc = encodeURIComponent(scmRepo);
  try {
    const d = await api(`api/repos/${enc}/diff?path=${encodeURIComponent(path)}${staged ? "&staged=1" : ""}`);
    renderDiff(d.diff && d.diff.length ? d.diff : "(no textual diff)");
  } catch {
    renderDiff("(diff failed)");
  }
}

function renderDiff(text) {
  const pre = $("scm-diff");
  pre.innerHTML = "";
  for (const line of text.split("\n")) {
    const s = document.createElement("span");
    s.className =
      "dl " +
      (line.startsWith("+") && !line.startsWith("+++")
        ? "add"
        : line.startsWith("-") && !line.startsWith("---")
          ? "del"
          : line.startsWith("@@")
            ? "hunk"
            : "");
    s.textContent = line + "\n";
    pre.append(s);
  }
}

async function scmOp(op, paths) {
  const enc = encodeURIComponent(scmRepo);
  await fetch(rel(`api/repos/${enc}/${op}`), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ paths }),
  });
  refreshSCM();
}

$("scm-close").onclick = () => {
  $("scm-pane").hidden = true;
  scmRepo = null;
};
$("scm-refresh").onclick = () => refreshSCM();
$("scm-commit").onclick = async () => {
  if (!scmRepo) return;
  const msg = $("scm-msg").value.trim();
  if (!msg) {
    alert("commit message required");
    return;
  }
  const r = await fetch(rel(`api/repos/${encodeURIComponent(scmRepo)}/commit`), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message: msg, all: $("scm-all").checked }),
  });
  if (r.ok) {
    $("scm-msg").value = "";
    refreshSCM();
    refreshRepos();
  } else {
    const e = await r.json().catch(() => ({}));
    alert("commit failed: " + (e.error?.message || r.status));
  }
};

function mkBtn(label, title, onclick) {
  const b = document.createElement("button");
  b.className = "icon";
  b.textContent = label;
  b.title = title;
  b.onclick = onclick;
  return b;
}

$("repos-refresh").onclick = refreshRepos;
$("clone-repo").onsubmit = async (e) => {
  e.preventDefault();
  const url = $("cr-url").value.trim();
  const branch = $("cr-branch").value.trim();
  const btn = e.target.querySelector("button");
  btn.disabled = true;
  btn.textContent = "Cloning…";
  try {
    const res = await api("api/repos", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ remote_url: url, branch }),
    });
    if (res && res.error) {
      alert("clone failed: " + (res.error.message || res.error));
    } else {
      $("cr-url").value = "";
      $("cr-branch").value = "";
    }
  } finally {
    btn.disabled = false;
    btn.textContent = "Clone";
  }
  await refreshRepos();
};

// --- file explorer (docs/17 P3-5 段2) ---
async function fsList(path) {
  try {
    return await api(`api/fs/tree?path=${encodeURIComponent(path)}`);
  } catch {
    return { entries: [] };
  }
}

function fsRow(entry, parentPath) {
  const full = parentPath ? parentPath + "/" + entry.name : entry.name;
  const li = document.createElement("li");
  const label = document.createElement("span");
  label.className = "fs-" + entry.type;
  label.textContent = (entry.type === "dir" ? "▸ " : "   ") + entry.name;
  li.append(label);
  if (entry.type === "dir") {
    let open = false,
      childUl = null;
    label.onclick = async () => {
      open = !open;
      label.textContent = (open ? "▾ " : "▸ ") + entry.name;
      if (open) {
        if (!childUl) {
          childUl = document.createElement("ul");
          const d = await fsList(full);
          for (const c of d.entries || []) childUl.append(fsRow(c, full));
          li.append(childUl);
        } else childUl.style.display = "";
      } else if (childUl) childUl.style.display = "none";
    };
  } else {
    label.onclick = () => showFile(full);
  }
  return li;
}

async function openFiles() {
  $("fs-pane").hidden = false;
  $("fs-viewpath").textContent = "";
  $("fs-view").textContent = "select a file";
  const root = $("fs-tree");
  root.innerHTML = "";
  const d = await fsList("");
  for (const c of d.entries || []) root.append(fsRow(c, ""));
}

async function showFile(path) {
  try {
    const d = await api(`api/fs/file?path=${encodeURIComponent(path)}`);
    $("fs-viewpath").textContent = path + (d.truncated ? " (truncated)" : "");
    $("fs-view").textContent = d.binary ? `(binary file, ${d.size || 0} bytes)` : (d.content ?? "");
  } catch {
    $("fs-view").textContent = "(cannot read)";
  }
}

$("open-files").onclick = openFiles;
$("fs-refresh").onclick = openFiles;
$("fs-close").onclick = () => ($("fs-pane").hidden = true);

// --- on-demand sign-in URL copy ---
// Ink hard-wraps the /login auth URL across terminal rows, so neither plain
// copy nor web-links yields the whole URL. Reconstruct it from the xterm buffer
// (join full-width rows) only when the user asks — no auto-popup, no false hits.
function reconstructURL() {
  const buf = term.buffer.active,
    cols = term.cols;
  for (let y = buf.length - 1; y >= Math.max(0, buf.length - 200); y--) {
    const line = buf.getLine(y);
    if (!line) continue;
    const m = line.translateToString(true).match(/(https:\/\/[^\s]*)$/);
    if (!m) continue; // a URL fragment reaching the row end => wrapped onward
    let url = m[1];
    for (let yy = y + 1; yy < buf.length; yy++) {
      const seg = buf.getLine(yy)?.translateToString(true) ?? "";
      if (!seg || /[^\x21-\x7e]/.test(seg)) break; // non-URL char (incl space) => end
      url += seg;
      if (seg.length < cols) break; // shorter than width => last segment
    }
    if (/oauth|authorize/i.test(url)) return url;
  }
  return null;
}
$("copy-login-url").onclick = async () => {
  const btn = $("copy-login-url");
  const url = term ? reconstructURL() : null;
  if (!url) {
    btn.textContent = "no URL on screen";
  } else {
    try {
      await navigator.clipboard.writeText(url);
      btn.textContent = "Copied!";
    } catch {
      btn.textContent = "copy failed";
    }
  }
  setTimeout(() => (btn.textContent = "⧉ sign-in URL"), 1400);
};

// --- terminal ---
let term, fitAddon, ws;
function ensureTerm() {
  if (term) return;
  term = new Terminal({
    fontSize: 13,
    // JetBrains Mono を主に、末尾の CJK フォントで日本語等のフォールバックを効かせる。
    fontFamily:
      '"JetBrains Mono", "Cascadia Code", "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", "Noto Sans Mono CJK JP", "Noto Sans CJK JP", "Hiragino Kaku Gothic ProN", "Yu Gothic", monospace',
    theme: { background: "#1e1e1e" },
    cursorBlink: true,
    allowProposedApi: true,
  });
  fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  // Unicode 11 widths so emoji / wide glyphs occupy 2 cells (no half-clipping).
  if (window.Unicode11Addon) {
    term.loadAddon(new Unicode11Addon.Unicode11Addon());
    term.unicode.activeVersion = "11";
  }
  // Make URLs clickable (open in a new tab) so the /login auth URL needn't be
  // copied out of the terminal — it wraps across rows and breaks on copy.
  if (window.WebLinksAddon) {
    term.loadAddon(new WebLinksAddon.WebLinksAddon((e, uri) => window.open(uri, "_blank", "noopener")));
  }
  term.open($("terminal"));
  // Crisp GPU rendering; fall back silently to the default renderer if WebGL2
  // is unavailable or the context is lost.
  if (window.WebglAddon) {
    try {
      const webgl = new WebglAddon.WebglAddon();
      webgl.onContextLoss(() => webgl.dispose());
      term.loadAddon(webgl);
    } catch {}
  }
  // The web font loads async — refit/redraw once it's ready so metrics are right.
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(() => {
      try {
        fitAddon.fit();
        term.refresh(0, term.rows - 1);
      } catch {}
    });
  }
  fitAddon.fit();
  term.onData((d) => ws && ws.readyState === 1 && ws.send(JSON.stringify({ type: "input", data: d })));
  term.onResize(({ cols, rows }) => ws && ws.readyState === 1 && ws.send(JSON.stringify({ type: "resize", cols, rows })));
  window.addEventListener("resize", () => fitAddon && fitAddon.fit());
}
function attach(session) {
  ensureTerm();
  if (ws) ws.close();
  term.reset();
  $("term-title").textContent = `session: ${session}`;
  const u = new URL(rel("ws/terminal"));
  u.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  u.search = `?session=${encodeURIComponent(session)}` +
    (selectedTenant ? `&tenant=${encodeURIComponent(selectedTenant)}` : "");
  ws = new WebSocket(u);
  ws.binaryType = "arraybuffer";
  ws.onopen = () => { fitAddon.fit(); ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows })); };
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
    else term.write(ev.data);
  };
  ws.onclose = () => term.write("\r\n[disconnected]\r\n");
}

(async () => {
  await initTenants();
  reloadAll();
})();
