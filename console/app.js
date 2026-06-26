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
  await fetch(rel("api/sessions"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, dir }),
  });
  $("ns-name").value = "";
  $("ns-dir").value = "";
  await refreshSessions();
  attach(name);
};

// --- terminal ---
let term, fitAddon, ws;
function ensureTerm() {
  if (term) return;
  term = new Terminal({ fontSize: 13, theme: { background: "#1e1e1e" }, cursorBlink: true });
  fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  // Make URLs clickable (open in a new tab) so the /login auth URL needn't be
  // copied out of the terminal — it wraps across rows and breaks on copy.
  if (window.WebLinksAddon) {
    term.loadAddon(new WebLinksAddon.WebLinksAddon((e, uri) => window.open(uri, "_blank", "noopener")));
  }
  term.open($("terminal"));
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
refreshSessions();
