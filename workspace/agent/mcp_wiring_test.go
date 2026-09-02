package main

// mcp_wiring.go の配線が**生きているか**を通しで見る 1 本。
//
// 🔥 `mcpx.Configure` が捕まえるのは**未配線**（nil / 零値）だけで、**間違った配線**は
// 捕まえられない。実際に踏める形が 3 つある:
//
//   - `ApprovalGate` を「常に承認」にする  → **オペレーター承認が丸ごと消える**
//   - `WriteEnabled` を `return false` 固定 → 書き込み道具が出ない（黙って機能が消える）
//   - `ConvID` を `return ""` 固定        → 完了報告の宛先が失われる
//
// どれも配線 1 行の書き換えで、しかもその行は「写しの罠を避けるため閉包にした」という
// **正しい理由で書かれている**ぶん、将来の整理で触られやすい。移送でカバレッジが落ちた
// わけではない（移送前も同種のテストは無い）が、**壊せる面が増えた**のは確かなので、
// ここで 1 本止める。
//
// 検査の形は 2 つ:
//
//   - ただの関数は**関数ポインタの同一性**（別の関数や閉包にすり替わっていれば落ちる）
//   - 閉包で受けている 4 組（写しにできない値）は**往復**（main 側の変数 → mcpx の getter、
//     mcpx の setter → main 側の変数）
//
// そして **Deps のフィールド集合と検査の集合を突き合わせる**ので、フィールドが増えたのに
// 検査を足さなければここが落ちる。

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func TestMCPWiringIsLive(t *testing.T) {
	w := mcpx.Wired()

	checks := map[string]func(t *testing.T){
		// --- 値で渡しているもの（本物の定数と同じであること） ---
		"SessionTitleMaxRunes": func(t *testing.T) {
			if w.SessionTitleMaxRunes != sessionTitleMaxRunes {
				t.Fatalf("件名の上限 = %d, want %d", w.SessionTitleMaxRunes, sessionTitleMaxRunes)
			}
		},
		"ReportKindSelfReport": func(t *testing.T) {
			if w.ReportKindSelfReport != reportKindSelfReport {
				t.Fatalf("report kind = %q, want %q", w.ReportKindSelfReport, reportKindSelfReport)
			}
		},
		"PeerIntentNames": func(t *testing.T) {
			if !reflect.DeepEqual(w.PeerIntentNames, peerIntentNames) {
				t.Fatalf("intent の一覧 = %v, want %v", w.PeerIntentNames, peerIntentNames)
			}
		},

		// --- 関数（本物と同一であること） ---
		"CleanTitle":                 func(t *testing.T) { sameFunc(t, w.CleanTitle, cleanTitle) },
		"PeerReachableSessions":      func(t *testing.T) { sameFunc(t, w.PeerReachableSessions, peerReachableSessions) },
		"ApprovalGate":               func(t *testing.T) { sameFunc(t, w.ApprovalGate, bridgeApprovalGate) },
		"ApprovalLabel":              func(t *testing.T) { sameFunc(t, w.ApprovalLabel, approvalLabel) },
		"ShellCreateTarget":          func(t *testing.T) { sameFunc(t, w.ShellCreateTarget, shellCreateTarget) },
		"ShellSendTarget":            func(t *testing.T) { sameFunc(t, w.ShellSendTarget, shellSendTarget) },
		"SessionIsShell":             func(t *testing.T) { sameFunc(t, w.SessionIsShell, sessionIsShell) },
		"ReadUIPrefs":                func(t *testing.T) { sameFunc(t, w.ReadUIPrefs, uiprefs.Read) },
		"EnsureClaudeSettingsWiring": func(t *testing.T) { sameFunc(t, w.EnsureClaudeSettingsWiring, ensureClaudeSettingsWiring) },
		"RepoAnyDirFromPath":         func(t *testing.T) { sameFunc(t, w.RepoAnyDirFromPath, repoAnyDirFromPath) },
		"ReadBuildPins":              func(t *testing.T) { sameFunc(t, w.ReadBuildPins, readBuildPins) },
		"AgentFleetShareDir":         func(t *testing.T) { sameFunc(t, w.AgentFleetShareDir, agentFleetShareDir) },
		"InstallGrafanaMCP":          func(t *testing.T) { sameFunc(t, w.InstallGrafanaMCP, installGrafanaMCP) },
		"WriteSSMConfig":             func(t *testing.T) { sameFunc(t, w.WriteSSMConfig, writeSSMConfig) },

		// --- 写しにできない 4 組（往復で見る） ---
		"WriteEnabled":      func(t *testing.T) { roundTripBool(t, &mcpWriteEnabled, w.WriteEnabled, w.SetWriteEnabled) },
		"SetWriteEnabled":   func(t *testing.T) { roundTripBool(t, &mcpWriteEnabled, w.WriteEnabled, w.SetWriteEnabled) },
		"SelfReportOnly":    func(t *testing.T) { roundTripBool(t, &mcpSelfReportOnly, w.SelfReportOnly, w.SetSelfReportOnly) },
		"SetSelfReportOnly": func(t *testing.T) { roundTripBool(t, &mcpSelfReportOnly, w.SelfReportOnly, w.SetSelfReportOnly) },
		"SessionChromiumEnabled": func(t *testing.T) {
			roundTripBool(t, &mcpSessionChromiumEnabled, w.SessionChromiumEnabled, w.SetSessionChromiumEnabled)
		},
		"SetSessionChromiumEnabled": func(t *testing.T) {
			roundTripBool(t, &mcpSessionChromiumEnabled, w.SessionChromiumEnabled, w.SetSessionChromiumEnabled)
		},
		"ConvID":    func(t *testing.T) { roundTripConvID(t) },
		"SetConvID": func(t *testing.T) { roundTripConvID(t) },
	}

	// 検査の集合と Deps のフィールド集合を突き合わせる。**フィールドが増えたら必ずここが
	// 落ちる**ので、「配線は足したが検査は足さなかった」が起きない。
	typ := reflect.TypeOf(w)
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := checks[name]; !ok {
			t.Errorf("Deps.%s の配線を検査していない（フィールドを足したら検査も足すこと）", name)
		}
	}
	for name := range checks {
		if !seen[name] {
			t.Errorf("Deps に %s は無い（検査だけが古い）", name)
		}
	}
	for name, run := range checks {
		t.Run(name, run)
	}
}

// sameFunc は「その関数そのものが配線されている」ことを見る。閉包や別の関数に
// すり替わっていれば、コードポインタが違うので落ちる。
func sameFunc(t *testing.T, got, want any) {
	t.Helper()
	g, w := reflect.ValueOf(got).Pointer(), reflect.ValueOf(want).Pointer()
	if g != w {
		t.Fatalf("配線先が違う: got %s, want %s", funcName(g), funcName(w))
	}
}

func funcName(pc uintptr) string {
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	return "?"
}

// roundTripBool は main 側の変数と mcpx の読み書きが**同じ実体**を指していることを見る。
// 片道（getter だけ / setter だけ）では「固定値を返す配線」を捕まえられない。
func roundTripBool(t *testing.T, home *bool, get func() bool, set func(bool)) {
	t.Helper()
	old := *home
	t.Cleanup(func() { *home = old })

	*home = true
	if !get() {
		t.Fatal("main 側で立てた値が mcpx から見えない（getter が固定値を返している）")
	}
	*home = false
	if get() {
		t.Fatal("main 側で倒した値が mcpx から見えない（getter が固定値を返している）")
	}
	set(true)
	if !*home {
		t.Fatal("mcpx の setter が main 側の変数を書いていない（写しになっている）")
	}
}

func roundTripConvID(t *testing.T) {
	t.Helper()
	w := mcpx.Wired()
	old := mcpConvID
	t.Cleanup(func() { mcpConvID = old })

	mcpConvID = "conv-wiring-probe"
	if got := w.ConvID(); got != "conv-wiring-probe" {
		t.Fatalf("mcpx から見た会話 id = %q（main 側の代入が届いていない）", got)
	}
	w.SetConvID("conv-from-mcpx")
	if mcpConvID != "conv-from-mcpx" {
		t.Fatalf("main 側の会話 id = %q（mcpx の setter が届いていない）", mcpConvID)
	}
}
