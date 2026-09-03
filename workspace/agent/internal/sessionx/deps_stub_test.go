package sessionx

// deps_stub_test.go — sessionx のテストが `chatx` / `gitx` を通るための偽配線。
//
// 移送前、rate_limit_resume_test.go は package main で走っており、chatx は本番の
// `chat_wiring.go` の init が配線していた。sessionx のテストバイナリには main が居ないので、
// `chatx.Configure` を自分で呼ばないと `deps.rateLimitState` が nil で落ちる
// （実測: TestRateLimitResumeNoteOnFailedReport が chat_report.go:226 で SIGSEGV）。
//
// 🔥 **44 フィールドを手で並べない。** 手書きの一覧は chatx.Deps が増えた日に漏れ、
// しかも漏れると `Configure` が panic するので**テスト全体が落ちて理由が読めなくなる**。
// ここは reflect で全フィールドを埋め、**関数フィールドは「踏んだら落ちる」**にする
// （internal/gitx/deps_test.go の `unreached` と同じ考え方。作り物の戻り値を置くと、
// 将来ここへ到達する検査が増えたときに**嘘の値で静かに緑になる**）。
//
// 実際に踏むのは **rateLimitState ただ 1 本**（測定して確かめた）。そこだけ本物と同じ
// 読み方をする —— sessionx の `RateLimitStates` ストアを引く。main 側の
// `chat_wiring.go` も同じストアを引いているので、写しではなく**同じ出所**である。

import (
	"fmt"
	"reflect"
	"testing"

	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func init() {
	chatx.Configure(chatxStubDeps())
	gitx.Configure(gitxStubDeps())
	mcpx.Configure(mcpxStubDeps())
}

// mcpxStubDeps —— 管理セッションの起動（HandleSessionDriver → mcpx.StartManagedSession）が
// mcpx を通るため。main の mcp_wiring.go が **sessionx へ移った実体**を配線している分は、
// ここでも同じ実体を繋ぐ（写しではなく本物）。
func mcpxStubDeps() mcpx.Deps {
	var d mcpx.Deps
	fillStubDeps("mcpx", &d)

	d.CleanTitle = CleanTitle
	d.SessionTitleMaxRunes = SessionTitleMaxRunes
	d.PeerIntentNames = PeerIntentNames
	d.PeerReachableSessions = PeerReachableSessions
	d.ApprovalGate = BridgeApprovalGate
	d.ApprovalLabel = ApprovalLabel
	d.ShellCreateTarget = ShellCreateTarget
	d.ShellSendTarget = ShellSendTarget
	d.SessionIsShell = SessionIsShell
	d.EnsureClaudeSettingsWiring = EnsureClaudeSettingsWiring
	d.WriteSSMConfig = WriteSSMConfig

	// main と同じく本物を使う（uiprefs は純粋な下位パッケージ）。
	d.ReadUIPrefs = uiprefs.Read

	// 可変のフラグ 4 組。main では package var だが、sessionx のテストバイナリには
	// その var が無いので、**ここに閉じたローカル状態**で同じ形（getter/setter 対）を作る。
	var writeEnabled, selfReportOnly, chromiumEnabled bool
	var convID string
	d.WriteEnabled = func() bool { return writeEnabled }
	d.SetWriteEnabled = func(v bool) { writeEnabled = v }
	d.SelfReportOnly = func() bool { return selfReportOnly }
	d.SetSelfReportOnly = func(v bool) { selfReportOnly = v }
	d.SessionChromiumEnabled = func() bool { return chromiumEnabled }
	d.SetSessionChromiumEnabled = func(v bool) { chromiumEnabled = v }
	d.ConvID = func() string { return convID }
	d.SetConvID = func(id string) { convID = id }
	return d
}

// fillStubDeps は Deps 構造体を「全部埋まっているが、関数は踏んだら落ちる」状態にする。
// pkg は panic 文言に出す名前。
func fillStubDeps(pkg string, ptr any) {
	v := reflect.ValueOf(ptr).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Func:
			name := f.Name
			fv.Set(reflect.MakeFunc(f.Type, func(args []reflect.Value) []reflect.Value {
				panic("sessionx のテストが " + pkg + "." + name + " を踏んだ。" +
					"移送時の実測では踏まれていない依存である —— 作り物の戻り値を置く前に、" +
					"main 側の配線と同じ振る舞いを写すか、その検査を package main へ置くかを決めること")
			}))
		case reflect.String:
			fv.SetString("sessionx-stub:" + f.Name)
		case reflect.Map:
			fv.Set(reflect.MakeMapWithSize(f.Type, 1))
			fv.SetMapIndex(reflect.ValueOf("sessionx-stub").Convert(f.Type.Key()),
				reflect.Zero(f.Type.Elem()))
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(f.Type, 1, 1))
		case reflect.Int, reflect.Int64:
			fv.SetInt(1)
		case reflect.Bool:
			fv.SetBool(true)
		default:
			panic(fmt.Sprintf("%s.Deps.%s の型 %s を埋める方法が無い（フィールドが増えた——ここを直すこと）",
				pkg, f.Name, f.Type))
		}
	}
}

// gitxStubDeps —— worktree 系の検査（TestEnsureWorktree ほか）が gitx を通るため。
// 実測で踏むのは ScratchAutoRelocate 1 本だけで、main の scratch.go と同じく
// **AF_WS_SCRATCH が無ければ何もしない**（no-op を置くと、env が立った環境でだけ
// 本物と挙動が変わる）。
func gitxStubDeps() gitx.Deps {
	var d gitx.Deps
	fillStubDeps("gitx", &d)

	// main の git_wiring.go が **sessionx へ移った実体**を配線している 5 本は、
	// ここでも同じ実体を繋ぐ。**写しではなく本物**なので、gitx 越しに踏む検査
	// （TestMaybePruneWorktreeKeeps など）が移送前と同じものを見る。
	d.AbsPath = AbsPath
	d.RepoLocked = RepoLocked
	d.LockedRepoDirs = LockedRepoDirs
	d.LiveSessionsInDir = LiveSessionsInDir
	d.LockedSessionsInDir = LockedSessionsInDir
	d.WorktreeHasSessions = WorktreeHasSessions
	d.ManagedAlive = ManagedAlive

	// main の scratch.go と同じく **AF_WS_SCRATCH が無ければ何もしない**
	// （no-op を置くと、env が立った環境でだけ本物と挙動が変わる）。
	d.ScratchAutoRelocate = func(dir string) {}
	return d
}

// chatxStubDeps は chatx.Deps を「全部埋まっているが、踏んだら落ちる」状態で作る。
func chatxStubDeps() chatx.Deps {
	var d chatx.Deps
	fillStubDeps("chatx", &d)

	// main の chat_wiring.go が **sessionx へ移った実体**を配線している 21 本は、
	// ここでも同じ実体を繋ぐ（**写しではなく本物**）。一覧は手で並べず、
	// chatx.Deps のフィールド名と sessionx の公開名を突き合わせて作った。
	d.FilterVisibleModels = FilterVisibleModels
	d.VisibleModel = VisibleModel
	d.VisibleModelIDs = VisibleModelIDs
	d.CleanSuggestedTitle = CleanSuggestedTitle
	d.TitleModel = TitleModel
	d.TitleSuggestFooter = TitleSuggestFooter
	d.TitleSuggestInstructions = TitleSuggestInstructions
	d.TitleSuggestPersona = TitleSuggestPersona
	d.TitleSuggestTimeout = TitleSuggestTimeout
	d.CleanSuggestedReplies = CleanSuggestedReplies
	d.ReplyCounterpartChat = ReplyCounterpartChat
	d.ReplySuggestEnabled = ReplySuggestEnabled
	d.ReplySuggestInstructions = ReplySuggestInstructions
	d.ReplySuggestLogHeader = ReplySuggestLogHeader
	d.ReplySuggestModel = ReplySuggestModel
	d.ReplySuggestPersona = ReplySuggestPersona
	d.ReplySuggestTimeout = ReplySuggestTimeout
	d.AbortResumeHolds = AbortResumeHolds
	d.CleanTitle = CleanTitle
	d.NormalizeKind = NormalizeKind
	d.ReplySuggestWindow = func(b *strings.Builder, msgs []chatx.ReplyMsg) {
		// main の chat_wiring.go と同じ詰め替え（chatx は sessionx の型を名指しできない）。
		out := make([]ReplyMsg, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, ReplyMsg{Role: m.Role, Text: m.Text})
		}
		ReplySuggestWindow(b, out)
	}

	// 唯一、本物と同じ振る舞いが要るもの。sessionx の RateLimitStates ストアを引く。
	d.RateLimitState = func(name string) (string, string, bool) {
		st, ok := RateLimitStates.Read(name)
		if !ok {
			return "", "", false
		}
		return st.ScheduleID, st.ResumeAt, true
	}
	return d
}

// TestDepsStubsAreExhaustive は、上の reflect が chatx / gitx の Deps を**本当に全部埋めた**ことを見る。
//
// 🔥 これが無いと、chatx 側にフィールドが増えた日に `chatx.Configure` の panic が
// **init の中**で起きる —— init の panic はテスト名を持たないので、CI では
// 「パッケージごと落ちた」としか見えず、原因が読めない（#321 の「落ちると固まるは別」の親戚）。
// ここで名前付きの失敗にしておく。
func TestDepsStubsAreExhaustive(t *testing.T) {
	for _, c := range []struct {
		pkg string
		d   any
	}{{"chatx", chatxStubDeps()}, {"gitx", gitxStubDeps()}, {"mcpx", mcpxStubDeps()}} {
		v := reflect.ValueOf(c.d)
		tp := v.Type()
		if tp.NumField() == 0 {
			t.Fatalf("%s.Deps のフィールドが 0 = この検査が無言化している", c.pkg)
		}
		for i := 0; i < tp.NumField(); i++ {
			if v.Field(i).IsZero() {
				t.Errorf("%s.Deps.%s が埋まっていない（Configure が init で panic する）",
					c.pkg, tp.Field(i).Name)
			}
		}
	}
}
