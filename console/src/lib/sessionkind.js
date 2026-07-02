// Session kind → display icon (codicon name) + label. Shared by the Sessions list,
// the Repos launch menu, the New Session modal, and the archive modal so the kind
// presentation stays consistent in one place.
export const kindIcon = (k) =>
  k === "shell" ? "terminal"
  : k === "opencode" ? "hubot"
  : k === "codex" ? "rocket"
  : k === "ssm" ? "cloud"
  : "sparkle";
export const kindLabel = (k) =>
  k === "shell" ? "shell"
  : k === "opencode" ? "opencode"
  : k === "codex" ? "codex"
  : k === "ssm" ? "ssm"
  : "claude";
// 2-char abbreviation for tight spots (narrow pane headers): shown next to the
// icon when the full label would wrap. claude=cc, codex=cx, opencode=oc, shell=sh,
// ssm=aw (AWS SSM).
export const kindShort = (k) =>
  k === "shell" ? "sh"
  : k === "opencode" ? "oc"
  : k === "codex" ? "cx"
  : k === "ssm" ? "aw"
  : "cc";
// Canonical kind slug for CSS color classes (.kind-<slug>); mirrors kindLabel so
// unknown kinds fall back to "claude".
export const kindClass = (k) =>
  k === "shell" ? "shell"
  : k === "opencode" ? "opencode"
  : k === "codex" ? "codex"
  : k === "ssm" ? "ssm"
  : "claude";
