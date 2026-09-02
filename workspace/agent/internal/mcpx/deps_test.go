package mcpx

// mcpx 単体のテストは package main を持たないので、外向きの依存を**作り物で**配線する。
// 数字も文言も本物ではない（本物は main の alias_mcp.go が配線する）——ここで本物の値を
// 書き写すと、それ自体が二つ目の出所になる。

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mcpx 単体テストでの保管場所（本番は main の alias_mcp.go が持つ）。
var (
	testWrite, testSelfReport, testChromium bool
	testConvID                              string
)

func init() {
	Configure(Deps{
		WriteEnabled:              func() bool { return testWrite },
		SetWriteEnabled:           func(v bool) { testWrite = v },
		SelfReportOnly:            func() bool { return testSelfReport },
		SetSelfReportOnly:         func(v bool) { testSelfReport = v },
		SessionChromiumEnabled:    func() bool { return testChromium },
		SetSessionChromiumEnabled: func(v bool) { testChromium = v },
		ConvID:                    func() string { return testConvID },
		SetConvID:                 func(v string) { testConvID = v },

		CleanTitle: func(s string) (string, bool) {
			s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
			return s, s != ""
		},
		SessionTitleMaxRunes:  80,
		PeerIntentNames:       []string{"request", "question", "answer", "notice"},
		PeerReachableSessions: func(string) []session.Meta { return nil },
		ReportKindSelfReport:  "self-report",

		// 承認は既定で通す（作り物）。**承認そのものを検査するテストは main 側にある**
		// ——ゲートの本体が main にあるので、ここで偽物を検査しても意味が無い。
		ApprovalGate:      func(string, string) error { return nil },
		ApprovalLabel:     func(op string) string { return op },
		ShellCreateTarget: func(dir, prompt string) string { return dir + " " + prompt },
		ShellSendTarget:   func(name, prompt string) string { return name + " " + prompt },
		SessionIsShell:    func(string) bool { return false },

		ReadUIPrefs:                func() map[string]any { return map[string]any{} },
		EnsureClaudeSettingsWiring: func(string) {},

		RepoAnyDirFromPath: func(w http.ResponseWriter, r *http.Request) (string, bool) {
			dir := r.URL.Query().Get("dir")
			if dir == "" {
				http.Error(w, "no dir", http.StatusBadRequest)
				return "", false
			}
			return dir, true
		},

		ReadBuildPins:      func() map[string]string { return map[string]string{} },
		AgentFleetShareDir: func() string { return filepath.Join(os.Getenv("HOME"), ".local", "share", "agent-fleet") },
		InstallGrafanaMCP:  func(string) (string, error) { return "", os.ErrNotExist },

		WriteSSMConfig: func(string, session.SSMMeta) error { return nil },
	})
}

// withTempHome points HOME at a temp dir so the fstore stores write under the test's
// own tree（main 側の同名ヘルパと同じ 4 行。パッケージを跨げないので持つ）。
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// withMCPFlags は道具集合のフラグを一時的に据える（後片付けまで面倒を見る）。
func withMCPFlags(t *testing.T, write, selfReport, chromium bool) {
	t.Helper()
	ow, os, oc := writeEnabled(), selfReportOnly(), sessionChromiumEnabled()
	setFlags(write, selfReport, chromium)
	t.Cleanup(func() { setFlags(ow, os, oc) })
}

func setFlags(write, selfReport, chromium bool) {
	setWriteEnabled(write)
	setSelfReportOnly(selfReport)
	setSessionChromiumEnabled(chromium)
}
