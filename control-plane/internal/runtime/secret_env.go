// secret_env.go — which workspace environment variables are credentials, and how the
// adapters keep them out of places a task definition or a process listing exposes.
//
// The CP builds one KEY=VAL list for a workspace. Most of it is configuration, but the
// per-membership bridge tokens, the internal git token and the dev IdP client secret are
// bearer credentials. They used to travel as plain task-definition `environment` on ECS and
// as argv `-e` on docker. Old task-definition revisions are never deleted and ECS
// DescribeTaskDefinition (and CloudTrail's RegisterTaskDefinition) returns `environment`
// verbatim, so any principal that can read task definitions could read them; argv is readable
// in /proc/<pid>/cmdline. The adapters therefore route every name below through the channel
// AGENT_TOKEN already uses: an SSM SecureString under /af-ws/<workspace>/ referenced by the
// container's `secrets` (ECS), a 0600 --env-file (docker), a Secret (kubernetes).
package runtime

import (
	"sort"
	"strings"
)

// secretEnvNames is the closed list of credential variables. A name is added here when the
// CP starts injecting a new bearer credential; TestSecretEnvNamesCoverCPCredentials in the CP
// module's root fails when workspaceExtraEnv emits a *_TOKEN / *_SECRET that is not listed.
var secretEnvNames = []string{
	"AF_MEMO_TOKEN",
	"AF_SCHEDULE_TOKEN",
	"AF_MCP_TOKEN",
	"AF_DOCS_TOKEN",
	"AF_ENGINE_ISSUE_TOKEN",
	"AF_GIT_OAUTH_TOKEN",
	"AF_AWS_PROFILES_TOKEN",
	"AF_GCP_PROFILES_TOKEN",
	"AF_BRANCH_RULES_TOKEN",
	"AF_INTERNAL_GIT_TOKEN",
	"AF_DEV_IDP_CLIENT_SECRET",
}

// SecretEnvNames returns a copy of the credential variable names.
func SecretEnvNames() []string { return append([]string(nil), secretEnvNames...) }

// IsSecretEnv reports whether name is a credential variable (see secretEnvNames).
func IsSecretEnv(name string) bool {
	for _, n := range secretEnvNames {
		if n == name {
			return true
		}
	}
	return false
}

// secretEnvPair is one credential variable of a start.
type secretEnvPair struct{ Name, Value string }

// splitSecretEnv separates env into the plain entries (order kept) and the credential
// entries (sorted by name, the last duplicate winning, empty values dropped). The sort keeps
// the ECS-EC2 task-definition fingerprint stable across starts.
func splitSecretEnv(env []string) (plain []string, secret []secretEnvPair) {
	last := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && IsSecretEnv(k) {
			last[k] = v
			continue
		}
		plain = append(plain, kv)
	}
	for k, v := range last {
		if v != "" {
			secret = append(secret, secretEnvPair{k, v})
		}
	}
	sort.Slice(secret, func(i, j int) bool { return secret[i].Name < secret[j].Name })
	return plain, secret
}

// secretEnvParamSuffix is the SSM parameter's last path element for a credential variable:
// /af-ws/<workspace>/<suffix>. Lower-case with hyphens, like the existing agent-token.
func secretEnvParamSuffix(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}
