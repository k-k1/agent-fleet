// agent-fleet caller identity for opencode's af MCP tools (#989).
//
// A Managed opencode session cannot tell af's MCP child which session it is: the MCP config
// is global and opencode spawns one child per project directory, so sessions sharing a
// worktree share one child and no per-process environment can carry AF_SESSION_NAME
// (contract_mcp_identity_test.go). The one per-call channel opencode has is this hook: it
// fires for MCP tools too, carries opencode's own session id, and `output.args` is the very
// object handed to the MCP client's tools/call (measured on 1.18.32,
// contract_caller_plugin_test.go).
//
// So for af's own tools — and only those, a third-party server must never see this — the
// plugin stamps the calling session id into a reserved argument. The af server strips it
// before decoding and resolves it against the Agent's slot → opencode-session mapping
// (mcpx.mcpCallerSession); a value it cannot match is ignored, never taken as a name.
//
// The value is always OVERWRITTEN: the stamped args are also what opencode stores as the tool
// part's input and replays to the model, so a model can learn the key and write it itself.
//
// af's server name is minted per boot as `af_` + 8 hex digits (mcpreg/af_server_name.go);
// opencode keys an MCP tool `<server>_<tool>`. The legacy bare `af` name is deliberately not
// matched: `af_` alone is a prefix any other tool could have. Such a session keeps the cwd
// fallback it had before.
const AF_TOOL = /^af_[0-9a-f]{8}_/;
const CALLER_ARG = "_af_caller_sid";

export const AgentFleetCaller = async () => ({
  "tool.execute.before": async (input, output) => {
    if (!AF_TOOL.test(String(input?.tool ?? ""))) return;
    const args = output?.args;
    if (!args || typeof args !== "object" || typeof input?.sessionID !== "string") return;
    args[CALLER_ARG] = input.sessionID;
  },
});
