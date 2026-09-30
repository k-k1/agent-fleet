package main

// A deployment that signs real people in (AUTH other than dev) but has no AF_MASTER_KEY issues
// no DEK, and every Workspace then writes its credential store — git tokens, the Claude token,
// API keys, MCP and chat connections — as plaintext JSON in the member's home. That is the intended dev behaviour and a
// silent leak anywhere else (#1080).
//
// ⚠️ A warning, not a refusal to start. Deployments already running without a key would stop
// on upgrade, and setting the key is not a quiet fix they can make on the spot: a running
// workspace keeps its keyless environment until it is stopped and started, and the Agent then
// reads secrets.enc and never migrates or deletes secrets.json, so every member has to
// reconnect what they had stored, and the old file has to be deleted and its credentials
// rotated. The operator has to plan that, so the CP says it loudly at start and to super_admins
// in the Console instead.

// deploymentWarnPlaintextSecrets is the code GET /api/admin/tenants carries in
// deployment_warnings; the Console renders it as admin.deploy_warn_plaintext_secrets.
const deploymentWarnPlaintextSecrets = "plaintext_secrets"

const plaintextSecretsLog = "WARNING: AUTH=%s but AF_MASTER_KEY is not set: workspace credentials " +
	"(git tokens, the Claude token, API keys, MCP and chat connections) are stored UNENCRYPTED in each member's home. " +
	"Set AF_MASTER_KEY and restart, stop and start every existing workspace, then have members reconnect " +
	"their credentials (see guide/operate/04-secure.md)"

// plaintextSecrets reports whether this deployment stores workspace credentials unencrypted
// while signing real people in.
func (m *manager) plaintextSecrets() bool {
	return m.authMode != "dev" && (len(m.master32) == 0 || m.custodian == nil)
}

// deploymentWarnings lists the deployment-level problems a super_admin must see. Never nil,
// so the wire always carries an array.
func (m *manager) deploymentWarnings() []string {
	out := []string{}
	if m.plaintextSecrets() {
		out = append(out, deploymentWarnPlaintextSecrets)
	}
	return out
}
