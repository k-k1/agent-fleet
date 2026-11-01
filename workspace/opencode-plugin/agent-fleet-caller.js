// agent-fleet caller identity for opencode's af MCP tools (#989).
//
// A Managed opencode session cannot tell af's MCP child which session it is: the MCP config
// is global and opencode spawns one child per project directory, so sessions sharing a
// worktree share one child and no per-process environment can carry AF_SESSION_NAME. The one
// per-call channel opencode has is the tool pre-execution hook: it fires for MCP tools too,
// carries opencode's own session id, and the input it mutates is the very object handed to the
// MCP client's tools/call.
//
// So for af's own tools — and only those, a third-party server must never see this — the
// plugin stamps the calling session id into a reserved argument. The af server strips it
// before decoding and resolves it against the Agent's slot → opencode-session mapping
// (mcpx.mcpCallerSession); a value it cannot match is ignored, never taken as a name.
//
// The value is always OVERWRITTEN: the stamped args are also what opencode stores as the tool
// part's input and replays to the model, so a model can learn the key and write it itself.
//
// opencode 2 only half-delivers this (measured on 2.0.28 against a stub MCP server): the
// stamp reaches a tool whose inputSchema is open, and is dropped when the schema is closed —
// af's are (`additionalProperties: false`). There the server reads the session id from
// `_meta["ai.opencode/sessionID"]`, which opencode 2 sends on every tools/call and the model
// cannot write (mcpx.metaCallerSID). The hook stays so an open-schema af tool still gets the
// same stamp, and a forged value can never outrank the real one: the server lets a stamp win.
//
// af's server name is minted per boot as `af_` + 8 hex digits (mcpreg/af_server_name.go);
// opencode keys an MCP tool `<server>_<tool>` (measured on 2.0.28: the hook
// also fires for the call made inside the Code Mode `execute` tool, as `af_0123abcd_probe_tool`).
// The legacy bare `af` name is deliberately not matched: `af_` alone is a prefix any other tool
// could have. Such a session keeps the cwd fallback it had before.
//
// No dependency on @opencode/plugin: opencode 2 reads `id` and `setup` from a plain
// default-exported object (measured 2.0.28).
const AF_TOOL = /^af_[0-9a-f]{8}_/;
const CALLER_ARG = "_af_caller_sid";

const stamp = (tool, sessionID, args) => {
  if (!AF_TOOL.test(String(tool ?? ""))) return;
  if (!args || typeof args !== "object" || typeof sessionID !== "string") return;
  args[CALLER_ARG] = sessionID;
};

export default {
  id: "agent-fleet.caller",
  // One mutable event: `event.input` is the tool's argument object.
  async setup(ctx) {
    await ctx.tool.hook("execute.before", (event) => stamp(event?.tool, event?.sessionID, event?.input));
  },
};
