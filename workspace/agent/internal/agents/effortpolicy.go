package agents

// EffortPolicy is an optional Agent capability: how a caller that cannot see the CLI (MCP
// create_session) should treat an effort it is asked to set, before it reads the model
// catalog. A kind without it is checked against its catalog row like claude and codex.
// Resolved by exact registry key, never through the claude fallback.
type EffortPolicy interface {
	EffortTraits() EffortTraits
}

// EffortRule is how a kind takes an effort at create time.
type EffortRule int

const (
	// EffortByCatalog is the zero value: the effort must be one the chosen model's catalog row
	// lists.
	EffortByCatalog EffortRule = iota
	// EffortInModelID: the effort is part of the model id, so a separate one is refused.
	EffortInModelID
	// EffortNever: the kind takes no effort; Reason, when set, says why.
	EffortNever
	// EffortCLIJudges: the catalog lists no efforts yet the CLI takes one, so the CLI is the
	// only judge of the value and nothing is refused here. A guessed list would refuse real
	// values.
	EffortCLIJudges
)

// EffortTraits is a kind's create-time effort policy.
type EffortTraits struct {
	Rule EffortRule
	// Reason is the parenthetical explanation of an EffortNever refusal. Empty: none.
	Reason string
	// AutoRejectsEffort: a blank model or "auto" (any case) launches the CLI's automatic
	// choice, which refuses --effort, so an effort is refused for it.
	AutoRejectsEffort bool
}
