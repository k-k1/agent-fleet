// Phase 1 MVP Console: workspace controls, session list, and an xterm.js
// terminal bridged to /ws/terminal (which the Control Plane proxies to the
// Workspace Agent's PTY).

const $ = (id) => document.getElementById(id);

// Resolve URLs relative to where the Console is mounted, so it works both at
// the host root (http://localhost:8099/) and behind a path-stripping proxy
// (e.g. Tailscale Funnel + Caddy at /agent-fleet/). Use the trailing-slash URL.
const rel = (p) => new URL(p, document.baseURI).toString();
const api = (path, opts) => fetch(rel(path), opts).then((r) => r.json());

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
  const btn = e.target.querySelector("button");
  btn.disabled = true;
  btn.textContent = remote_url ? "Cloning…" : "…";
  try {
    const res = await api("api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, dir, remote_url, branch }),
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
  list.append(gitConnRow("GitHub", "github.com", data.github, [{ key: "token", ph: "Personal Access Token", pw: true }]));
  list.append(gitConnRow("Bitbucket", "bitbucket.org", data.bitbucket, [
    { key: "username", ph: "Atlassian email" },
    { key: "token", ph: "API token", pw: true },
  ]));
}

function gitConnRow(label, host, st, fields) {
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
  } else {
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
  return li;
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

  li.append(dot, name, branch, ab, fetchBtn, sessBtn, delBtn);
  return li;
}

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
  u.search = `?session=${encodeURIComponent(session)}`;
  ws = new WebSocket(u);
  ws.binaryType = "arraybuffer";
  ws.onopen = () => { fitAddon.fit(); ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows })); };
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
    else term.write(ev.data);
  };
  ws.onclose = () => term.write("\r\n[disconnected]\r\n");
}

refreshWorkspace();
refreshConnections();
refreshRepos();
refreshSessions();
