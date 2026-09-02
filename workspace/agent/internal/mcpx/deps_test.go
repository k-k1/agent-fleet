package mcpx

// mcpx 単体のテストは package main を持たないので、外向きの依存を**作り物で**配線する。
// 数字も文言も本物ではない（本物は main の alias_mcp.go が配線する）——ここで本物の値を
// 書き写すと、それ自体が二つ目の出所になる。

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mcpx 単体テストでの保管場所（本番は main の alias_mcp.go が持つ）。
var (
	testWrite, testSelfReport, testChromium bool
	testConvID                              string
)

func init() { Configure(testDeps()) }

// testDeps は mcpx 単体テスト用の作り物一式。**網羅性の検査（下）が同じものを使う**ので、
// 1 箇所に置く。
func testDeps() Deps {
	return Deps{
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
	}
}

// 🔥 Deps の**どのフィールドを 1 つ落としても** Configure が落ちること。
//
// 網羅の検査そのものを reflect で回すので、**フィールドが増えたら自動で対象になる**。
// 手で並べた一覧（実装側もテスト側も）は、増えたときに漏れて、しかも漏れても何も起きない
// ——落ちるのは「件名の上限が黙って 0 になる」ような、誰も気付かない側である。
func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	good := testDeps()
	v := reflect.ValueOf(good)
	typ := v.Type()
	if typ.NumField() < 20 {
		t.Fatalf("Deps のフィールドが %d 個しか無い（構造体を取り違えている）", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			// 正しい配線へ必ず戻す（deps はパッケージ全体で共有する）。
			defer Configure(good)
			broken := reflect.New(typ).Elem()
			broken.Set(v)
			broken.Field(i).Set(reflect.Zero(f.Type))
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s を未配線にしても Configure が通った（配線漏れが静かに素通りする）", f.Name)
				}
				if !strings.Contains(fmt.Sprint(r), f.Name) {
					t.Fatalf("panic に %s の名前が出ていない: %v", f.Name, r)
				}
			}()
			Configure(broken.Interface().(Deps))
		})
	}
}

// 零値ではないが中身の無い配線も拒むこと（`unwired` の枝を 1 つずつ踏む）。
// 零値だけを見ていると、**`[]string{}` や負の上限**が「配線済み」として通る。
func TestConfigureRejectsHollowValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		bend func(*Deps)
	}{
		{"SessionTitleMaxRunes が負", func(d *Deps) { d.SessionTitleMaxRunes = -1 }},
		{"PeerIntentNames が空スライス", func(d *Deps) { d.PeerIntentNames = []string{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer Configure(testDeps())
			d := testDeps()
			tc.bend(&d)
			defer func() {
				if recover() == nil {
					t.Fatalf("%s でも Configure が通った", tc.name)
				}
			}()
			Configure(d)
		})
	}
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
