package cursor

// InstructionsUnsupported is agents.InstructionsUnsupported: no user layer exists locally.
// User Rules live on the Cursor account side (aiserver.v1.UserRules) and every local rules
// source is project-scoped (measured, docs/log/60 §60.3). That is a structural impossibility
// rather than missing wiring, so it is not shown as pending implementation.
func (agentImpl) InstructionsUnsupported() string { return "no_user_scope" }
