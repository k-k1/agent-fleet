package agents

import "github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"

// ConnectionStatuser is an optional Agent capability: the kind's entry in GET /connections,
// under its kind name as the JSON key. A kind without it (shell, ssm) has no entry. The status
// never carries a secret.
type ConnectionStatuser interface {
	ConnectionStatus(StatusInputs) map[string]any
}

// StatusInputs is what a ConnectionStatus may read beyond the kind's own files. Each field is
// meaningful to the kinds that use it and ignored by the rest.
type StatusInputs struct {
	// Secrets is the member's loaded encrypted store, never nil.
	Secrets *secrets.Data
	// GithubConnected is whether the github.com git connection is set (copilot rides on it).
	GithubConnected bool
}
