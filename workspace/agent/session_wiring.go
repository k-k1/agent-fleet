package main

// session_wiring.go wires `internal/sessionx`'s outward dependencies (sessionx → main) in one
// place.
//
// The other direction (main → sessionx) lives in alias_session.go as aliases. They are two
// files because the aliases are peeled away wholesale at a wave boundary while this wiring
// stays: sessionx's need for errcodes.go and fs.go outlives the reclamation.
//
// The name follows the sibling families (git_wiring.go / mcp_wiring.go / memory_wiring.go):
// family name + _wiring. The destination package is `internal/sessionx` rather than
// `internal/session` because the latter is a model leaf (16 packages import it, including 7
// under agents) and merging into it would create a cycle. A wiring file's name names the
// family, not the destination.
//
// Never give the wiring a default value. Anything left unwired makes `sessionx.Configure`
// panic. Zero values are worst for value types: `MaxUploadBytes` of 0 rejects every upload as
// "too large", and an empty error code sends `""` to the Console, where i18n cannot resolve it
// and the raw developer message is exposed — both fail quietly.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"

func init() { sessionx.Configure(sessionDeps()) }

// sessionDeps is the production wiring. The completeness check on the sessionx side
// (internal/sessionx/deps_test.go) uses fabricated values, so this is the only place the real
// ones are written.
//
// It is therefore also the only place a swap can be caught: all 11 error codes are `string`,
// and 2 of the 9 functions (BrowseRoot / ToolchainShellPrefix) share the type `func() string`,
// so mixing two up trips neither the type checker nor the reflect-based completeness check.
// session_wiring_test.go compares each one against the real value.
func sessionDeps() sessionx.Deps {
	return sessionx.Deps{
		EnvOr:            envOr,
		FirstNonEmpty:    firstNonEmpty,
		SplitFrontmatter: splitFrontmatter,

		BrowseRoot:     browseRoot,
		MaxUploadBytes: maxUploadBytes,

		IsSvnRepo:       isSvnRepo,
		RepoJobsRunning: repoJobsRunning,

		FinalizeSessionUsage:  finalizeSessionUsage,
		MaybeFoldSessionUsage: maybeFoldSessionUsage,

		RemoveTerminalHistory: removeTerminalHistory,

		ToolchainShellPrefix: toolchainShellPrefix,

		// mcpConvID is a var mcp_wiring.go rewrites at run time, so pass a reader
		// rather than the value: passing the value pins approval prompts to whatever
		// conversation id existed at wiring time.
		MCPConvID:       func() string { return mcpConvID },
		RunOperatorTurn: runOperatorTurn,

		ErrCodeAgentNotConnected:      errCodeAgentNotConnected,
		ErrCodeChatConversationNotFnd: errCodeChatConversationNotFnd,
		ErrCodeForkAtUnsupported:      errCodeForkAtUnsupported,
		ErrCodeForkBadAnchor:          errCodeForkBadAnchor,
		ErrCodeForkMissingDir:         errCodeForkMissingDir,
		ErrCodeForkUnsupportedKind:    errCodeForkUnsupportedKind,
		ErrCodeLocked:                 errCodeLocked,
		ErrCodePasteTooLarge:          errCodePasteTooLarge,
		ErrCodePasteUnsupportedAgent:  errCodePasteUnsupportedAgent,
		ErrCodePasteUnsupportedKind:   errCodePasteUnsupportedKind,
		ErrCodeTitleFeatureDisabled:   errCodeTitleFeatureDisabled,
		ErrCodeTitleNoContent:         errCodeTitleNoContent,
	}
}
