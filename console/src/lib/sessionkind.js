// Session kind → display icon (codicon name) + label. Shared by the Sessions list,
// the Repos launch menu, the New Session modal, and the archive modal so the kind
// presentation stays consistent in one place.
export const kindIcon = (k) =>
  k === "shell" ? "terminal" : k === "opencode" ? "hubot" : k === "codex" ? "rocket" : "sparkle";
export const kindLabel = (k) =>
  k === "shell" ? "shell" : k === "opencode" ? "opencode" : k === "codex" ? "codex" : "claude";
