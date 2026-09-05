package main

// mcp_wiring.go — holds nothing but the wiring of the MCP family (`internal/mcpx`). What
// lives here are not aliases but the two things that can only sit on the main side:
//
//   - The storage for "the four that cannot be copies" (below).
//   - `mcpx.Configure`, which wires the 19 reverse mcpx → main dependencies as function
//     values. mcpx cannot import main, so this is the only way (internal/mcpx/deps.go).

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The four that cannot be copies keep their storage on this side; mcpx is handed only the
// read/write ports (see the note in internal/mcpx/deps.go). Two reasons, both of them
// instances of "taking a var through an alias breaks it":
//
//   - `--conv <id>` and `--write` are decided after the assignment at startup, when the
//     arguments are parsed. A copy stays frozen at ""/false, and bridge_approval.go posts
//     the approval without knowing which conversation it belongs to.
//   - package main's tests assign to these directly to switch mcpx's tool set, then read the
//     resulting interpretation back (chat_report_test.go / bridge_approval_test.go). With a
//     copy the assignment never arrives, and the test goes green while the stub does nothing.
var (
	mcpConvID                 string
	mcpWriteEnabled           bool
	mcpSelfReportOnly         bool
	mcpSessionChromiumEnabled bool
)

// --- mcpx → main -----------------------------------------------------------
//
// A missing wire makes mcpx.Configure panic. Never fill one in silently with a default: an
// approval gate that waves everything through unwired is worse than not starting at all.
func init() {
	mcpx.Configure(mcpx.Deps{
		CleanTitle:           sessionx.CleanTitle,
		SessionTitleMaxRunes: sessionx.SessionTitleMaxRunes,

		PeerIntentNames:       sessionx.PeerIntentNames,
		PeerReachableSessions: sessionx.PeerReachableSessions,

		ReportKindSelfReport: chatx.ReportKindSelfReport,

		ApprovalGate:      sessionx.BridgeApprovalGate,
		ApprovalLabel:     sessionx.ApprovalLabel,
		ShellCreateTarget: sessionx.ShellCreateTarget,
		ShellSendTarget:   sessionx.ShellSendTarget,
		SessionIsShell:    sessionx.SessionIsShell,

		ReadUIPrefs:                uiprefs.Read,
		EnsureClaudeSettingsWiring: sessionx.EnsureClaudeSettingsWiring,

		RepoAnyDirFromPath: gitx.RepoAnyDirFromPath,

		ReadBuildPins:      readBuildPins,
		AgentFleetShareDir: agentFleetShareDir,
		InstallGrafanaMCP:  installGrafanaMCP,

		WriteSSMConfig: sessionx.WriteSSMConfig,

		WriteEnabled:              func() bool { return mcpWriteEnabled },
		SetWriteEnabled:           func(v bool) { mcpWriteEnabled = v },
		SelfReportOnly:            func() bool { return mcpSelfReportOnly },
		SetSelfReportOnly:         func(v bool) { mcpSelfReportOnly = v },
		SessionChromiumEnabled:    func() bool { return mcpSessionChromiumEnabled },
		SetSessionChromiumEnabled: func(v bool) { mcpSessionChromiumEnabled = v },

		ConvID:    func() string { return mcpConvID },
		SetConvID: func(id string) { mcpConvID = id },
	})
}
