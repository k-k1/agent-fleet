package main

import "github.com/k-k1/agent-fleet/control-plane/internal/auth"

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

// deploymentWarnAuthDev is the code GET /api/admin/tenants carries in deployment_warnings for
// AUTH=dev on a container runtime; the Console renders it as admin.deploy_warn_auth_dev.
const deploymentWarnAuthDev = "auth_dev"

const authDevLog = "WARNING: AUTH=dev with " + auth.AllowInsecureDevAuthEnv + "=1: there is NO authentication and every " +
	"request is a fixed super_admin. Anyone who can reach this Control Plane controls every workspace. " +
	"Use it on a machine only you can reach; set AUTH=oauth or AUTH=proxy for anything shared " +
	"(see guide/operate/04-secure.md)"

// authDevInsecure reports AUTH=dev on a container runtime (reachable only through the explicit
// opt-in), the case the Console banner exists for. The native runtimes are single-user by design.
func (m *manager) authDevInsecure() bool {
	return m.authMode == "dev" && !m.nativeRuntime
}

// deploymentWarnEfsBackupMissing is the code for an ECS deployment on Persistence=retain whose
// data stack has no daily EFS backup plan; the Console renders it as
// admin.deploy_warn_efs_backup_missing.
const deploymentWarnEfsBackupMissing = "efs_backup_missing"

const efsBackupMissingLog = "WARNING: the data stack runs Persistence=retain but has no daily EFS backup " +
	"(vault and plan were added to 10-data in 0.26.0); see deploy/aws/ecs/README.md, \"EFS backup and restore\""

// efsBackupMissing reports AF_EFS_BACKUP=missing on an ECS runtime. The ingress stack carries
// that marker (update.sh / standup.sh fill it from the data stack's outputs), so the CP needs
// no AWS permission to know. Anything else, including an unset or empty marker, means "not
// known to be missing": when in doubt answer false, a false alarm teaches operators to ignore it.
func efsBackupMissing(runtime, marker string) bool {
	return (runtime == "ecs" || runtime == "ecs-ec2") && marker == "missing"
}

// deploymentWarnings lists the deployment-level problems a super_admin must see. Never nil,
// so the wire always carries an array.
func (m *manager) deploymentWarnings() []string {
	out := []string{}
	if m.plaintextSecrets() {
		out = append(out, deploymentWarnPlaintextSecrets)
	}
	if m.authDevInsecure() {
		out = append(out, deploymentWarnAuthDev)
	}
	if m.efsBackupMissing {
		out = append(out, deploymentWarnEfsBackupMissing)
	}
	return out
}
