// Presentation helpers for a session object, shared by the left-pane Sessions
// list and the terminal pane header so both render a session identically: a
// display name, and a status chip (codicon + label + color class).

// stamp formats a timestamp as MMDD-HHMM (matching the agent's claude --name), so
// shell rows show a launch time consistent with claude rows.
export const stamp = (iso) => {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
};

// displayName: a claude session's --name (minus the "[AF] " tag); shell sessions
// (no --name) use "{repo} @MMDD-HHMM". The kind is shown by the badge, so no
// [AF]/[SH] prefix is needed.
export const displayName = (s) => {
  if (s.kind !== "shell" && s.label) return s.label.replace(/^\[AF\]\s*/, "");
  return `${s.repo || s.name} @${stamp(s.createdAt)}`;
};

// stateInfo maps a session to its status chip (codicon + label + color class).
export const stateInfo = (s) => {
  if (!s.alive) {
    // A stopped claude whose working dir was deleted can't be resumed (archive only).
    if (s.resumable === false) return { cls: "off dead", icon: "circle-slash", text: "フォルダ無し — 再開不可" };
    return { cls: "off", icon: "debug-pause", text: "停止中" };
  }
  if (s.kind === "shell") return { cls: "on", icon: "pulse", text: "起動中" };
  // claude (hooks), opencode (plugin) and codex (injected hooks) all report
  // working/idle. opencode/codex have no "question" state; an empty state = idle.
  switch (s.state) {
    case "working":
      return { cls: "working", icon: "loading", spin: true, text: "進行中…" };
    case "question":
      return { cls: "question", icon: "question", text: "質問あり" };
    case "plan":
      return { cls: "question", icon: "checklist", text: "プランあり" };
    case "permission":
      return { cls: "question", icon: "shield", text: "許可待ち" };
    default:
      return { cls: "on", icon: "check", text: "入力待ち" };
  }
};
